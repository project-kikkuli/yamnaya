package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

const exhaustedReason = "default login exhausted: weekly limit, resets Nov 5 3am UTC"

// turnHarness reports one session, on pid only, as whatever *s says now.
type turnHarness struct {
	switchingHarness
	s   *core.Session
	pid int
	at  time.Time
}

func (h turnHarness) Discover(ps []proc.Proc) []core.Session {
	var out []core.Session
	for _, p := range ps {
		if p.PID == h.pid {
			s := *h.s
			s.PID, s.TTY = p.PID, p.TTY
			out = append(out, s)
		}
	}
	return out
}
func (h turnHarness) InspectHandoff(core.Session) (time.Time, error) { return h.at, nil }
func (h turnHarness) QuotaDialog(string) bool                        { return true }

// queueWorld is one session on the default login, with claude-work and personal to move to and a fake native binary.
type queueWorld struct {
	l                 launcher
	session           *core.Session
	alive, external   *bool
	signals           *int
	ran, state, authd string
	rel, data         string
	def, work         profile.Profile
	rec               launch.Record
}

func newQueueWorld(t *testing.T, harness, transcript string) queueWorld {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	state, cwd, bins := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", state)
	t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
	for _, k := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "CODEX_SQLITE_HOME"} {
		t.Setenv(k, "")
	}
	ran, authd := filepath.Join(t.TempDir(), "resumed"), filepath.Join(t.TempDir(), "authed")
	script := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in *\" auth status \"*|*\" login status \"*) : > %s; printf '%%s\\n' '{\"loggedIn\":true,\"authMethod\":\"claude.ai\"}'; exit 0;; esac\nprintf '%%s\\n' \"$YAMNAYA_PROFILE\" \"$*\" > %s.tmp && mv %s.tmp %s\nexec sleep 30\n", shellQuote(authd), shellQuote(ran), shellQuote(ran), shellQuote(ran))
	putProfileFile(t, filepath.Join(bins, harness), script)
	if err := os.Chmod(filepath.Join(bins, harness), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(profile.EnvKey(harness), t.TempDir())
	var work profile.Profile
	for _, id := range []string{"claude-work", "personal"} {
		p := profile.Profile{Harness: harness, ID: id, Root: t.TempDir(), Explicit: true}
		if err := profile.Add(state, p); err != nil {
			t.Fatal(err)
		}
		p.Root = physicalProfileRoot(p.Root)
		subscriptionIdentity(t, &p)
		if id == "claude-work" {
			work = p
		}
	}
	ps, err := profile.Load(state)
	if err != nil {
		t.Fatal(err)
	}
	def, err := profile.Find(ps, harness, "default")
	if err != nil {
		t.Fatal(err)
	}
	subscriptionIdentity(t, &def)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	rel, data := filepath.Join("projects", "p", profileConversation+".jsonl"), profileResponse(harness, stamp, "first", 100, 20)+transcript
	if harness == "codex" {
		rel = filepath.Join("sessions", "rollout-2026-10-23T10-00-00-"+profileConversation+".jsonl")
		data = profileMeta(profileConversation, "", 0) + profileResponse(harness, stamp, "first", 100, 20)
	}
	putProfileFile(t, filepath.Join(def.Root, rel), data)
	st, err := core.OpenStore(filepath.Join(state, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	session := &core.Session{Harness: harness, ID: profileConversation, Profile: "default", Account: def.Account, ConfigRoot: def.Root, Status: core.Busy, Cwd: cwd}
	alive, external, signals := true, false, 0
	l := testLauncher(t, state)
	l.core = &core.Core{Store: st, StateDir: state, Hosts: []core.Host{quotaHost{text: "quota dialog"}},
		Harnesses: []core.Harness{turnHarness{switchingHarness: switchingHarness{session: *session}, s: session, pid: 4242, at: time.Now().Add(-time.Minute)}},
		Procs: func() ([]proc.Proc, error) {
			var out []proc.Proc
			if alive {
				out = append(out, proc.Proc{PID: 4242, TTY: "/dev/ttys999", Args: []string{harness, "resume", profileConversation}})
			}
			if external {
				out = append(out, proc.Proc{PID: 5555, Args: []string{harness, "app-server"}})
			}
			return out, nil
		},
		Signal: func(int, syscall.Signal) error { signals++; alive = false; return nil },
	}
	l.core.Launches = l
	l.rootOf = func(pid int, key string) string {
		switch {
		case key == "CODEX_HOME":
			return def.Root
		case key == "YAMNAYA_LAUNCH_KEY" && pid == 4242:
			return "conversation"
		}
		return ""
	}
	rec := launch.Record{Key: "conversation", Harness: harness, Session: profileConversation, NativePID: 4242, Dedicated: harness == "codex", Adopted: true, Worktree: launch.Worktree{Path: cwd}, AttachedAt: time.Now().Add(-time.Hour)}
	setRecordProfile(&rec, def)
	if err = l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	return queueWorld{l: l, session: session, alive: &alive, external: &external, signals: &signals, ran: ran, state: state, authd: authd, rel: rel, data: data, def: def, work: work, rec: rec}
}

// row is what the board's session row says of the login: the pending switch, else the current login.
func (w queueWorld) row(t *testing.T) string {
	t.Helper()
	b, err := w.l.core.Board()
	if err != nil {
		t.Fatal(err)
	}
	s, ok := b.Session(profileConversation)
	if !ok {
		for _, n := range b.Launched {
			if n.Session == profileConversation && n.Switching != nil {
				return n.Switching.String()
			}
		}
		return "no row"
	}
	if s.Switching == nil {
		return profile.ID(s.Profile)
	}
	return s.Switching.String()
}

func (w queueWorld) serve(t *testing.T) {
	t.Helper()
	b, err := w.l.core.Board()
	if err != nil {
		t.Fatal(err)
	}
	if err = w.l.RecoverProfiles(b); err != nil {
		t.Fatal(err)
	}
}

func (w queueWorld) queue(t *testing.T) {
	t.Helper()
	out, err := w.l.QueueProfileSwitch(w.rec.Key, "claude-work", exhaustedReason)
	if err != nil || !out.Queued || *w.signals != 0 {
		t.Fatalf("busy switch = %+v, %v, signals %d; want it queued without a signal", out, err, *w.signals)
	}
}

func (w queueWorld) migration(t *testing.T) migration {
	t.Helper()
	m, err := loadMigration(w.state, w.rec.Harness, profileConversation)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// applied checks the session resumed the same conversation under claude-work and returns its resume arguments.
func (w queueWorld) applied(t *testing.T) string {
	t.Helper()
	if *w.signals == 0 {
		t.Fatal("the switch never stopped the session")
	}
	saved, _ := w.l.records.Find(w.rec.Key)
	if saved.Profile != "claude-work" || saved.Session != profileConversation || saved.ConfigRoot != w.work.Root {
		t.Fatalf("record after apply = %+v; want claude-work with the same conversation", saved)
	}
	got := strings.SplitN(resumed(t, w.ran), "\n", 2)
	if len(got) != 2 || got[0] != "claude-work" || !strings.Contains(got[1], profileConversation) {
		t.Fatalf("resume = %q; want the same conversation under claude-work", got)
	}
	if copied, err := os.ReadFile(filepath.Join(w.work.Root, w.rel)); err != nil || string(copied) != w.data {
		t.Fatalf("conversation not carried to claude-work: %v", err)
	}
	if m := w.migration(t); m.State != "running" || m.Blocked != "" {
		t.Fatalf("applied switch = %+v; want running", m)
	}
	if p := w.l.PendingSwitches(); len(p) != 0 {
		t.Fatalf("board still shows a pending switch: %+v", p)
	}
	return got[1]
}

func TestAQueuedSwitchAppliesWhenTheSessionItselfEndsItsTurn(t *testing.T) {
	background := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_run","name":"Bash","input":{"command":"go test ./...","description":"Run the full suite","run_in_background":true}}]}}
{"type":"user","message":{"content":[{"tool_use_id":"toolu_run","type":"tool_result","content":"Command running in background with ID: b1run."}]},"toolUseResult":{"stdout":"","backgroundTaskId":"b1run"}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_done","name":"Bash","input":{"command":"git fetch origin","run_in_background":true}}]}}
{"type":"user","message":{"content":[{"tool_use_id":"toolu_done","type":"tool_result","content":"Command running in background with ID: b2done."}]},"toolUseResult":{"stdout":"","backgroundTaskId":"b2done"}}
{"type":"user","message":{"content":[{"tool_use_id":"toolu_agent","type":"tool_result","content":"Async agent launched"}]},"toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"a65be36e","description":"Slack sweep"}}
{"type":"queue-operation","operation":"enqueue","content":"<task-notification>\n<task-id>b2done</task-id>\n<status>completed</status>\n</task-notification>"}
`
	for _, c := range []struct {
		name, harness string
		tasks         []string
	}{
		{name: "claude with background work", harness: "claude", tasks: []string{"background Bash b1run: Run the full suite", "background Agent a65be36e: Slack sweep"}},
		{name: "codex", harness: "codex"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, c.harness, background)
			w.queue(t)
			if got := w.row(t); got != "switch pending -> claude-work" {
				t.Fatalf("board row while busy = %q", got)
			}
			w.serve(t)
			if *w.signals != 0 || w.row(t) != "switch pending -> claude-work" {
				t.Fatalf("ym serve applied the switch mid-turn: signals %d, row %q", *w.signals, w.row(t))
			}

			w.session.Status = core.Idle
			w.session.Prompt = &core.Prompt{Tool: "Bash"}
			w.serve(t)
			if *w.signals != 0 {
				t.Fatal("ym serve applied the switch under an open dialog")
			}
			w.session.Prompt = nil
			w.session.Crew = []core.Member{{ID: "a65be36e", Name: "Slack sweep", Status: core.Busy}, {ID: "t7", Name: "reviewer", Status: core.Busy}, {ID: "t8", Name: "finished", Status: core.Done}}
			w.serve(t)

			args := w.applied(t)
			note := "ym switched this conversation from profile default to profile claude-work. Reason: " + exhaustedReason + "."
			if !strings.Contains(args, note) {
				t.Fatalf("resume note = %q; want %q", args, note)
			}
			if strings.Count(args, "a65be36e") > 1 || !strings.Contains(args, "subagent reviewer (t7)") || strings.Contains(args, "finished") {
				t.Errorf("resume note names the wrong crew: %q", args)
			}
			for _, task := range c.tasks {
				if !strings.Contains(args, task) {
					t.Errorf("resume note misses %q: %q", task, args)
				}
			}
			if strings.Contains(args, "b2done") {
				t.Errorf("resume note lists a finished background task: %q", args)
			}

			// The session reads the note, so a later revive does not repeat it.
			*w.alive = true
			w.session.Profile, w.session.ConfigRoot, w.session.Account = "claude-work", w.work.Root, w.work.Account
			appendProfileFile(t, filepath.Join(w.work.Root, w.rel), `{"type":"user","message":{"content":"`+note+`"}}`+"\n")
			w.serve(t)
			saved, _ := w.l.records.Find(w.rec.Key)
			if saved.Handoff != "" || !w.migration(t).NoteRead {
				t.Fatalf("note not settled: handoff %q, %+v", saved.Handoff, w.migration(t))
			}
		})
	}
}

func TestAnExhaustedLoginsQueuedSwitchAppliesAtItsQuotaStop(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			w := newQueueWorld(t, harness, "")
			w.queue(t)
			at := time.Now().Add(-time.Second)
			db, err := sql.Open("sqlite", filepath.Join(w.state, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			name, why := "7d", "subscription_rate_limit"
			if harness == "codex" {
				name, why = "codex 7d", "usage_limit_exceeded"
			}
			event, _ := json.Marshal(core.Exhaustion{Harness: harness, Profile: "default", Account: w.def.Account, Session: profileConversation, Reason: why, Seen: at})
			window, _ := json.Marshal(core.Limit{Harness: harness, Profile: "default", Account: w.def.Account, Name: name, Used: 100, Seen: at, ResetsAt: time.Now().Add(32 * time.Hour)})
			if _, err = db.Exec(`CREATE TABLE ym_usage_exhaustions(body TEXT);CREATE TABLE ym_usage_limits(body TEXT)`); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO ym_usage_exhaustions VALUES(?)`, string(event)); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO ym_usage_limits VALUES(?)`, string(window)); err != nil {
				t.Fatal(err)
			}
			if err = saveFailover(w.state, harness, failoverAttempt{State: core.FailoverState{Session: profileConversation, Profile: "default", Account: w.def.Account, Status: "queued"}}); err != nil {
				t.Fatal(err)
			}
			w.session.Status = core.Waiting
			w.serve(t)
			if args := w.applied(t); !strings.Contains(args, "Reason: "+exhaustedReason) {
				t.Fatalf("resume note = %q", args)
			}
			a, err := loadFailover(w.state, harness, profileConversation)
			if err != nil || a.State.Status != "switched" || a.State.Profile != "claude-work" {
				t.Fatalf("failover record = %+v, %v; want switched to claude-work", a.State, err)
			}
		})
	}
}

func TestASecondSwitchReplacesOrCancelsTheQueuedOne(t *testing.T) {
	for _, c := range []struct {
		name, harness, to, row string
		blocked                bool
		pending                bool
	}{
		{name: "another login replaces it", harness: "claude", to: "personal", row: "switch pending -> personal", pending: true},
		{name: "the same login stays queued", harness: "claude", to: "claude-work", row: "switch pending -> claude-work", pending: true},
		{name: "the current login cancels it", harness: "claude", to: "default", row: "default"},
		{name: "another login replaces a blocked one", harness: "codex", to: "personal", blocked: true, row: "switch -> personal blocked: external/shared Codex lifecycle pid 5555", pending: true},
		{name: "the current login cancels a blocked one", harness: "codex", to: "default", blocked: true, row: "default"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, c.harness, "")
			w.queue(t)
			if c.blocked {
				*w.external = true
				w.session.Status = core.Idle
				w.serve(t)
				if m := w.migration(t); m.State != "queued" || !strings.Contains(m.Blocked, "external/shared") {
					t.Fatalf("quiesce block = %+v; want queued with the blocker", m)
				}
			}
			_, err := w.l.QueueProfileSwitch(w.rec.Key, c.to, "changed my mind")
			if c.blocked && c.pending {
				if err == nil {
					t.Fatal("a retarget that is still blocked reported success")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if got := w.row(t); !strings.HasPrefix(got, c.row) {
				t.Fatalf("row = %q, want %q", got, c.row)
			}
			if m := w.migration(t); c.pending != (m.Version != 0) || c.pending && m.State != "queued" {
				t.Fatalf("record = %+v, pending %v", m, c.pending)
			}
			saved, _ := w.l.records.Find(w.rec.Key)
			if saved.Profile != "default" || *w.signals != 0 {
				t.Fatalf("the session moved: %q, signals %d", saved.Profile, *w.signals)
			}
		})
	}
}

func TestAQuiesceBlockStaysVisibleAndAppliesOnceItClears(t *testing.T) {
	for _, c := range []struct {
		name, blocker string
		refuse        func(t *testing.T, w queueWorld) func()
	}{
		{name: "shared owner", blocker: "external/shared Codex lifecycle pid 5555", refuse: func(t *testing.T, w queueWorld) func() {
			*w.external = true
			return func() { *w.external = false }
		}},
		{name: "live writer", blocker: "native thread publication is active", refuse: func(t *testing.T, w queueWorld) func() {
			release, err := writerLock(w.work.Root, profileConversation)
			if err != nil {
				t.Fatal(err)
			}
			return func() {
				release()
				appendProfileFile(t, filepath.Join(w.def.Root, w.rel), "")
				if err := os.Chtimes(filepath.Join(w.def.Root, w.rel), time.Now(), time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "codex", "")
			w.queue(t)
			clear := c.refuse(t, w)
			w.session.Status = core.Idle
			w.serve(t)
			saved, _ := w.l.records.Find(w.rec.Key)
			if *w.signals != 0 || saved.Profile != "default" {
				t.Fatalf("blocked switch stopped or rebound the session: signals %d, profile %q", *w.signals, saved.Profile)
			}
			if got := w.row(t); !strings.HasPrefix(got, "switch -> claude-work blocked: "+c.blocker) {
				t.Fatalf("board row = %q, want the blocker", got)
			}
			b, err := w.l.core.Board()
			if err != nil {
				t.Fatal(err)
			}
			if listed := sessionsJSON(w.l.core, b); len(listed) != 1 || listed[0].Switching == nil || !strings.Contains(listed[0].Switching.Blocked, c.blocker) {
				t.Fatalf("ym sessions lost the blocker: %+v", listed)
			}
			if m := w.migration(t); m.State != "queued" {
				t.Fatalf("blocked switch = %+v; want queued", m)
			}
			if _, err := os.Stat(filepath.Join(w.work.Root, w.rel)); !os.IsNotExist(err) {
				t.Fatal("blocked switch published the conversation")
			}
			clear()
			w.serve(t)
			if args := w.applied(t); !strings.Contains(args, "Reason: "+exhaustedReason) {
				t.Fatalf("resume note after the block cleared = %q", args)
			}
		})
	}
}

func TestASessionEndedWhileItsSwitchIsQueuedStaysEnded(t *testing.T) {
	for _, how := range []string{"killed", "landed"} {
		t.Run(how, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			w.queue(t)
			*w.alive = false
			saved, _ := w.l.records.Find(w.rec.Key)
			switch how {
			case "killed":
				saved.Killed = time.Now()
			case "landed":
				saved.Landed = time.Now()
			}
			if err := w.l.records.Save(saved); err != nil {
				t.Fatal(err)
			}
			w.serve(t)
			if m := w.migration(t); m.Version != 0 {
				t.Fatalf("record still open: %+v", m)
			}
			after, _ := w.l.records.Find(w.rec.Key)
			if after.Profile != "default" || !after.Killed.Equal(saved.Killed) || !after.Landed.Equal(saved.Landed) {
				t.Fatalf("record changed: %+v", after)
			}
			if _, state := w.l.state(after); state == "running" {
				t.Fatal("the ended session came back")
			}
			if _, err := os.Stat(w.ran); !os.IsNotExist(err) {
				t.Fatal("the ended session was resumed")
			}
		})
	}
}

func TestAnExitedSessionKeepsItsQueuedSwitchForItsRevive(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			w := newQueueWorld(t, harness, "")
			w.queue(t)
			*w.alive = false
			w.serve(t)
			if got := w.row(t); got != "switch pending -> claude-work" {
				t.Fatalf("row = %q; want the switch still pending on the launch row", got)
			}
			saved, _ := w.l.records.Find(w.rec.Key)
			if _, state := w.l.state(saved); state == "running" || saved.Profile != "default" {
				t.Fatalf("serve resumed the exited session: %s on %s", state, saved.Profile)
			}
			if _, err := os.Stat(w.ran); !os.IsNotExist(err) {
				t.Fatal("serve resumed the exited session")
			}
			got, err := w.l.recoverMigration(saved)
			if err != nil || got.Profile != "claude-work" {
				t.Fatalf("revive = %+v, %v; want it on claude-work", got, err)
			}
			if !strings.Contains(got.Handoff, "Reason: "+exhaustedReason) {
				t.Fatalf("revive note = %q", got.Handoff)
			}
		})
	}
}

func TestASessionThatGoesBusyBeforeItsStopIsNotInterrupted(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			w := newQueueWorld(t, harness, "")
			w.session.Status = core.Idle
			list := w.l.core.Procs
			w.l.core.Procs = func() ([]proc.Proc, error) {
				if _, err := os.Stat(w.authd); err == nil {
					w.session.Status = core.Busy
				}
				return list()
			}
			out, err := w.l.QueueProfileSwitch(w.rec.Key, "claude-work", exhaustedReason)
			if err != nil || !out.Queued || *w.signals != 0 {
				t.Fatalf("switch = %+v, %v, signals %d; want it queued without a signal", out, err, *w.signals)
			}
			if m := w.migration(t); m.State != "queued" || m.Reason != exhaustedReason {
				t.Fatalf("record = %+v; want queued with its reason", m)
			}
		})
	}
}

func TestABlockerAfterTheStopShowsWithoutALiveRow(t *testing.T) {
	w := newQueueWorld(t, "claude", "")
	w.session.Status = core.Idle
	if err := os.Remove(filepath.Join(w.def.Root, w.rel)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.l.QueueProfileSwitch(w.rec.Key, "claude-work", exhaustedReason); err == nil {
		t.Fatal("handoff without a transcript succeeded")
	}
	if *w.signals == 0 {
		t.Fatal("the fixture's failure should come after the stop")
	}
	if got := w.row(t); !strings.Contains(got, "switch -> claude-work blocked: handoff failed: source transcript is unavailable") {
		t.Fatalf("row = %q, want the blocker on the launch row", got)
	}
	w.serve(t)
	if m := w.migration(t); m.Version == 0 || m.State == "queued" {
		t.Fatalf("a stopped handoff must stay recoverable: %+v", m)
	}
}

func TestAFailoverNoteReachesTheSessionOnce(t *testing.T) {
	w := newQueueWorld(t, "claude", "")
	w.session.Status = core.Idle
	note := "The prior turn stopped because the claude subscription exhausted its usage window."
	next := w.l
	next.resumeNote = note
	if _, err := next.QueueProfileSwitch(w.rec.Key, "claude-work", exhaustedReason); err != nil {
		t.Fatal(err)
	}
	if args := w.applied(t); strings.Count(args, note) != 1 {
		t.Fatalf("resume note = %q; want it once", args)
	}
}

func TestReviveAppliesAQueuedSwitchToTheCurrentRecord(t *testing.T) {
	w := newQueueWorld(t, "claude", "")
	w.queue(t)
	*w.alive = false
	saved, _ := w.l.records.Find(w.rec.Key)
	saved.Model = "newer-model"
	if err := w.l.records.Save(saved); err != nil {
		t.Fatal(err)
	}
	got, err := w.l.recoverMigration(saved)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "newer-model" || got.Profile != "claude-work" {
		t.Fatalf("recovery wrote the queue-time record: %+v", got)
	}
	if on, _ := w.l.records.Find(w.rec.Key); on.Model != "newer-model" || on.Profile != "claude-work" {
		t.Fatalf("saved record = %+v", on)
	}
}

// during runs do at the first process listing after this call, standing for another ym process working mid-pass.
func (w queueWorld) during(do func()) {
	list := w.l.core.Procs
	fired := false
	w.l.core.Procs = func() ([]proc.Proc, error) {
		if !fired {
			fired = true
			do()
		}
		return list()
	}
}

func TestAServePassNeverOverridesANewerSwitchRequest(t *testing.T) {
	for _, c := range []struct {
		to, row string
	}{
		{to: "default", row: "default"},
		{to: "personal", row: "switch pending -> personal"},
	} {
		t.Run(c.to, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			w.queue(t)
			w.session.Status = core.Idle
			b, err := w.l.core.Board()
			if err != nil {
				t.Fatal(err)
			}
			w.during(func() {
				w.session.Status = core.Busy
				if _, err := w.l.QueueProfileSwitch(w.rec.Key, c.to, "changed my mind"); err != nil {
					t.Fatal(err)
				}
				w.session.Status = core.Idle
			})
			if err := w.l.RecoverProfiles(b); err != nil {
				t.Fatal(err)
			}
			saved, _ := w.l.records.Find(w.rec.Key)
			if *w.signals != 0 || saved.Profile != "default" {
				t.Fatalf("serve overrode the request: signals %d, profile %s", *w.signals, saved.Profile)
			}
			w.session.Status = core.Busy
			if got := w.row(t); got != c.row {
				t.Fatalf("row = %q, want %q", got, c.row)
			}
		})
	}
}

func TestAFailoverRecordFollowsItsSwitch(t *testing.T) {
	queued := failoverAttempt{Event: "e", State: core.FailoverState{Session: profileConversation, Profile: "default", Status: "queued", Target: "claude-work", Reason: "switch to claude-work waits for the turn end"}}
	blocked := queued
	blocked.State.Status, blocked.State.Reason = "blocked", "external/shared Codex lifecycle pid 5555"
	for _, c := range []struct {
		name     string
		start    failoverAttempt
		act      func(t *testing.T, w queueWorld)
		want     string
		wantDest string
	}{
		{name: "cancelled", start: queued, want: "closed", act: func(t *testing.T, w queueWorld) {
			if _, err := w.l.QueueProfileSwitch(w.rec.Key, "default", "cancel"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "killed", start: queued, want: "closed", act: func(t *testing.T, w queueWorld) {
			*w.alive = false
			saved, _ := w.l.records.Find(w.rec.Key)
			saved.Killed = time.Now()
			if err := w.l.records.Save(saved); err != nil {
				t.Fatal(err)
			}
			w.serve(t)
		}},
		{name: "landed", start: blocked, want: "closed", act: func(t *testing.T, w queueWorld) {
			*w.alive = false
			saved, _ := w.l.records.Find(w.rec.Key)
			saved.Landed = time.Now()
			if err := w.l.records.Save(saved); err != nil {
				t.Fatal(err)
			}
			w.serve(t)
		}},
		{name: "queued then applied", start: queued, want: "switched", wantDest: "claude-work", act: func(t *testing.T, w queueWorld) {
			w.session.Status = core.Idle
			w.serve(t)
		}},
		{name: "blocked then applied", start: blocked, want: "switched", wantDest: "claude-work", act: func(t *testing.T, w queueWorld) {
			w.session.Status = core.Idle
			w.serve(t)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			w.queue(t)
			if err := saveFailover(w.state, "claude", c.start); err != nil {
				t.Fatal(err)
			}
			c.act(t, w)
			a, err := loadFailover(w.state, "claude", profileConversation)
			if err != nil || a.State.Status != c.want {
				t.Fatalf("failover = %+v, %v; want %s", a.State, err, c.want)
			}
			if c.wantDest != "" && a.State.Profile != c.wantDest {
				t.Fatalf("failover profile = %q, want %q", a.State.Profile, c.wantDest)
			}
		})
	}
}

func TestAWaitOtherThanAQuotaStopKeepsTheSwitchQueuedWithoutWork(t *testing.T) {
	for _, blocked := range []string{"", "destination login expired"} {
		t.Run("blocked "+blocked, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			w.queue(t)
			w.session.Status = core.Waiting
			m := w.migration(t)
			m.Blocked = blocked
			if err := saveMigration(w.state, m); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(migrationPath(w.state, "claude", profileConversation))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(w.authd); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				w.serve(t)
			}
			if _, err := os.Stat(w.authd); !os.IsNotExist(err) {
				t.Fatal("a tick ran native authentication")
			}
			after, err := os.ReadFile(migrationPath(w.state, "claude", profileConversation))
			if err != nil || string(after) != string(before) || *w.signals != 0 {
				t.Fatalf("ticks changed the record or stopped the session: %s, %v, signals %d", after, err, *w.signals)
			}
		})
	}
}

func TestAKillNeverLetsServeResumeAHandoff(t *testing.T) {
	for _, state := range []string{"prepared", "published", "selected", "recorded"} {
		for _, c := range []struct {
			name         string
			since        time.Duration
			wantRecord   bool
			wantBlockers bool
		}{
			{name: "killed after the record began", since: -time.Minute, wantRecord: state != "prepared"},
			{name: "stamp from the switch's own stop", since: time.Minute, wantRecord: true},
		} {
			t.Run(state+"/"+c.name, func(t *testing.T) {
				w := newQueueWorld(t, "claude", "")
				*w.alive = false
				m := migration{Version: 1, Reason: "r", Source: w.rec, Destination: w.work, State: state, Since: time.Now().Add(c.since)}
				if err := saveMigration(w.state, m); err != nil {
					t.Fatal(err)
				}
				if err := w.l.SessionStopped(w.rec.Key); err != nil {
					t.Fatal(err)
				}
				w.serve(t)
				if got := w.migration(t).Version != 0; got != c.wantRecord {
					t.Fatalf("record kept = %v, want %v", got, c.wantRecord)
				}
				if c.since < 0 {
					if _, err := os.Stat(w.ran); !os.IsNotExist(err) {
						t.Fatal("serve resumed a session the owner killed")
					}
					return
				}
				saved, _ := w.l.records.Find(w.rec.Key)
				if saved.Profile != "claude-work" {
					t.Fatalf("the switch's own stop blocked its handoff: %+v", w.migration(t))
				}
			})
		}
	}
}

func TestAKillStampOnlyClosesASwitchItsSessionEnded(t *testing.T) {
	for _, c := range []struct {
		name        string
		stamp       time.Duration
		alive       bool
		wantRecord  bool
		wantResumed bool
	}{
		{name: "old stamp on a running session", stamp: -time.Hour, alive: true, wantRecord: true},
		{name: "new stamp on a session still running", stamp: time.Hour, alive: true, wantRecord: true},
		{name: "new stamp on an ended session", stamp: time.Hour},
		{name: "old stamp on an exited session", stamp: -time.Hour, wantRecord: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			w.queue(t)
			saved, _ := w.l.records.Find(w.rec.Key)
			saved.Killed = time.Now().Add(c.stamp)
			if err := w.l.records.Save(saved); err != nil {
				t.Fatal(err)
			}
			*w.alive = c.alive
			w.serve(t)
			if got := w.migration(t).Version != 0; got != c.wantRecord {
				t.Fatalf("record kept = %v, want %v", got, c.wantRecord)
			}
			if _, err := os.Stat(w.ran); !os.IsNotExist(err) {
				t.Fatal("serve resumed the session")
			}
		})
	}
}

func TestALockCollisionDoesNotStickTheNextTick(t *testing.T) {
	for _, c := range []struct {
		name    string
		blocker string
	}{{name: "no earlier blocker"}, {name: "keeps a real blocker", blocker: "destination login expired"}} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			w.queue(t)
			w.session.Status = core.Idle
			m := w.migration(t)
			m.Blocked = c.blocker
			if err := saveMigration(w.state, m); err != nil {
				t.Fatal(err)
			}
			release, err := profile.Operation(w.state, "claude", profileConversation)
			if err != nil {
				t.Fatal(err)
			}
			w.serve(t)
			if got := w.migration(t).Blocked; got != c.blocker {
				t.Fatalf("collision saved as blocker %q", got)
			}
			release()
			w.serve(t)
			w.applied(t)
		})
	}
}

func TestNoteBookkeepingKeepsASwitchQueuedMeanwhile(t *testing.T) {
	w := newQueueWorld(t, "claude", "")
	w.session.Status = core.Idle
	if _, err := w.l.QueueProfileSwitch(w.rec.Key, "claude-work", "first"); err != nil {
		t.Fatal(err)
	}
	*w.alive = true
	w.session.Profile, w.session.ConfigRoot, w.session.Account, w.session.Status = "claude-work", w.work.Root, w.work.Account, core.Busy
	running := w.migration(t)
	if out, err := w.l.QueueProfileSwitch(w.rec.Key, "personal", "second"); err != nil || !out.Queued {
		t.Fatalf("second switch = %+v, %v", out, err)
	}
	if err := w.l.settleNote(running); err != nil {
		t.Fatal(err)
	}
	if m := w.migration(t); m.State != "queued" || m.Destination.ID != "personal" {
		t.Fatalf("stale note bookkeeping overwrote the queued switch: %+v", m)
	}
}

func TestReplacingAPendingFailoverNamesTheNewLogin(t *testing.T) {
	for _, c := range []struct{ status, reason string }{
		{"queued", "switch to claude-work waits for the turn end"},
		{"blocked", "external/shared Codex lifecycle pid 5555"},
	} {
		t.Run(c.status, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			w.queue(t)
			if err := saveFailover(w.state, "claude", failoverAttempt{Event: "e", State: core.FailoverState{Session: profileConversation, Status: c.status, Target: "claude-work", Reason: c.reason}}); err != nil {
				t.Fatal(err)
			}
			if _, err := w.l.QueueProfileSwitch(w.rec.Key, "personal", "changed my mind"); err != nil {
				t.Fatal(err)
			}
			a, err := loadFailover(w.state, "claude", profileConversation)
			if err != nil || a.State.Target != "personal" || strings.Contains(a.State.Reason, c.reason) || !strings.Contains(a.State.Reason, "switch to personal") {
				t.Fatalf("failover = %+v, %v", a.State, err)
			}
		})
	}
}

func TestReviveSettlesAQueuedFailover(t *testing.T) {
	w := newQueueWorld(t, "claude", "")
	w.queue(t)
	if err := saveFailover(w.state, "claude", failoverAttempt{Event: "e", State: core.FailoverState{Session: profileConversation, Profile: "default", Status: "queued", Target: "claude-work"}}); err != nil {
		t.Fatal(err)
	}
	*w.alive = false
	w.serve(t)
	saved, _ := w.l.records.Find(w.rec.Key)
	if _, err := w.l.revive(saved); err != nil {
		t.Fatal(err)
	}
	if got := resumed(t, w.ran); !strings.Contains(got, "Reason: "+exhaustedReason) {
		t.Fatalf("revive note = %q", got)
	}
	*w.alive = true
	w.session.Profile, w.session.ConfigRoot, w.session.Account = "claude-work", w.work.Root, w.work.Account
	w.serve(t)
	a, err := loadFailover(w.state, "claude", profileConversation)
	if err != nil || a.State.Status != "switched" || w.migration(t).State != "running" {
		t.Fatalf("after revive: failover %+v, %v, migration %+v", a.State, err, w.migration(t))
	}
}

func TestAHandoffNoteClearsOnlyOnceThisSwitchsNoteReachedTheTranscript(t *testing.T) {
	prior := `{"type":"user","message":{"content":"ym switched this conversation from profile default to profile claude-work. Reason: long ago."}}` + "\n"
	for _, c := range []struct {
		name    string
		carried string
		after   string
		cleared bool
	}{
		{name: "it is the latest prompt", after: "NOTE\n", cleared: true},
		{name: "another prompt came after it", after: "NOTE\nlater\n", cleared: true},
		{name: "not delivered yet"},
		{name: "only an older switch's note", carried: prior},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", c.carried)
			w.session.Status = core.Idle
			if _, err := w.l.QueueProfileSwitch(w.rec.Key, "claude-work", exhaustedReason); err != nil {
				t.Fatal(err)
			}
			saved, _ := w.l.records.Find(w.rec.Key)
			if saved.Handoff == "" {
				t.Fatal("no note on the record")
			}
			for _, line := range strings.Split(strings.TrimSpace(c.after), "\n") {
				if line == "NOTE" {
					line = saved.Handoff
				}
				if line != "" {
					appendProfileFile(t, filepath.Join(w.work.Root, w.rel), `{"type":"user","message":{"content":"`+line+`"}}`+"\n")
				}
			}
			w.serve(t)
			after, _ := w.l.records.Find(w.rec.Key)
			if (after.Handoff == "") != c.cleared || w.migration(t).NoteRead != c.cleared {
				t.Fatalf("handoff = %q, note read %v, want cleared %v", after.Handoff, w.migration(t).NoteRead, c.cleared)
			}
		})
	}
}

func TestARemoteDormantRowKeepsItsPendingSwitch(t *testing.T) {
	r := remoteBoard("h", boardJSON{Dormant: []dormantJSON{{Key: "k", Harness: "claude", Session: "s", Switching: &core.Switching{To: "work", Blocked: "x"}}}})
	if len(r.Dormant) != 1 || r.Dormant[0].Switching == nil || r.Dormant[0].Switching.Blocked != "x" {
		t.Fatalf("remote dormant row lost its pending switch: %+v", r.Dormant)
	}
}
