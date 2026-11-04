package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/pressure"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func TestExhaustedHostLoginFailsOverToUsableLoginAndNeverResumesIntoTheWall(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, c := range []struct {
		name         string
		attached     time.Duration
		seen         time.Duration
		priorBlocked bool
		workUsed     float64
		restart      bool
		want         string
		status       string
	}{
		{name: "quota stopped", attached: -time.Hour, seen: -time.Minute, workUsed: 5, want: "claude-work", status: "switched"},
		{name: "resumed into exhausted login", attached: -11 * time.Hour, seen: -25 * time.Hour, workUsed: 5, want: "claude-work", status: "switched"},
		{name: "blocked before claude-work was usable", attached: -11 * time.Hour, seen: -25 * time.Hour, priorBlocked: true, workUsed: 5, want: "claude-work", status: "switched"},
		{name: "no usable login", attached: -time.Hour, seen: -time.Minute, workUsed: 100, want: "default", status: "blocked"},
		{name: "host restart revive", attached: -11 * time.Hour, seen: -25 * time.Hour, workUsed: 5, restart: true, want: "claude-work"},
		{name: "host restart revive of a session blocked with no usable login", attached: -11 * time.Hour, seen: -25 * time.Hour, priorBlocked: true, workUsed: 5, restart: true, want: "claude-work", status: "switched"},
	} {
		t.Run(c.name, func(t *testing.T) {
			now := time.Now()
			reset := now.Add(31 * time.Hour).Truncate(time.Second)
			state, cwd, bins := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("YAMNAYA_STATE_DIR", state)
			t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
			for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"} {
				t.Setenv(k, "")
			}
			ran := filepath.Join(t.TempDir(), "resumed")
			script := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in *\" auth status \"*) printf '%%s\\n' '{\"loggedIn\":true,\"authMethod\":\"claude.ai\"}'; exit 0;; esac\nprintf '%%s\\n' \"$YAMNAYA_PROFILE\" \"$*\" > %s.tmp && mv %s.tmp %s\nexec sleep 30\n", shellQuote(ran), shellQuote(ran), shellQuote(ran))
			if err := os.WriteFile(filepath.Join(bins, "claude"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			home := profile.Profile{Harness: "claude", ID: "default", Label: "default", Root: physicalProfileRoot(t.TempDir()), Explicit: true}
			defaults, _ := json.Marshal([]profile.Profile{home})
			putProfileFile(t, filepath.Join(state, "profile-defaults.json"), string(defaults))
			work := profile.Profile{Harness: "claude", ID: "claude-work", Root: t.TempDir(), Explicit: true}
			if err := profile.Add(state, work); err != nil {
				t.Fatal(err)
			}
			work.Root = physicalProfileRoot(work.Root)
			subscriptionIdentity(t, &home)
			subscriptionIdentity(t, &work)
			putProfileFile(t, filepath.Join(home.Root, "projects", "p", profileConversation+".jsonl"), profileResponse("claude", now.Add(c.seen).UTC().Format(time.RFC3339Nano), "first", 100, 20))

			st, err := core.OpenStore(filepath.Join(state, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			db, err := sql.Open("sqlite", filepath.Join(state, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			setLimits := func(workUsed float64) {
				t.Helper()
				if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ym_usage_limits(body TEXT); DELETE FROM ym_usage_limits`); err != nil {
					t.Fatal(err)
				}
				for _, w := range []core.Limit{
					{Harness: "claude", Profile: "default", Account: home.Account, Name: "7d", Used: 100, Seen: now, ResetsAt: reset},
					{Harness: "claude", Profile: work.ID, Account: work.Account, Name: "7d", Used: workUsed, Seen: time.Now(), ResetsAt: now.Add(7 * 24 * time.Hour)},
				} {
					body, _ := json.Marshal(w)
					if _, err := db.Exec(`INSERT INTO ym_usage_limits VALUES(?)`, string(body)); err != nil {
						t.Fatal(err)
					}
				}
			}
			setLimits(c.workUsed)

			alive := !c.restart
			l := testLauncher(t, state)
			s := core.Session{Harness: "claude", ID: profileConversation, Profile: "default", ConfigRoot: home.Root, Account: home.Account, Status: core.Idle, Cwd: cwd}
			l.core = &core.Core{Store: st, StateDir: state, Harnesses: []core.Harness{switchingHarness{session: s}}, Hosts: []core.Host{quotaHost{}},
				Procs: func() ([]proc.Proc, error) {
					if !alive {
						return nil, nil
					}
					return []proc.Proc{{PID: 4242, TTY: "/dev/ttys999", Args: []string{"claude", "--resume", profileConversation}}}, nil
				},
				Signal: func(int, syscall.Signal) error { alive = false; return nil },
			}
			l.rootOf = func(_ int, key string) string {
				if key == "YAMNAYA_LAUNCH_KEY" {
					return "agent"
				}
				return ""
			}
			rec := launch.Record{Key: "agent", Harness: "claude", Session: profileConversation, NativePID: 4242, Adopted: true, Worktree: launch.Worktree{Path: cwd}, AttachedAt: now.Add(c.attached)}
			setRecordProfile(&rec, home)
			if err = l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			hit := core.Exhaustion{Harness: "claude", Profile: "default", Account: home.Account, Session: profileConversation, Reason: "subscription_rate_limit", Event: "quota-" + profileConversation, Seen: now.Add(c.seen)}
			other := hit
			other.Session, other.Event = "33333333-3333-4333-8333-333333333333", "quota-other"
			if c.priorBlocked {
				prior := failoverAttempt{State: core.FailoverState{Session: profileConversation, Profile: "default", Account: home.Account, Status: "blocked", Reason: "no different verified usable subscription pool", Reset: reset}, Event: hit.Event}
				if err = saveFailover(state, "claude", prior); err != nil {
					t.Fatal(err)
				}
			}

			if c.restart {
				if _, _, err = l.Resume(profileConversation, workResumeTextFixture, ""); err != nil {
					t.Fatal(err)
				}
			} else {
				for range 3 {
					b, err := l.core.Board()
					if err != nil {
						t.Fatal(err)
					}
					b.At, b.Fleet = now, core.Fleet{At: now, Exhaustions: []core.Exhaustion{hit, other}, Limits: profileLimits(state)}
					if err = l.Failover(b); err != nil {
						t.Fatal(err)
					}
				}
			}

			saved, _ := l.records.Find(rec.Key)
			if saved.Profile != c.want {
				t.Fatalf("session runs on %q, want %q", saved.Profile, c.want)
			}
			if c.status != "" {
				a, err := loadFailover(state, "claude", profileConversation)
				if err != nil || a.State.Status != c.status {
					t.Fatalf("failover %+v %v, want %s", a.State, err, c.status)
				}
			}
			items, err := st.Items(core.ItemFilter{})
			if err != nil {
				t.Fatal(err)
			}
			wantItems := 1
			if len(items) != wantItems {
				t.Fatalf("owner items %d, want %d: %+v", len(items), wantItems, items)
			}
			if wantItems == 1 {
				it := items[0]
				if it.Recipient != core.Owner || !strings.Contains(it.Question, "default") || !strings.Contains(it.Question, reset.UTC().Format("Jan 2 15:04 UTC")) {
					t.Fatalf("owner item %+v", it)
				}
				switchOffered := strings.HasPrefix(it.Rec, "switch to claude-work")
				if switchOffered != (c.want == "claude-work") {
					t.Fatalf("owner item recommendation %q", it.Rec)
				}
			}

			if c.want == "claude-work" {
				if got := strings.Split(resumed(t, ran), "\n"); got[0] != "claude-work" || !strings.Contains(got[1], profileConversation) {
					t.Fatalf("resumed as %q", got)
				}
				if got := resumed(t, ran); c.restart && !strings.Contains(got, workResumeTextFixture) {
					t.Fatalf("the resume message did not reach the moved session: %q", got)
				}
				return
			}
			alive = false
			_, _, err = l.Resume(profileConversation, workResumeTextFixture, "")
			if err == nil || !strings.Contains(err.Error(), "out of its subscription limit") {
				t.Fatalf("resume onto exhausted login: %v", err)
			}
			if _, err := os.Stat(ran); !os.IsNotExist(err) {
				t.Fatal("ym resumed the session onto its exhausted login")
			}

			setLimits(5)
			alive = true
			b, err := l.core.Board()
			if err != nil {
				t.Fatal(err)
			}
			b.At, b.Fleet = now, core.Fleet{At: now, Exhaustions: []core.Exhaustion{hit}, Limits: profileLimits(state)}
			if err = l.Failover(b); err != nil {
				t.Fatal(err)
			}
			if got := strings.Split(resumed(t, ran), "\n"); got[0] != "claude-work" {
				t.Fatalf("blocked failover was not retried once claude-work had usage: %q", got)
			}
			if items, _ := st.Items(core.ItemFilter{}); len(items) != 1 {
				t.Fatalf("retry filed another owner item: %d", len(items))
			}
		})
	}
}

const workResumeTextFixture = "ym: this session stopped and ym resumed it."

func TestFailoverRecordsWhetherTheSwitchWasQueuedOrAppliedUntilTheSessionMoves(t *testing.T) {
	type world = queueWorld
	for _, c := range []struct {
		name    string
		harness string
		status  core.Status
		// external keeps a process the switch cannot quiesce, so applying is refused.
		external bool
		// ownerQueued is the login the owner queued a switch to before the quota event.
		ownerQueued string
		// ended marks the session landed or killed before its quota event is seen.
		ended func(*launch.Record)
		// busyLock holds the conversation's operation lock through the first tick.
		busyLock bool
		// to is the login a switched record names, claude-work when empty.
		to string
		// first is the record after the first ticks, and then what happens next, with the record it must end on.
		first   string
		then    func(*testing.T, world)
		end     string
		profile string
		resumed bool
	}{
		{name: "idle applies at once", harness: "claude", status: core.Idle, first: "switched", end: "switched", profile: "claude-work", resumed: true},
		{name: "mid-turn queues and applies at the turn end", harness: "claude", status: core.Busy, first: "queued",
			then: func(t *testing.T, w world) {
				w.session.Status = core.Idle
				w.serve(t)
			}, end: "switched", profile: "claude-work", resumed: true},
		{name: "mid-turn queues and applies on revive", harness: "claude", status: core.Busy, first: "queued",
			then: func(t *testing.T, w world) {
				*w.alive = false
				if _, err := w.l.Revive(w.rec.Key); err != nil {
					t.Fatal(err)
				}
				w.session.Profile, w.session.ConfigRoot, w.session.Account, w.session.Status = w.work.ID, w.work.Root, w.work.Account, core.Idle
				*w.alive = true
				w.serve(t)
			}, end: "switched", profile: "claude-work", resumed: true},
		{name: "owner cancels the queued switch", harness: "claude", status: core.Busy, first: "queued",
			then: func(t *testing.T, w world) {
				if _, err := w.l.QueueProfileSwitch(w.rec.Key, "default", "stay"); err != nil {
					t.Fatal(err)
				}
			}, end: "closed", profile: "default"},
		{name: "session killed while queued", harness: "claude", status: core.Busy, first: "queued",
			then: func(t *testing.T, w world) {
				*w.alive = false
				rec, _ := w.l.records.Find(w.rec.Key)
				rec.Killed = time.Now()
				if err := w.l.records.Save(rec); err != nil {
					t.Fatal(err)
				}
				w.serve(t)
			}, end: "closed", profile: "default"},
		{name: "refused apply stays queued", harness: "codex", status: core.Idle, external: true, first: "queued", end: "queued", profile: "default"},
		{name: "owner replaces the queued login", harness: "claude", status: core.Busy, first: "queued",
			then: func(t *testing.T, w world) {
				if out, err := w.l.QueueProfileSwitch(w.rec.Key, "personal", "the owner asked for it"); err != nil || !out.Queued {
					t.Fatalf("replace = %+v, %v", out, err)
				}
				w.session.Status = core.Idle
				w.serve(t)
			}, end: "switched", to: "personal", profile: "personal", resumed: true},
		{name: "a conversation another operation holds is tried again", harness: "claude", status: core.Idle, busyLock: true, first: "blocked",
			then: func(t *testing.T, w world) {}, end: "switched", profile: "claude-work", resumed: true},
		{name: "landed session stays ended", harness: "claude", status: core.Idle, ended: func(r *launch.Record) { r.Landed = time.Now() }, profile: "default"},
		{name: "killed session stays ended", harness: "claude", status: core.Idle, ended: func(r *launch.Record) { r.Killed = time.Now() }, profile: "default"},
		{name: "killed with an owner-queued switch closed in the same tick", harness: "claude", status: core.Busy, ownerQueued: "personal", ended: func(r *launch.Record) { r.Killed = time.Now() }, profile: "default"},
		{name: "landed with an owner-queued switch closed in the same tick", harness: "claude", status: core.Busy, ownerQueued: "personal", ended: func(r *launch.Record) { r.Landed = time.Now() }, profile: "default"},
		{name: "a switch the owner queued keeps its login", harness: "claude", status: core.Busy, ownerQueued: "personal",
			then: func(t *testing.T, w world) {
				w.session.Status = core.Idle
				w.serve(t)
			}, profile: "personal", resumed: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, c.harness, "")
			w.session.Status = c.status
			*w.external = c.external
			now := time.Now()
			reset := now.Add(31 * time.Hour).Truncate(time.Second)
			window, reason := "7d", "subscription_rate_limit"
			if c.harness == "codex" {
				window, reason = "codex 7d", "usage_limit_exceeded"
			}
			limits := []core.Limit{
				{Harness: c.harness, Profile: "default", Account: w.def.Account, Name: window, Used: 100, Seen: now, ResetsAt: reset},
				{Harness: c.harness, Profile: w.work.ID, Account: w.work.Account, Name: window, Used: 5, Seen: now, ResetsAt: now.Add(7 * 24 * time.Hour)},
			}
			db, err := sql.Open("sqlite", filepath.Join(w.state, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS ym_usage_limits(body TEXT)`); err != nil {
				t.Fatal(err)
			}
			for _, lim := range limits {
				body, _ := json.Marshal(lim)
				if _, err = db.Exec(`INSERT INTO ym_usage_limits VALUES(?)`, string(body)); err != nil {
					t.Fatal(err)
				}
			}
			hit := core.Exhaustion{Harness: c.harness, Profile: "default", Account: w.def.Account, Session: profileConversation, Reason: reason, Event: "quota-1", Seen: now.Add(-25 * time.Hour)}
			tick := func(e core.Exhaustion) {
				t.Helper()
				b, err := w.l.core.Board()
				if err != nil {
					t.Fatal(err)
				}
				b.At, b.Fleet = now, core.Fleet{At: now, Exhaustions: []core.Exhaustion{e}, Limits: limits}
				if err = w.l.Failover(b); err != nil {
					t.Fatal(err)
				}
			}
			status := func() core.FailoverState {
				t.Helper()
				a, err := loadFailover(w.state, c.harness, profileConversation)
				if err != nil {
					t.Fatal(err)
				}
				return a.State
			}
			if c.ownerQueued != "" {
				if out, err := w.l.QueueProfileSwitch(w.rec.Key, c.ownerQueued, "the owner asked for it"); err != nil || !out.Queued {
					t.Fatalf("owner queue = %+v, %v", out, err)
				}
			}
			if c.ended != nil {
				*w.alive = false
				rec, _ := w.l.records.Find(w.rec.Key)
				c.ended(&rec)
				if err := w.l.records.Save(rec); err != nil {
					t.Fatal(err)
				}
				w.serve(t)
				if m, err := loadMigration(w.state, c.harness, profileConversation); err != nil || m.Version != 0 {
					t.Fatalf("an ended session keeps its queued switch: %+v, %v", m, err)
				}
			}
			release := func() {}
			if c.busyLock {
				if release, err = profile.Operation(w.state, c.harness, profileConversation); err != nil {
					t.Fatal(err)
				}
			}
			tick(hit)
			release()
			if got := status(); got.Status != c.first {
				t.Fatalf("first outcome %+v, want %s", got, c.first)
			}
			if c.first == "queued" {
				m, err := loadMigration(w.state, c.harness, profileConversation)
				if err != nil || m.State != "queued" || m.Destination.ID != "claude-work" || !strings.Contains(m.Reason, "default") || !strings.Contains(m.Reason, reset.UTC().Format(time.RFC3339)) {
					t.Fatalf("queued migration %+v %v", m, err)
				}
				saved, _ := os.ReadFile(migrationPath(w.state, c.harness, profileConversation))
				next := hit
				next.Event = "quota-2"
				tick(hit)
				tick(next)
				if got := status(); got.Status != "queued" || got.Profile != "default" {
					t.Fatalf("later ticks changed a queued record: %+v", got)
				}
				if again, _ := os.ReadFile(migrationPath(w.state, c.harness, profileConversation)); string(again) != string(saved) {
					t.Fatal("later ticks switched a queued session again")
				}
				if *w.signals != 0 {
					t.Fatalf("a queued switch stopped the session: %d signals", *w.signals)
				}
				if _, err := os.Stat(w.ran); !os.IsNotExist(err) {
					t.Fatal("a queued switch resumed the session")
				}
			}
			if c.then != nil {
				c.then(t, w)
			}
			tick(hit)
			tick(hit)
			got := status()
			if got.Status != c.end {
				t.Fatalf("record ended %+v, want %s", got, c.end)
			}
			rec, _ := w.l.records.Find(w.rec.Key)
			if rec.Profile != c.profile {
				t.Fatalf("session on %q, want %q", rec.Profile, c.profile)
			}
			if c.end == "switched" && (got.Profile != c.profile || got.Target != c.profile && c.to != "") {
				t.Fatalf("switched record names %+v", got)
			}
			if c.resumed {
				if lines := strings.Split(resumed(t, w.ran), "\n"); lines[0] != c.profile {
					t.Fatalf("resumed as %q", lines)
				}
			} else if _, err := os.Stat(w.ran); !os.IsNotExist(err) {
				t.Fatal("the session resumed though the switch never applied")
			}
			want := 1
			if c.ended != nil {
				want = 0
			}
			items, err := w.l.core.Store.Items(core.ItemFilter{})
			if err != nil || len(items) != want {
				t.Fatalf("owner items %d, %v, want %d: nobody on the login needs one: %+v", len(items), err, want, items)
			}
		})
	}
}

func TestBlockedFailoverRowsSettleFromWhereTheirSessionsNowAre(t *testing.T) {
	noPool := "no different verified usable subscription pool"
	for _, c := range []struct {
		name string
		// reason and target are the blocked record's.
		reason, target string
		move           bool
		ended          func(*launch.Record)
		queued         bool
		want, profile  string
	}{
		{name: "moved by hand, still running", reason: noPool, move: true, want: "switched", profile: "claude-work"},
		{name: "moved by hand, then landed", reason: noPool, move: true, ended: func(r *launch.Record) { r.Landed, r.Killed = time.Now(), time.Now() }, want: "switched", profile: "claude-work"},
		{name: "landed on the exhausted login", reason: noPool, ended: func(r *launch.Record) { r.Landed, r.Killed = time.Now(), time.Now() }, want: "closed", profile: "default"},
		{name: "killed on the exhausted login", reason: noPool, ended: func(r *launch.Record) { r.Killed = time.Now() }, want: "closed", profile: "default"},
		{name: "killed after its chosen switch was refused", reason: "refused", target: "claude-work", ended: func(r *launch.Record) { r.Killed = time.Now() }, want: "closed", profile: "default"},
		{name: "still on the login with a switch queued", reason: noPool, queued: true, want: "blocked", profile: "default"},
		{name: "still on the login with nothing usable", reason: noPool, want: "blocked", profile: "default"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			*w.alive = false
			now := time.Now()
			reset := now.Add(31 * time.Hour).Truncate(time.Second)
			rec, _ := w.l.records.Find(w.rec.Key)
			if c.move {
				setRecordProfile(&rec, w.work)
			}
			if c.ended != nil {
				c.ended(&rec)
			}
			if err := w.l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			if c.queued {
				if err := saveMigration(w.state, migration{Version: 1, Source: rec, Destination: w.work, State: "queued", Since: now}); err != nil {
					t.Fatal(err)
				}
			}
			row := failoverAttempt{Event: "quota-1",
				State: core.FailoverState{Session: profileConversation, Profile: "default", Account: w.def.Account, Status: "blocked", Reason: c.reason, Target: c.target, Reset: reset}}
			if err := saveFailover(w.state, "claude", row); err != nil {
				t.Fatal(err)
			}
			limits := []core.Limit{{Harness: "claude", Profile: "default", Account: w.def.Account, Name: "7d", Used: 100, Seen: now, ResetsAt: reset}}
			if c.want == "blocked" && !c.queued {
				limits = append(limits, core.Limit{Harness: "claude", Profile: w.work.ID, Account: w.work.Account, Name: "7d", Used: 100, Seen: now, ResetsAt: reset},
					core.Limit{Harness: "claude", Profile: "personal", Account: "claude/fixture-user/personal", Name: "7d", Used: 100, Seen: now, ResetsAt: reset})
			}
			hit := core.Exhaustion{Harness: "claude", Profile: "default", Account: w.def.Account, Session: profileConversation, Reason: "subscription_rate_limit", Event: "quota-1", Seen: now.Add(-25 * time.Hour)}
			for range 2 {
				b, err := w.l.core.Board()
				if err != nil {
					t.Fatal(err)
				}
				b.At, b.Fleet = now, core.Fleet{At: now, Exhaustions: []core.Exhaustion{hit}, Limits: limits}
				if err = w.l.Failover(b); err != nil {
					t.Fatal(err)
				}
			}
			a, err := loadFailover(w.state, "claude", profileConversation)
			if err != nil || a.State.Status != c.want || a.State.Profile != c.profile {
				t.Fatalf("record %+v, %v; want %s on %s", a.State, err, c.want, c.profile)
			}
			if _, err := os.Stat(w.ran); !os.IsNotExist(err) {
				t.Fatal("settling a record resumed its session")
			}
		})
	}
}

// portWorld is queueWorld's session with every Claude login exhausted unless claudeLeft, and a usable Codex login, the port stubbed.
func portWorld(t *testing.T, claudeLeft bool) (queueWorld, func(core.Exhaustion), profile.Profile) {
	t.Helper()
	const ported = "01a1b2c3-0000-7000-8000-000000000042"
	w := newQueueWorld(t, "claude", "")
	dir := filepath.Dir(strings.SplitN(os.Getenv("PATH"), ":", 2)[0] + "/x")
	script, err := os.ReadFile(filepath.Join(dir, "claude"))
	if err != nil {
		t.Fatal(err)
	}
	putProfileFile(t, filepath.Join(dir, "codex"), string(script))
	if err = os.Chmod(filepath.Join(dir, "codex"), 0700); err != nil {
		t.Fatal(err)
	}
	cx := profile.Profile{Harness: "codex", ID: "codex-main", Root: t.TempDir(), Explicit: true}
	if err = profile.Add(w.state, cx); err != nil {
		t.Fatal(err)
	}
	cx.Root = physicalProfileRoot(cx.Root)
	subscriptionIdentity(t, &cx)
	now := time.Now()
	reset := now.Add(31 * time.Hour).Truncate(time.Second)
	limits := []core.Limit{{Harness: "claude", Profile: "default", Account: w.def.Account, Name: "7d", Used: 100, Seen: now, ResetsAt: reset}}
	if !claudeLeft {
		limits = append(limits,
			core.Limit{Harness: "claude", Profile: w.work.ID, Account: w.work.Account, Name: "7d", Used: 100, Seen: now, ResetsAt: reset},
			core.Limit{Harness: "claude", Profile: "personal", Account: "claude/fixture-user/personal", Name: "7d", Used: 100, Seen: now, ResetsAt: reset})
	}
	db, err := sql.Open("sqlite", filepath.Join(w.state, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS ym_usage_limits(body TEXT)`); err != nil {
		t.Fatal(err)
	}
	seen := map[string]time.Time{}
	for _, lim := range limits {
		if lim.Seen.After(seen[lim.Account]) {
			seen[lim.Account] = lim.Seen
		}
	}
	for _, lim := range limits {
		lim.Seen = seen[lim.Account]
		body, _ := json.Marshal(lim)
		if _, err = db.Exec(`INSERT INTO ym_usage_limits VALUES(?)`, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	seeded, _ := w.l.records.Find(w.rec.Key)
	seeded.Model = "claude-opus-5-5"
	if err = w.l.records.Save(seeded); err != nil {
		t.Fatal(err)
	}
	w.l.core.PreparePort = func(s core.Session, to, id string) (core.PortPlan, error) {
		return core.PortPlan{Target: core.Session{Harness: to, Profile: id, ConfigRoot: cx.Root, Cwd: s.Cwd}, Close: func() {}, Write: func() (string, error) { return ported, nil }}, nil
	}
	tick := func(e core.Exhaustion) {
		t.Helper()
		b, err := w.l.core.Board()
		if err != nil {
			t.Fatal(err)
		}
		b.At, b.Fleet = now, core.Fleet{At: now, Exhaustions: []core.Exhaustion{e}, Limits: limits}
		if err = w.l.RecoverProfiles(b); err != nil {
			t.Fatal(err)
		}
		if err = w.l.Failover(b); err != nil {
			t.Fatal(err)
		}
	}
	return w, tick, cx
}

func TestEveryClaudeLoginExhaustedFailsOverToCodexAtTheTurnEndAndKeepsItsLinks(t *testing.T) {
	const ported = "01a1b2c3-0000-7000-8000-000000000042"
	const parent, child = "aaaaaaaa-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"
	for _, c := range []struct {
		name        string
		claudeLeft  bool
		status      core.Status
		managed     bool
		wantHarness string
		queued      bool
	}{
		{name: "idle agent ports", status: core.Idle, wantHarness: "codex"},
		{name: "idle manager ports and stays its goal's manager", status: core.Idle, managed: true, wantHarness: "codex"},
		{name: "mid-turn session stays pending until its turn ends", status: core.Busy, wantHarness: "codex", queued: true},
		{name: "mid-turn manager stays pending until its turn ends", status: core.Busy, managed: true, wantHarness: "codex", queued: true},
		{name: "busy crew do not hold a session past its own turn end", status: core.Idle, wantHarness: "codex"},
		{name: "a usable claude login is chosen first", claudeLeft: true, status: core.Idle, wantHarness: "claude"},
		{name: "a usable claude login is chosen first mid-turn", claudeLeft: true, status: core.Busy, wantHarness: "claude", queued: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, tick, cx := portWorld(t, c.claudeLeft)
			st := w.l.core.Store
			w.session.Status = c.status
			if strings.Contains(c.name, "crew") {
				w.session.Crew = []core.Member{{ID: child, Name: "worker", Status: core.Busy}}
			}
			mgr := core.Manager{}
			var err error
			if c.managed {
				if mgr, err = st.AddManager(core.Manager{Role: core.RoleManager, Harness: "claude", Profile: "default", ConfigRoot: w.def.Root}); err != nil {
					t.Fatal(err)
				}
				mgr.Session = profileConversation
				if err = st.SaveManager(mgr); err != nil {
					t.Fatal(err)
				}
			}
			if err = st.Link(profileConversation, parent); err != nil {
				t.Fatal(err)
			}
			if err = st.Link(child, profileConversation); err != nil {
				t.Fatal(err)
			}
			pending, err := st.AddMessage(profileConversation, "instruction that waited", core.MsgPending)
			if err != nil {
				t.Fatal(err)
			}
			item, err := st.AddItem(core.Item{Kind: core.KindChore, Question: "an open ask", Session: profileConversation})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			hit := core.Exhaustion{Harness: "claude", Profile: "default", Account: w.def.Account, Session: profileConversation, Reason: "subscription_rate_limit", Event: "quota-1", Seen: now.Add(-25 * time.Hour)}
			state := func() core.FailoverState {
				t.Helper()
				a, err := loadFailover(w.state, "claude", profileConversation)
				if err != nil {
					t.Fatal(err)
				}
				return a.State
			}
			tick(hit)
			if c.queued {
				if got := state(); got.Status != "queued" {
					t.Fatalf("mid-turn failover %+v, want queued", got)
				}
				if *w.signals != 0 {
					t.Fatalf("a mid-turn session was stopped: %d signals", *w.signals)
				}
				if rec, _ := w.l.records.Find(w.rec.Key); rec.Session != profileConversation || rec.Harness != "claude" {
					t.Fatalf("a mid-turn session moved: %+v", rec)
				}
				row, want := w.row(t), "switch pending -> codex/codex-main"
				if c.claudeLeft {
					want = "switch pending -> claude-work"
				}
				if row != want {
					t.Fatalf("board row %q, want %q", row, want)
				}
				tick(hit)
				if got := state(); got.Status != "queued" {
					t.Fatalf("a later tick changed the queued record: %+v", got)
				}
				w.session.Status = core.Idle
				tick(hit)
			}
			if got := state(); got.Status != "switched" {
				t.Fatalf("failover %+v, want switched", got)
			}
			rec, ok := w.l.records.Find(w.rec.Key)
			if !ok || rec.Harness != c.wantHarness {
				t.Fatalf("record %+v, want harness %s", rec, c.wantHarness)
			}
			lines := strings.Split(resumed(t, w.ran), "\n")
			quotaItems, err := st.Items(core.ItemFilter{})
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range quotaItems {
				if strings.Contains(it.Question, "hit its subscription limit") && !slices.ContainsFunc(it.Options, func(o core.Option) bool { return o.Label == "move to codex/"+cx.ID }) {
					t.Fatalf("owner item does not list the codex login: %+v", it.Options)
				}
			}
			if c.wantHarness == "claude" {
				if rec.Session != profileConversation || lines[0] != "claude-work" {
					t.Fatalf("same-harness kept id %s, resumed %q", rec.Session, lines)
				}
				if got, _ := st.Continued(); len(got) != 0 {
					t.Fatalf("same-harness recorded a continuation: %v", got)
				}
				return
			}
			if rec.Session != ported || rec.Profile != cx.ID || rec.ConfigRoot != cx.Root || rec.Model != "" {
				t.Fatalf("record not on the port: %+v", rec)
			}
			if lines[0] != cx.ID || !strings.Contains(lines[1], ported) || !strings.Contains(lines[1], "exhausted its usage window") {
				t.Fatalf("resumed without the port or its note: %q", lines)
			}
			if !strings.Contains(lines[1], "prompts and replies only") || strings.Contains(lines[1], "recorded tool results") {
				t.Fatalf("the note misstates what the port carried: %q", lines[1])
			}
			if got := w.l.core.Resolve(profileConversation); got != ported {
				t.Fatalf("old id resolves to %q", got)
			}
			lin, err := st.Lineage()
			if err != nil {
				t.Fatal(err)
			}
			if lin.Parent[ported] != parent || lin.Parent[child] != ported {
				t.Fatalf("lineage %v", lin.Parent)
			}
			if got, err := st.Message(pending.Seq); err != nil || got.Session != ported {
				t.Fatalf("waiting message %+v, %v", got, err)
			}
			if got, err := st.Item(item.Key()); err != nil || got.Session != profileConversation {
				t.Fatalf("an item changes its filer: %+v, %v", got, err)
			}
			if c.managed {
				saved, err := st.Manager(mgr.ID)
				if err != nil || saved.Session != ported || saved.Harness != "codex" || saved.Profile != cx.ID || saved.ConfigRoot != cx.Root {
					t.Fatalf("manager %+v, %v", saved, err)
				}
				o, err := w.l.core.LoadOrg()
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{profileConversation, ported} {
					if m, ok := o.ManagerOf(id); !ok || m.ID != mgr.ID {
						t.Fatalf("session %s no longer belongs to manager %s: %+v", id, mgr.ID, m)
					}
				}
				stale := mgr
				stale.Nudges = 7
				if err = st.SaveManager(stale); err != nil {
					t.Fatal(err)
				}
				saved, err = st.Manager(mgr.ID)
				if err != nil || saved.Session != ported || saved.Harness != "codex" || saved.Nudges != 7 {
					t.Fatalf("a save from a stale copy undid the move: %+v, %v", saved, err)
				}
			}
			items, err := st.Items(core.ItemFilter{})
			if err != nil {
				t.Fatal(err)
			}
			quota := 0
			for _, it := range items {
				if strings.Contains(it.Question, "hit its subscription limit") {
					quota++
					offered := false
					for _, o := range it.Options {
						offered = offered || o.Label == "move to codex/"+cx.ID
					}
					if !offered {
						t.Fatalf("owner item does not list the codex login: %+v", it.Options)
					}
				}
			}
			if quota != 1 {
				t.Fatalf("owner items %d, want 1", quota)
			}
		})
	}
}

func TestRestartResumesAManagedSessionOnCodexWhenEveryClaudeLoginIsExhausted(t *testing.T) {
	const ported = "01a1b2c3-0000-7000-8000-000000000042"
	for _, c := range []struct {
		name  string
		left  bool
		want  string
		wantH string
	}{
		{name: "no claude login left", want: ported, wantH: "codex"},
		{name: "claude login left", left: true, want: profileConversation, wantH: "claude"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, _, _ := portWorld(t, c.left)
			*w.alive = false
			now, resumedAs, err := w.l.Resume(profileConversation, workResumeTextFixture, "")
			if err != nil || now != c.want || !resumedAs {
				t.Fatalf("resume = %q %v %v, want %s", now, resumedAs, err, c.want)
			}
			rec, _ := w.l.records.Find(w.rec.Key)
			if rec.Session != c.want || rec.Harness != c.wantH {
				t.Fatalf("record %+v", rec)
			}
			if got := resumed(t, w.ran); !strings.Contains(got, workResumeTextFixture) || !strings.Contains(got, c.want) {
				t.Fatalf("resumed without its message: %q", got)
			}
		})
	}
}

func TestAPortAppliesOnceWhateverStopsItPartWay(t *testing.T) {
	const ported = "01a1b2c3-0000-7000-8000-000000000042"
	for _, c := range []struct {
		name string
		// head is a port a past attempt already wrote; failFirst fails the first write.
		head       string
		failFirst  bool
		via        string
		wantWrites int
	}{
		{name: "revive applies a queued port", via: "revive", wantWrites: 1},
		{name: "revive finishes a port already written", head: ported, via: "revive", wantWrites: 0},
		{name: "a failed write is tried again", failFirst: true, via: "tick", wantWrites: 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, tick, cx := portWorld(t, false)
			writes := 0
			prepare := w.l.core.PreparePort
			w.l.core.PreparePort = func(s core.Session, to, id string) (core.PortPlan, error) {
				plan, err := prepare(s, to, id)
				write := plan.Write
				plan.Write = func() (string, error) {
					writes++
					if c.failFirst && writes == 1 {
						return "", fmt.Errorf("cs failed")
					}
					return write()
				}
				return plan, err
			}
			ps, err := profile.Load(w.state)
			if err != nil {
				t.Fatal(err)
			}
			dest, err := profile.Find(ps, "codex", cx.ID)
			if err != nil {
				t.Fatal(err)
			}
			hit := core.Exhaustion{Harness: "claude", Profile: "default", Account: w.def.Account, Session: profileConversation, Reason: "subscription_rate_limit", Event: "quota-1", Seen: time.Now().Add(-time.Hour)}
			if c.via == "revive" {
				*w.alive = false
				state := "queued"
				if c.head != "" {
					state = "prepared"
				}
				rec, _ := w.l.records.Find(w.rec.Key)
				if err = saveMigration(w.state, migration{Version: 1, ResumeNote: "note", Source: rec, Destination: dest, Head: c.head, State: state, Since: time.Now().Add(-time.Minute)}); err != nil {
					t.Fatal(err)
				}
				if _, err = w.l.Revive(w.rec.Key); err != nil {
					t.Fatal(err)
				}
			} else {
				w.session.Status = core.Idle
				tick(hit)
				if a, _ := loadFailover(w.state, "claude", profileConversation); a.State.Status != "blocked" {
					t.Fatalf("a failed port reads %+v, want blocked", a.State)
				}
				if got := w.l.core.Resolve(profileConversation); got != profileConversation {
					t.Fatalf("a failed port moved the links to %s", got)
				}
				*w.alive = false
				tick(hit)
			}
			rec, _ := w.l.records.Find(w.rec.Key)
			if rec.Session != ported || rec.Harness != "codex" {
				t.Fatalf("record %+v", rec)
			}
			if writes != c.wantWrites {
				t.Fatalf("%d writes, want %d", writes, c.wantWrites)
			}
			if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != cx.ID || !strings.Contains(got[1], ported) {
				t.Fatalf("resumed as %q", got)
			}
			tick(hit)
			if m, _ := loadMigration(w.state, "claude", profileConversation); m.State != "running" || m.Head != ported {
				t.Fatalf("migration %+v", m)
			}
		})
	}
}

// quotaLimits replaces the host's limit rows.
func quotaLimits(t *testing.T, state string, limits ...core.Limit) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(state, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS ym_usage_limits(body TEXT); DELETE FROM ym_usage_limits`); err != nil {
		t.Fatal(err)
	}
	seen := map[string]time.Time{}
	for _, lim := range limits {
		if lim.Seen.After(seen[lim.Account]) {
			seen[lim.Account] = lim.Seen
		}
	}
	for _, lim := range limits {
		lim.Seen = seen[lim.Account]
		body, _ := json.Marshal(lim)
		if _, err = db.Exec(`INSERT INTO ym_usage_limits VALUES(?)`, string(body)); err != nil {
			t.Fatal(err)
		}
	}
}

func limit(p profile.Profile, used float64, reset time.Time) core.Limit {
	name := "7d"
	if p.Harness == "codex" {
		name = "codex 7d"
	}
	return core.Limit{Harness: p.Harness, Profile: p.ID, Account: p.Account, Name: name, Used: used, Seen: time.Now(), ResetsAt: reset}
}

func quotaEvent(w queueWorld, seen time.Time) core.Exhaustion {
	return core.Exhaustion{Harness: "claude", Profile: "default", Account: w.def.Account, Session: profileConversation, Reason: "subscription_rate_limit", Event: "quota-1", Seen: seen}
}

func tickFailover(t *testing.T, w queueWorld, at time.Time, events ...core.Exhaustion) {
	t.Helper()
	b, err := w.l.core.Board()
	if err != nil {
		t.Fatal(err)
	}
	b.At, b.Fleet = at, core.Fleet{At: at, Exhaustions: events, Limits: profileLimits(w.state)}
	if err = w.l.RecoverProfiles(b); err != nil {
		t.Fatal(err)
	}
	if err = w.l.Failover(b); err != nil {
		t.Fatal(err)
	}
}

func personal(t *testing.T, w queueWorld) profile.Profile {
	t.Helper()
	ps, err := profile.Load(w.state)
	if err != nil {
		t.Fatal(err)
	}
	p, err := profile.Find(ps, "claude", "personal")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAFailureToApplyDoesNotUseUpTheLoginItTargeted(t *testing.T) {
	for _, c := range []struct {
		name string
		then func(*testing.T, queueWorld, core.Exhaustion, time.Time)
	}{
		{name: "failover retries once the check recovers", then: func(t *testing.T, w queueWorld, e core.Exhaustion, now time.Time) {
			tickFailover(t, w, now.Add(2*time.Minute), e)
		}},
		{name: "a resume does not call the login unusable", then: func(t *testing.T, w queueWorld, _ core.Exhaustion, _ time.Time) {
			*w.alive = false
			a, err := loadFailover(w.state, "claude", profileConversation)
			if err != nil {
				t.Fatal(err)
			}
			a.Failed[w.work.Account] = time.Now().Add(-time.Second)
			if err = saveFailover(w.state, "claude", a); err != nil {
				t.Fatal(err)
			}
			if _, _, err := w.l.Resume(profileConversation, "go on", ""); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			w.session.Status = core.Idle
			now := time.Now()
			reset := now.Add(31 * time.Hour)
			quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 5, reset), limit(personal(t, w), 100, reset))
			script := filepath.Join(filepath.Dir(strings.SplitN(os.Getenv("PATH"), ":", 2)[0]+"/x"), "claude")
			good, err := os.ReadFile(script)
			if err != nil {
				t.Fatal(err)
			}
			attempts := filepath.Join(t.TempDir(), "attempts")
			if err = os.WriteFile(script, []byte("#!/bin/sh\necho x >> "+shellQuote(attempts)+"\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			e := quotaEvent(w, now.Add(-time.Hour))
			tickFailover(t, w, now, e)
			a, err := loadFailover(w.state, "claude", profileConversation)
			if err != nil || a.State.Status != "blocked" {
				t.Fatalf("failed switch %+v, %v", a.State, err)
			}
			if !barredUntil(w.state, w.work, profileLimits(w.state), now).IsZero() {
				t.Fatal("a failed switch barred claude-work for everyone")
			}
			first, _ := os.ReadFile(attempts)
			if len(first) == 0 {
				t.Fatal("the failing check never ran")
			}
			tickFailover(t, w, now.Add(time.Second), e)
			if again, _ := os.ReadFile(attempts); len(again) != len(first) {
				t.Fatal("a failed switch was retried on the very next tick")
			}
			if err = os.WriteFile(script, good, 0700); err != nil {
				t.Fatal(err)
			}
			c.then(t, w, e, now)
			if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != "claude-work" {
				t.Fatalf("resumed as %q", got)
			}
		})
	}
}

func TestFailoverLeavesAloneASessionServeMustNotResume(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(*testing.T, queueWorld)
	}{
		{name: "exited", setup: func(t *testing.T, w queueWorld) { *w.alive = false }},
		{name: "ym off", setup: func(t *testing.T, w queueWorld) {
			if err := w.l.core.Store.SetOff(profileConversation, true); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "killed", setup: func(t *testing.T, w queueWorld) {
			if err := w.l.core.Store.RecordKill(profileConversation, time.Now()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "throttled", setup: func(t *testing.T, w queueWorld) {
			dir := t.TempDir()
			if err := (pressure.Throttle{Blanket: true}).Save(dir); err != nil {
				t.Fatal(err)
			}
			w.l.core.ThrottleDir = func() (string, error) { return dir, nil }
		}},
		{name: "paced behind another resume", setup: func(t *testing.T, w queueWorld) { w.l.core.ResumedNow() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			w.session.Status = core.Idle
			now := time.Now()
			reset := now.Add(31 * time.Hour)
			quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 5, reset))
			c.setup(t, w)
			tickFailover(t, w, now, quotaEvent(w, now.Add(-time.Hour)))
			if _, err := os.Stat(w.ran); !os.IsNotExist(err) {
				t.Fatal("failover resumed it")
			}
			if *w.signals != 0 {
				t.Fatalf("failover stopped it: %d signals", *w.signals)
			}
			if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "default" {
				t.Fatalf("failover moved it to %s", rec.Profile)
			}
		})
	}
}

func TestAQueuedSwitchNeverLandsOnALoginThatRanOutWhileItWaited(t *testing.T) {
	for _, c := range []struct {
		name        string
		owner       bool
		personalOut bool
		end         string
		profile     string
	}{
		{name: "failover moves on to another usable login", end: "switched", profile: "personal"},
		{name: "failover waits when none is left", personalOut: true, end: "queued", profile: "default"},
		{name: "an owner's switch waits for its login", owner: true, profile: "default"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			now := time.Now()
			reset := now.Add(31 * time.Hour)
			me := personal(t, w)
			quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 5, reset), limit(me, 5, reset))
			e := quotaEvent(w, now.Add(-time.Hour))
			if c.owner {
				if out, err := w.l.QueueProfileSwitch(w.rec.Key, "claude-work", "owner"); err != nil || !out.Queued {
					t.Fatalf("%+v, %v", out, err)
				}
			} else {
				tickFailover(t, w, now, e)
				if a, _ := loadFailover(w.state, "claude", profileConversation); a.State.Status != "queued" || a.State.Target != "claude-work" {
					t.Fatalf("not queued: %+v", a.State)
				}
			}
			used := 5.0
			if c.personalOut {
				used = 100
			}
			quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 100, reset), limit(me, used, reset))
			w.session.Status = core.Idle
			tickFailover(t, w, now.Add(time.Minute), e)
			rec, _ := w.l.records.Find(w.rec.Key)
			if rec.Profile != c.profile {
				t.Fatalf("session on %s, want %s", rec.Profile, c.profile)
			}
			if c.end != "" {
				if a, _ := loadFailover(w.state, "claude", profileConversation); a.State.Status != c.end {
					t.Fatalf("record %+v, want %s", a.State, c.end)
				}
			}
			if c.profile != "default" {
				return
			}
			m, err := loadMigration(w.state, "claude", profileConversation)
			if err != nil || m.State != "queued" || !strings.Contains(m.Blocked, "claude-work is out of its subscription limit") {
				t.Fatalf("board cannot say why it waits: %+v, %v", m, err)
			}
			if *w.signals != 0 {
				t.Fatalf("stopped for a switch that cannot land: %d signals", *w.signals)
			}
		})
	}
}

func TestAResumeWithNoConversationStartsOverOnAUsableLogin(t *testing.T) {
	for _, c := range []struct {
		name   string
		via    string
		used   float64
		want   string
		refuse bool
	}{
		{name: "transcript gone, claude-work usable", via: "resume", used: 5, want: "claude-work"},
		{name: "transcript gone, nothing usable", via: "resume", used: 100, refuse: true},
		{name: "no session yet, claude-work usable", via: "revive", used: 5, want: "claude-work"},
		{name: "no session yet, nothing usable", via: "revive", used: 100, refuse: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			*w.alive = false
			now := time.Now()
			reset := now.Add(31 * time.Hour)
			me := personal(t, w)
			quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, c.used, reset), limit(me, c.used, reset))
			if err := os.Remove(filepath.Join(w.def.Root, w.rel)); err != nil {
				t.Fatal(err)
			}
			rec, _ := w.l.records.Find(w.rec.Key)
			var err error
			var resumedAs bool
			if c.via == "resume" {
				_, resumedAs, err = w.l.Resume(profileConversation, "go on", "start over on this")
			} else {
				rec.Session, rec.Prompt = "", "start over on this"
				if err = w.l.records.Save(rec); err != nil {
					t.Fatal(err)
				}
				_, err = w.l.revive(rec)
			}
			if m, _ := loadMigration(w.state, "claude", profileConversation); m.Version != 0 {
				t.Fatalf("a start-over left a handoff behind: %+v", m)
			}
			items, _ := w.l.core.Store.Items(core.ItemFilter{})
			if len(items) != 1 {
				t.Fatalf("owner items %d, want 1", len(items))
			}
			if c.refuse {
				var wait *core.QuotaWait
				if !errors.As(err, &wait) {
					t.Fatalf("resume = %v, want a quota refusal", err)
				}
				quotaLimits(t, w.state, limit(w.def, 5, reset))
				if c.via == "resume" {
					if _, resumedAs, err = w.l.Resume(profileConversation, "go on", "start over on this"); err != nil || resumedAs {
						t.Fatalf("no start-over once the limit cleared: %v resumed=%v", err, resumedAs)
					}
					if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != "default" {
						t.Fatalf("started as %q", got)
					}
				}
				return
			}
			if err != nil || resumedAs {
				t.Fatalf("start-over = %v resumed=%v", err, resumedAs)
			}
			if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != c.want || !strings.Contains(got[1], "start over on this") {
				t.Fatalf("started as %q", got)
			}
		})
	}
}

func TestALoginAtItsLimitAlwaysHasItsOneItemWhateverSawIt(t *testing.T) {
	for _, c := range []struct {
		name string
		see  func(*testing.T, queueWorld, time.Time)
		// moved says the session leaves the login; a limit row an event outdated by minutes still counts.
		moved bool
	}{
		{name: "limit row with no event, live session stays put", see: func(t *testing.T, w queueWorld, now time.Time) {
			w.session.Status = core.Idle
			tickFailover(t, w, now)
		}},
		{name: "event minutes newer than the limit row", moved: true, see: func(t *testing.T, w queueWorld, now time.Time) {
			w.session.Status = core.Idle
			tickFailover(t, w, now, quotaEvent(w, now.Add(5*time.Minute)))
		}},
		{name: "limit row with no event, refused resume", see: func(t *testing.T, w queueWorld, _ time.Time) {
			*w.alive = false
			me := personal(t, w)
			quotaLimits(t, w.state, limit(w.def, 100, time.Now().Add(time.Hour)), limit(w.work, 100, time.Now().Add(time.Hour)), limit(me, 100, time.Now().Add(time.Hour)))
			var wait *core.QuotaWait
			if _, _, err := w.l.Resume(profileConversation, "go on", ""); !errors.As(err, &wait) {
				t.Fatalf("resume = %v", err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			now := time.Now()
			reset := now.Add(31 * time.Hour)
			quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 5, reset))
			c.see(t, w, now)
			c.see(t, w, now)
			items, err := w.l.core.Store.Items(core.ItemFilter{})
			if err != nil || len(items) != 1 {
				t.Fatalf("owner items %d, %v, want 1", len(items), err)
			}
			rec, _ := w.l.records.Find(w.rec.Key)
			if c.moved != (rec.Profile == "claude-work") {
				t.Fatalf("session on %s, moved=%v", rec.Profile, c.moved)
			}
		})
	}
}

func TestAStaleLimitRowDoesNotMoveASessionThatJustRecovered(t *testing.T) {
	w := newQueueWorld(t, "claude", "")
	w.session.Status = core.Idle
	now := time.Now()
	reset := now.Add(31 * time.Hour)
	quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 5, reset))
	recovered := quotaEvent(w, now.Add(time.Minute))
	recovered.Recovered = true
	tickFailover(t, w, now, recovered)
	if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "default" || *w.signals != 0 {
		t.Fatalf("moved a working session: %+v signals=%d", rec, *w.signals)
	}
}

func TestExhaustedUntilAndTargetChoiceAgreeOnWhatStopsALogin(t *testing.T) {
	now := time.Now()
	reset := now.Add(time.Hour)
	me := profile.Profile{Harness: "codex", ID: "a", Account: "codex/u/a"}
	for _, c := range []struct {
		name string
		w    core.Limit
		out  bool
	}{
		{"used up", core.Limit{Harness: "codex", Profile: "a", Account: me.Account, Name: "codex 7d", Used: 100, ResetsAt: reset}, true},
		{"has usage left", core.Limit{Harness: "codex", Profile: "a", Account: me.Account, Name: "codex 7d", Used: 99, ResetsAt: reset}, false},
		{"already reset", core.Limit{Harness: "codex", Profile: "a", Account: me.Account, Name: "codex 7d", Used: 100, ResetsAt: now.Add(-time.Second)}, false},
		{"another account", core.Limit{Harness: "codex", Profile: "a", Account: "codex/u/b", Name: "codex 7d", Used: 100, ResetsAt: reset}, false},
		{"blank account, this profile", core.Limit{Harness: "codex", Profile: "a", Name: "codex 7d", Used: 100, ResetsAt: reset}, true},
		{"blank account, another profile", core.Limit{Harness: "codex", Profile: "b", Name: "codex 7d", Used: 100, ResetsAt: reset}, false},
		{"another harness", core.Limit{Harness: "claude", Profile: "a", Account: me.Account, Name: "7d", Used: 100, ResetsAt: reset}, false},
		{"one model's window", core.Limit{Harness: "codex", Profile: "a", Account: me.Account, Name: "codex_bengalfox 7d", Used: 100, ResetsAt: reset}, false},
		{"claude model window", core.Limit{Harness: "codex", Profile: "a", Account: me.Account, Name: "7d", Used: 100, ResetsAt: reset}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := exhaustedUntil([]core.Limit{c.w}, "codex", "a", me.Account, now)
			if got.IsZero() == c.out {
				t.Fatalf("exhaustedUntil = %v, want out=%v", got, c.out)
			}
			target, err := failoverTarget(usableLogins(t.TempDir(), failoverAttempt{}, []profile.Profile{me}, []core.Limit{c.w}, now), core.Exhaustion{Harness: "codex", Account: "codex/u/other"})
			if (err != nil) != c.out || !c.out && target.ID != "a" {
				t.Fatalf("target = %+v, %v, want out=%v", target, err, c.out)
			}
		})
	}
}

func TestEveryWayBackOntoAUsedUpLoginGoesThroughTheWall(t *testing.T) {
	for _, c := range []struct {
		name string
		come func(queueWorld) error
	}{
		{name: "ym revive", come: func(w queueWorld) error { _, err := w.l.Revive(w.rec.Key); return err }},
		{name: "resume", come: func(w queueWorld) error {
			_, _, err := w.l.Resume(profileConversation, "go on", "")
			return err
		}},
		{name: "bring", come: func(w queueWorld) error {
			return w.l.Bring(core.Live{Session: core.Session{ID: profileConversation, Harness: "claude", Profile: "default", ConfigRoot: w.def.Root, Cwd: w.rec.Worktree.Path}}, "go on")
		}},
	} {
		for _, usable := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/usable_%v", c.name, usable), func(t *testing.T) {
				w := newQueueWorld(t, "claude", "")
				*w.alive = false
				reset := time.Now().Add(31 * time.Hour)
				used := 5.0
				if !usable {
					used = 100
				}
				quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, used, reset), limit(personal(t, w), used, reset))
				err := c.come(w)
				if usable {
					if err != nil {
						t.Fatal(err)
					}
					if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != "claude-work" {
						t.Fatalf("came back as %q", got)
					}
					return
				}
				var wait *core.QuotaWait
				if !errors.As(err, &wait) {
					t.Fatalf("came back: %v", err)
				}
				if _, err := os.Stat(w.ran); !os.IsNotExist(err) {
					t.Fatal("resumed onto the used-up login")
				}
			})
		}
	}
}

func TestTheOwnerCanOverrideALimitThatResetEarly(t *testing.T) {
	for _, c := range []struct {
		name string
		act  func(queueWorld) (string, error)
		want string
	}{
		{name: "revive anyway", act: func(w queueWorld) (string, error) {
			w.l.anyway = true
			return w.l.Revive(w.rec.Key)
		}, want: "default"},
		{name: "switch onto it says so", act: func(w queueWorld) (string, error) {
			out, err := w.l.QueueProfileSwitch(w.rec.Key, "claude-work", "the owner asked for it")
			return out.Message, err
		}, want: "claude-work"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			*w.alive = false
			reset := time.Now().Add(31 * time.Hour).Truncate(time.Second)
			quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 100, reset), limit(personal(t, w), 100, reset))
			msg, err := c.act(w)
			if err != nil {
				t.Fatal(err)
			}
			if c.want == "claude-work" && !strings.Contains(msg, "out of its subscription limit until "+reset.UTC().Format(time.RFC3339)) {
				t.Fatalf("switch onto a used-up login did not say so: %q", msg)
			}
			if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != c.want {
				t.Fatalf("resumed as %q", got)
			}
		})
	}
}

func TestARefusalNeverStopsASessionThatWasRunning(t *testing.T) {
	for _, c := range []struct {
		name string
		act  func(*testing.T, queueWorld, core.Live) error
	}{
		{name: "adopt", act: func(t *testing.T, w queueWorld, s core.Live) error {
			_, err := w.l.Adopt(s, func(string) {})
			return err
		}},
		{name: "chief workspace", act: func(t *testing.T, w queueWorld, s core.Live) error {
			chief, err := w.l.core.Store.AddManager(core.Manager{Role: core.RoleChief, Name: "platform", Harness: "claude", Profile: "default", ConfigRoot: w.def.Root})
			if err != nil {
				t.Fatal(err)
			}
			chief.Session = profileConversation
			if err = w.l.core.Store.SaveManager(chief); err != nil {
				t.Fatal(err)
			}
			b, err := w.l.core.Board()
			if err != nil {
				t.Fatal(err)
			}
			return w.l.ChiefWorkspaces(b)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			w.session.Status = core.Idle
			reset := time.Now().Add(31 * time.Hour)
			quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 100, reset), limit(personal(t, w), 100, reset))
			s, err := w.l.core.Find(profileConversation)
			if err != nil {
				t.Fatal(err)
			}
			err = c.act(t, w, s)
			var wait *core.QuotaWait
			if err != nil && !errors.As(err, &wait) {
				t.Fatal(err)
			}
			if *w.signals != 0 {
				t.Fatalf("stopped a session it then refused to resume: %d signals", *w.signals)
			}
		})
	}
}

func TestAQueuedSwitchOntoAUsedUpLoginIsSaidToBeOneWhenQueued(t *testing.T) {
	w := newQueueWorld(t, "claude", "")
	reset := time.Now().Add(31 * time.Hour).Truncate(time.Second)
	quotaLimits(t, w.state, limit(w.def, 5, reset), limit(w.work, 100, reset))
	out, err := w.l.QueueProfileSwitch(w.rec.Key, "claude-work", "the owner asked for it")
	if err != nil || !out.Queued || !strings.Contains(out.Message, "out of its subscription limit until "+reset.UTC().Format(time.RFC3339)) {
		t.Fatalf("%+v, %v", out, err)
	}
}

func TestReviveNeverLandsAQueuedSwitchOnALoginThatRanOut(t *testing.T) {
	for _, c := range []struct {
		name                      string
		defaultOut, workOut, rest bool
		want                      string
		refused                   bool
		stillQueued               bool
	}{
		{name: "login usable: applied", want: "claude-work"},
		{name: "login ran out, session's own usable: resumes there, still queued", workOut: true, want: "default", stillQueued: true},
		{name: "both ran out, another usable: moves there", defaultOut: true, workOut: true, want: "personal"},
		{name: "everything ran out: refused", defaultOut: true, workOut: true, rest: true, refused: true, stillQueued: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			reset := time.Now().Add(31 * time.Hour)
			used := func(out bool) float64 {
				if out {
					return 100
				}
				return 5
			}
			if out, err := w.l.QueueProfileSwitch(w.rec.Key, "claude-work", "the owner asked for it"); err != nil || !out.Queued {
				t.Fatalf("%+v, %v", out, err)
			}
			*w.alive = false
			quotaLimits(t, w.state, limit(w.def, used(c.defaultOut), reset), limit(w.work, used(c.workOut), reset), limit(personal(t, w), used(c.rest), reset))
			_, err := w.l.Revive(w.rec.Key)
			var wait *core.QuotaWait
			if c.refused != errors.As(err, &wait) || (!c.refused && err != nil) {
				t.Fatalf("revive = %v, refused=%v", err, c.refused)
			}
			m, _ := loadMigration(w.state, "claude", profileConversation)
			if c.stillQueued != (m.State == "queued") {
				t.Fatalf("migration %+v, want queued=%v", m, c.stillQueued)
			}
			if c.refused {
				if _, err := os.Stat(w.ran); !os.IsNotExist(err) {
					t.Fatal("resumed though every login ran out")
				}
				return
			}
			if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != c.want {
				t.Fatalf("resumed as %q, want %s", got, c.want)
			}
		})
	}
}

type stuckInspect struct {
	turnHarness
	problem error
}

func (h stuckInspect) InspectHandoff(core.Session) (time.Time, error) { return h.at, h.problem }

func TestARowSaysWhyASwitchCannotApplyToASessionStoppedAtADialog(t *testing.T) {
	for _, c := range []struct {
		name   string
		status core.Status
		prompt bool
		want   string
	}{
		{name: "a dialog it cannot prove over", status: core.Waiting, want: "switch -> claude-work blocked: the session waits at the quota dialog: cannot switch waiting session: child native turn is active or unknown"},
		{name: "a permission prompt is the session's own", status: core.Waiting, prompt: true, want: "switch pending -> claude-work"},
		{name: "mid-turn", status: core.Busy, want: "switch pending -> claude-work"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			if out, err := w.l.QueueProfileSwitch(w.rec.Key, "claude-work", "owner"); err != nil || !out.Queued {
				t.Fatalf("%+v, %v", out, err)
			}
			base := w.l.core.Harnesses[0].(turnHarness)
			w.l.core.Harnesses = []core.Harness{stuckInspect{base, errors.New("child native turn is active or unknown")}}
			w.session.Status = c.status
			if c.prompt {
				w.session.Prompt = &core.Prompt{Tool: "Bash"}
			}
			w.serve(t)
			if got := w.row(t); got != c.want {
				t.Fatalf("row %q, want %q", got, c.want)
			}
			if c.status == core.Waiting && !c.prompt {
				w.session.Status = core.Busy
				w.serve(t)
				if got := w.row(t); got != "switch pending -> claude-work" {
					t.Fatalf("a blocker outlived its dialog: %q", got)
				}
			}
			if *w.signals != 0 {
				t.Fatalf("stopped: %d signals", *w.signals)
			}
		})
	}
}

// neighbourHarness lists several sessions at once, each on its own pid.
type neighbourHarness struct {
	turnHarness
	others []core.Session
}

func (h neighbourHarness) Discover(ps []proc.Proc) []core.Session {
	out := h.turnHarness.Discover(ps)
	for i, s := range h.others {
		for _, p := range ps {
			if p.PID == 6000+i {
				s.PID, s.TTY = p.PID, p.TTY
				out = append(out, s)
			}
		}
	}
	return out
}

func TestALoginAtItsLimitMovesOnlyTheSessionThatHitIt(t *testing.T) {
	const neighbour = "33333333-3333-4333-8333-333333333333"
	for _, c := range []struct {
		name string
		// stopped is what happens to the session itself: nothing, a real quota stop, or a resume refused after it stopped.
		stopped, want string
	}{
		{name: "idle sessions all stay", stopped: "", want: "default"},
		{name: "a real quota stop moves that session only", stopped: "quota stop", want: "claude-work"},
		{name: "a resume onto the login moves that session only", stopped: "resume", want: "claude-work"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			w.session.Status = core.Idle
			other := *w.session
			other.ID = neighbour
			base := w.l.core.Harnesses[0].(turnHarness)
			w.l.core.Harnesses = []core.Harness{neighbourHarness{base, []core.Session{other}}}
			procs := w.l.core.Procs
			w.l.core.Procs = func() ([]proc.Proc, error) {
				ps, err := procs()
				return append(ps, proc.Proc{PID: 6000, TTY: "/dev/ttys998", Args: []string{"claude", "--resume", neighbour}}), err
			}
			next := w.rec
			next.Key, next.Session, next.NativePID = "neighbour", neighbour, 6000
			if err := w.l.records.Save(next); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			reset := now.Add(31 * time.Hour)
			quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 5, reset))
			var events []core.Exhaustion
			switch c.stopped {
			case "quota stop":
				events = append(events, quotaEvent(w, now))
			case "resume":
				*w.alive = false
			}
			if c.stopped == "resume" {
				if _, _, err := w.l.Resume(profileConversation, "go on", ""); err != nil {
					t.Fatal(err)
				}
			}
			for range 3 {
				tickFailover(t, w, now, events...)
			}
			if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != c.want {
				t.Fatalf("the session is on %s, want %s", rec.Profile, c.want)
			}
			if rec, _ := w.l.records.Find("neighbour"); rec.Profile != "default" {
				t.Fatalf("an idle neighbour moved to %s", rec.Profile)
			}
			if c.want == "default" && *w.signals != 0 {
				t.Fatalf("an idle session was stopped: %d signals", *w.signals)
			}
			items, err := w.l.core.Store.Items(core.ItemFilter{})
			if err != nil || len(items) != 1 {
				t.Fatalf("owner items %d, %v, want exactly 1", len(items), err)
			}
			if c.want != "default" {
				if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != c.want {
					t.Fatalf("resumed as %q", got)
				}
			}
		})
	}
}

func TestAPortFinishesFromWhicheverStepItStoppedAt(t *testing.T) {
	const ported = "01a1b2c3-0000-7000-8000-000000000042"
	for _, step := range []string{"written", "record saved", "links moved", "links failed"} {
		for _, via := range []string{"serve", "resume of the old id"} {
			t.Run(step+"/"+via, func(t *testing.T) {
				w, _, cx := portWorld(t, false)
				st := w.l.core.Store
				mgr, err := st.AddManager(core.Manager{Role: core.RoleManager, Harness: "claude", Profile: "default", ConfigRoot: w.def.Root})
				if err != nil {
					t.Fatal(err)
				}
				mgr.Session = profileConversation
				if err = st.SaveManager(mgr); err != nil {
					t.Fatal(err)
				}
				writes := 0
				w.l.core.PreparePort = func(core.Session, string, string) (core.PortPlan, error) {
					writes++
					return core.PortPlan{}, errors.New("a finished port was written again")
				}
				*w.alive = false
				ps, err := profile.Load(w.state)
				if err != nil {
					t.Fatal(err)
				}
				dest, err := profile.Find(ps, "codex", cx.ID)
				if err != nil {
					t.Fatal(err)
				}
				putProfileFile(t, filepath.Join(cx.Root, "sessions", "rollout-2026-10-23T10-00-00-"+ported+".jsonl"), profileMeta(ported, "", 0)+profileResponse("codex", time.Now().UTC().Format(time.RFC3339Nano), "first", 100, 20))
				source, _ := w.l.records.Find(w.rec.Key)
				m := migration{Version: 1, ResumeNote: "the note", Source: source, Destination: dest, Head: ported, State: "prepared", Since: time.Now()}
				if err = saveMigration(w.state, m); err != nil {
					t.Fatal(err)
				}
				if err = saveFailover(w.state, "claude", failoverAttempt{Event: "quota-1", State: core.FailoverState{Session: profileConversation, Profile: "default", Status: "blocked", Target: cx.ID}}); err != nil {
					t.Fatal(err)
				}
				if step != "written" {
					moved := source
					setRecordProfile(&moved, dest)
					moved.Harness, moved.Session, moved.Model, moved.Handoff = "codex", ported, "", "the note"
					if err = w.l.records.Save(moved); err != nil {
						t.Fatal(err)
					}
				}
				if step == "links moved" {
					if err = st.ContinueSession(profileConversation, ported, "codex", cx.ID, cx.Root); err != nil {
						t.Fatal(err)
					}
				}
				if step == "links failed" {
					db, err := sql.Open("sqlite", filepath.Join(w.state, "ym.db"))
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					if _, err = db.Exec(`CREATE TRIGGER fail_port BEFORE INSERT ON ym_continued BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
						t.Fatal(err)
					}
					if err = w.l.RecoverProfiles(core.Board{}); err == nil {
						t.Fatal("recovery reported nothing while the store refused the links")
					}
					if m, _ := loadMigration(w.state, "claude", profileConversation); m.State != "prepared" {
						t.Fatalf("a failed recovery called the port done: %s", m.State)
					}
					if _, err = db.Exec(`DROP TRIGGER fail_port`); err != nil {
						t.Fatal(err)
					}
				}
				if via == "serve" {
					for range 3 {
						tickFailover(t, w, time.Now())
					}
				} else {
					now, resumedAs, err := w.l.Resume(profileConversation, "go on", "")
					if err != nil || now != ported || !resumedAs {
						t.Fatalf("resume of the old id = %q %v %v", now, resumedAs, err)
					}
				}
				rec, _ := w.l.records.Find(w.rec.Key)
				if rec.Session != ported || rec.Harness != "codex" || rec.Profile != cx.ID {
					t.Fatalf("record %+v", rec)
				}
				if got := w.l.core.Resolve(profileConversation); got != ported {
					t.Fatalf("old id resolves to %s", got)
				}
				saved, err := st.Manager(mgr.ID)
				if err != nil || saved.Session != ported || saved.Harness != "codex" {
					t.Fatalf("manager %+v, %v", saved, err)
				}
				tickFailover(t, w, time.Now())
				if m, _ := loadMigration(w.state, "claude", profileConversation); m.State != "running" {
					t.Fatalf("migration stuck at %s", m.State)
				}
				if a, _ := loadFailover(w.state, "claude", profileConversation); a.State.Status != "switched" {
					t.Fatalf("failover %+v", a.State)
				}
				if writes != 0 {
					t.Fatalf("%d more ports written", writes)
				}
				if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != cx.ID || !strings.Contains(got[1], ported) || !strings.Contains(got[1], "the note") {
					t.Fatalf("resumed as %q", got)
				}
			})
		}
	}
}

func TestASwitchWaitsWhileYmIsOffOrThrottledForTheSession(t *testing.T) {
	for _, c := range []struct {
		name string
		hold func(*testing.T, queueWorld) func()
	}{
		{name: "ym off", hold: func(t *testing.T, w queueWorld) func() {
			if err := w.l.core.Store.SetOff(profileConversation, true); err != nil {
				t.Fatal(err)
			}
			return func() { w.l.core.Store.SetOff(profileConversation, false) }
		}},
		{name: "throttled", hold: func(t *testing.T, w queueWorld) func() {
			dir := t.TempDir()
			if err := (pressure.Throttle{Blanket: true}).Save(dir); err != nil {
				t.Fatal(err)
			}
			w.l.core.ThrottleDir = func() (string, error) { return dir, nil }
			return func() { w.l.core.ThrottleDir = nil }
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			if out, err := w.l.QueueProfileSwitch(w.rec.Key, "claude-work", "owner"); err != nil || !out.Queued {
				t.Fatalf("%+v, %v", out, err)
			}
			release := c.hold(t, w)
			w.session.Status = core.Idle
			w.serve(t)
			if *w.signals != 0 {
				t.Fatalf("stopped while held: %d signals", *w.signals)
			}
			if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "default" {
				t.Fatalf("moved while held to %s", rec.Profile)
			}
			release()
			w.serve(t)
			if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "claude-work" {
				t.Fatalf("the switch never applied once released: %s", rec.Profile)
			}
		})
	}
}

func TestARetriedPortKeepsItsNoteAndTheCrewItKilled(t *testing.T) {
	w, tick, _ := portWorld(t, false)
	w.session.Status = core.Idle
	w.session.Crew = []core.Member{{ID: "bbbbbbbb", Name: "crewC", Status: core.Busy}}
	prepare := w.l.core.PreparePort
	writes := 0
	w.l.core.PreparePort = func(s core.Session, to, id string) (core.PortPlan, error) {
		plan, err := prepare(s, to, id)
		write := plan.Write
		plan.Write = func() (string, error) {
			if writes++; writes == 1 {
				return "", errors.New("cs failed")
			}
			return write()
		}
		return plan, err
	}
	now := time.Now()
	e := quotaEvent(w, now.Add(-time.Hour))
	tick(e)
	if writes != 1 {
		t.Fatalf("%d writes", writes)
	}
	*w.alive = false
	tick(e)
	got := resumed(t, w.ran)
	for _, want := range []string{"exhausted its usage window", "crewC"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the retry lost %q: %q", want, got)
		}
	}
}

func TestANamedLoginWithNoConversationWaitsForItsResetThenStartsOver(t *testing.T) {
	w := newQueueWorld(t, "claude", "")
	*w.alive = false
	reset := time.Now().Add(31 * time.Hour)
	quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 100, reset), limit(personal(t, w), 100, reset))
	rec, _ := w.l.records.Find(w.rec.Key)
	setRecordProfile(&rec, w.work)
	if err := w.l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	var wait *core.QuotaWait
	if _, _, err := w.l.Resume(profileConversation, "go on", "start over on this"); !errors.As(err, &wait) {
		t.Fatalf("before the reset: %v, want a quota refusal the serve loop asks again", err)
	}
	quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 5, reset))
	if _, resumedAs, err := w.l.Resume(profileConversation, "go on", "start over on this"); err != nil || resumedAs {
		t.Fatalf("after the reset: %v resumed=%v", err, resumedAs)
	}
	if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != "claude-work" || !strings.Contains(got[1], "start over on this") {
		t.Fatalf("started as %q", got)
	}
}

func TestResumeStartsOverOnlyWhenTheConversationIsVerifiablyGone(t *testing.T) {
	const prompt = "start over on this"
	for _, c := range []struct {
		name   string
		named  bool
		break_ func(*testing.T, queueWorld)
		// gone is verified absence, which starts over; anything else leaves the conversation alone.
		gone bool
	}{
		{name: "default: transcript removed", break_: func(t *testing.T, w queueWorld) { os.Remove(filepath.Join(w.def.Root, w.rel)) }, gone: true},
		{name: "default: transcript unreadable", break_: func(t *testing.T, w queueWorld) {
			if err := os.Chmod(filepath.Join(w.def.Root, w.rel), 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(filepath.Join(w.def.Root, w.rel), 0600) })
		}},
		{name: "default: config root missing", break_: func(t *testing.T, w queueWorld) {
			if err := os.Rename(w.def.Root, w.def.Root+".aside"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Rename(w.def.Root+".aside", w.def.Root) })
		}},
		{name: "named: transcript removed", named: true, break_: func(t *testing.T, w queueWorld) { os.Remove(filepath.Join(w.work.Root, w.rel)) }, gone: true},
		{name: "named: transcript unreadable", named: true, break_: func(t *testing.T, w queueWorld) {
			if err := os.Chmod(filepath.Join(w.work.Root, w.rel), 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(filepath.Join(w.work.Root, w.rel), 0600) })
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			*w.alive = false
			if c.named {
				putProfileFile(t, filepath.Join(w.work.Root, w.rel), w.data)
				rec, _ := w.l.records.Find(w.rec.Key)
				setRecordProfile(&rec, w.work)
				if err := w.l.records.Save(rec); err != nil {
					t.Fatal(err)
				}
			}
			c.break_(t, w)
			now, resumedAs, err := w.l.Resume(profileConversation, "go on", prompt)
			if c.gone {
				if err != nil || resumedAs || now != profileConversation {
					t.Fatalf("start-over = %q %v %v", now, resumedAs, err)
				}
				if got := resumed(t, w.ran); !strings.Contains(got, prompt) {
					t.Fatalf("started as %q", got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "leaves it untouched") {
				t.Fatalf("resume = %v, want a visible failure", err)
			}
			time.Sleep(200 * time.Millisecond)
			if _, err := os.Stat(w.ran); !os.IsNotExist(err) {
				t.Fatal("started a session over a conversation it could not read")
			}
			matches, _ := filepath.Glob(filepath.Join(w.def.Root+"*", "projects", "*", "*.noturn"))
			more, _ := filepath.Glob(filepath.Join(w.work.Root, "projects", "*", "*.noturn"))
			if len(matches)+len(more) != 0 {
				t.Fatalf("the real transcript was renamed: %v %v", matches, more)
			}
		})
	}
}

func TestAWrittenPortNeverFallsThroughToAFreshStart(t *testing.T) {
	const ported = "01a1b2c3-0000-7000-8000-000000000042"
	w, _, cx := portWorld(t, false)
	*w.alive = false
	st := w.l.core.Store
	moved, _ := w.l.records.Find(w.rec.Key)
	setRecordProfile(&moved, cx)
	moved.Harness, moved.Session, moved.Model = "codex", ported, ""
	if err := w.l.records.Save(moved); err != nil {
		t.Fatal(err)
	}
	if err := st.ContinueSession(profileConversation, ported, "codex", cx.ID, cx.Root); err != nil {
		t.Fatal(err)
	}
	_, _, err := w.l.Resume(profileConversation, "go on", "start over on this")
	if err == nil || !strings.Contains(err.Error(), "will not start it over") {
		t.Fatalf("resume = %v", err)
	}
	if _, err := os.Stat(w.ran); !os.IsNotExist(err) {
		t.Fatal("started over in place of a written port")
	}
}

func TestAFailedTargetIsSkippedForTheNextUsableLogin(t *testing.T) {
	signedOut := func(t *testing.T, w queueWorld) {
		t.Helper()
		if err := os.Remove(filepath.Join(w.work.Root, ".claude.json")); err == nil {
			putProfileFile(t, filepath.Join(w.work.Root, ".claude.json"), `{"oauthAccount":{"accountUuid":"fixture-user","organizationUuid":"claude-work"}}`)
		}
	}
	for _, c := range []struct {
		name string
		via  string
	}{
		{name: "live quota stop", via: "failover"},
		{name: "resume", via: "resume"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			signedOut(t, w)
			now := time.Now()
			reset := now.Add(31 * time.Hour)
			quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 5, reset), limit(personal(t, w), 5, reset))
			dir := strings.SplitN(os.Getenv("PATH"), ":", 2)[0]
			good, err := os.ReadFile(filepath.Join(dir, "claude"))
			if err != nil {
				t.Fatal(err)
			}
			script := "#!/bin/sh\ncase \"$CLAUDE_CONFIG_DIR\" in " + shellQuote(w.work.Root) + ") case \" $* \" in *\" auth status \"*) exit 1;; esac;; esac\n" + strings.TrimPrefix(string(good), "#!/bin/sh\n")
			if err = os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			e := quotaEvent(w, now.Add(-time.Hour))
			if c.via == "failover" {
				w.session.Status = core.Idle
				tickFailover(t, w, now, e)
				if a, _ := loadFailover(w.state, "claude", profileConversation); a.State.Status != "blocked" {
					t.Fatalf("first pass %+v, want blocked on the signed-out login", a.State)
				}
				tickFailover(t, w, now.Add(time.Second), e)
			} else {
				*w.alive = false
				if _, _, err = w.l.Resume(profileConversation, "go on", ""); err != nil {
					t.Fatal(err)
				}
			}
			if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "personal" {
				t.Fatalf("session on %s, want personal", rec.Profile)
			}
			if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != "personal" {
				t.Fatalf("resumed as %q", got)
			}
		})
	}
}

func TestSweepLeavesASessionAPortStoppedSoTheRetryKeepsItsNote(t *testing.T) {
	const ported = "01a1b2c3-0000-7000-8000-000000000042"
	w, tick, _ := portWorld(t, false)
	w.session.Status = core.Idle
	w.session.Crew = []core.Member{{ID: "bbbbbbbb", Name: "crewC", Status: core.Busy}}
	prepare := w.l.core.PreparePort
	writes := 0
	w.l.core.PreparePort = func(s core.Session, to, id string) (core.PortPlan, error) {
		plan, err := prepare(s, to, id)
		write := plan.Write
		plan.Write = func() (string, error) {
			if writes++; writes == 1 {
				return "", errors.New("cs failed")
			}
			return write()
		}
		return plan, err
	}
	e := quotaEvent(w, time.Now().Add(-time.Hour))
	tick(e)
	if err := w.l.Sweep(map[string]string{w.rec.Key: "exited 1"}); err != nil {
		t.Fatal(err)
	}
	rec, _ := w.l.records.Find(w.rec.Key)
	if rec.IsLanded() {
		t.Fatal("sweep landed a session its own port had stopped")
	}
	tick(e)
	if got := resumed(t, w.ran); !strings.Contains(got, "crewC") || !strings.Contains(got, ported) {
		t.Fatalf("resumed as %q", got)
	}
}

func TestSweepStillLandsWhatTheOwnerEndedDuringAHandoff(t *testing.T) {
	w := newQueueWorld(t, "claude", "")
	*w.alive = false
	rec, _ := w.l.records.Find(w.rec.Key)
	if err := saveMigration(w.state, migration{Version: 1, Source: rec, Destination: w.work, State: "prepared", Since: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	rec.Killed = time.Now()
	if err := w.l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	if err := w.l.Sweep(map[string]string{w.rec.Key: "exited 1"}); err != nil {
		t.Fatal(err)
	}
	if rec, _ = w.l.records.Find(w.rec.Key); !rec.IsLanded() {
		t.Fatal("an owner's kill during a handoff did not land")
	}
}

func TestARecoveredPortWaitsForWhatHoldsIt(t *testing.T) {
	const ported = "01a1b2c3-0000-7000-8000-000000000042"
	for _, c := range []struct {
		name    string
		hold    func(*testing.T, queueWorld, profile.Profile) func()
		blocker string
		// ended means it stays down for good and the handoff closes.
		ended bool
		// told is how many times its manager hears it cannot come back.
		told int
	}{
		{name: "ym off", blocker: heldOff, told: 1, hold: func(t *testing.T, w queueWorld, _ profile.Profile) func() {
			w.l.core.Store.SetOff(ported, true)
			return func() { w.l.core.Store.SetOff(ported, false) }
		}},
		{name: "throttled", blocker: heldThrottled, told: 1, hold: func(t *testing.T, w queueWorld, _ profile.Profile) func() {
			dir := t.TempDir()
			if err := (pressure.Throttle{Blanket: true}).Save(dir); err != nil {
				t.Fatal(err)
			}
			w.l.core.ThrottleDir = func() (string, error) { return dir, nil }
			return func() { w.l.core.ThrottleDir = nil }
		}},
		{name: "destination out", blocker: "out of its subscription limit", told: 1, hold: func(t *testing.T, w queueWorld, cx profile.Profile) func() {
			quotaLimits(t, w.state, limit(cx, 100, time.Now().Add(time.Hour)))
			return func() { quotaLimits(t, w.state, limit(cx, 5, time.Now().Add(time.Hour))) }
		}},
		{name: "killed", ended: true, hold: func(t *testing.T, w queueWorld, _ profile.Profile) func() {
			rec, _ := w.l.records.Find(w.rec.Key)
			rec.Killed = time.Now().Add(time.Hour)
			w.l.records.Save(rec)
			return func() {}
		}},
		{name: "landed", ended: true, hold: func(t *testing.T, w queueWorld, _ profile.Profile) func() {
			rec, _ := w.l.records.Find(w.rec.Key)
			rec.Landed = time.Now()
			w.l.records.Save(rec)
			return func() {}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, _, cx := portWorld(t, false)
			*w.alive = false
			ps, err := profile.Load(w.state)
			if err != nil {
				t.Fatal(err)
			}
			dest, err := profile.Find(ps, "codex", cx.ID)
			if err != nil {
				t.Fatal(err)
			}
			source, _ := w.l.records.Find(w.rec.Key)
			if err = saveMigration(w.state, migration{Version: 1, ResumeNote: "the note", Source: source, Destination: dest, Head: ported, State: "prepared", Since: time.Now()}); err != nil {
				t.Fatal(err)
			}
			mgr, err := w.l.core.Store.AddManager(core.Manager{Role: core.RoleManager, Harness: "claude", Profile: "default", ConfigRoot: w.def.Root})
			if err != nil {
				t.Fatal(err)
			}
			mgr.Session = "33333333-3333-4333-8333-333333333333"
			if err = w.l.core.Store.SaveManager(mgr); err != nil {
				t.Fatal(err)
			}
			if err = w.l.core.Store.Link(profileConversation, mgr.Session); err != nil {
				t.Fatal(err)
			}
			release := c.hold(t, w, cx)
			tickFailover(t, w, time.Now())
			tickFailover(t, w, time.Now())
			events, err := w.l.core.Store.GoalEvents(mgr.ID)
			if err != nil {
				t.Fatal(err)
			}
			told := 0
			for _, e := range events {
				if strings.Contains(e.Payload, "cannot come back") {
					told++
				}
			}
			if told != c.told {
				t.Fatalf("its manager told %d times, want %d: %+v", told, c.told, events)
			}
			time.Sleep(200 * time.Millisecond)
			if _, err := os.Stat(w.ran); !os.IsNotExist(err) {
				t.Fatal("resumed while held")
			}
			m, _ := loadMigration(w.state, "claude", profileConversation)
			if c.ended {
				if m.State != "running" {
					t.Fatalf("the handoff of an ended session stayed open: %+v", m.State)
				}
				return
			}
			if !strings.Contains(m.Blocked, c.blocker) {
				t.Fatalf("no reason shown: %q", m.Blocked)
			}
			release()
			tickFailover(t, w, time.Now())
			if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != cx.ID {
				t.Fatalf("resumed as %q", got)
			}
			if m, _ = loadMigration(w.state, "claude", profileConversation); m.State != "running" {
				t.Fatalf("migration %s", m.State)
			}
		})
	}
}

func TestAFailedReviveOfARecoveredPortIsTriedAgain(t *testing.T) {
	const ported = "01a1b2c3-0000-7000-8000-000000000042"
	w, _, cx := portWorld(t, false)
	*w.alive = false
	ps, err := profile.Load(w.state)
	if err != nil {
		t.Fatal(err)
	}
	dest, err := profile.Find(ps, "codex", cx.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, _ := w.l.records.Find(w.rec.Key)
	if err = saveMigration(w.state, migration{Version: 1, ResumeNote: "the note", Source: source, Destination: dest, Head: ported, State: "prepared", Since: time.Now()}); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(cx.Root, "config.toml")
	putProfileFile(t, config, "openai_base_url = \"https://not-a-subscription.example\"\n")
	tickFailover(t, w, time.Now())
	if m, _ := loadMigration(w.state, "claude", profileConversation); m.State == "running" || !strings.Contains(m.Blocked, "resume failed") {
		t.Fatalf("a failed revive reads %+v", m.State)
	}
	if err = os.Remove(config); err != nil {
		t.Fatal(err)
	}
	tickFailover(t, w, time.Now())
	if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != cx.ID {
		t.Fatalf("resumed as %q", got)
	}
}

func TestAPortStepsAsideForAnOperationAlreadyUnderWayAndNeverWritesTwice(t *testing.T) {
	const ported = "01a1b2c3-0000-7000-8000-000000000042"
	for _, c := range []struct {
		name string
		run  func(*testing.T, queueWorld, profile.Profile)
	}{
		{name: "serve leaves a held lock alone", run: func(t *testing.T, w queueWorld, cx profile.Profile) {
			release, err := profile.Operation(w.state, "claude", profileConversation)
			if err != nil {
				t.Fatal(err)
			}
			tickFailover(t, w, time.Now())
			rec, _ := w.l.records.Find(w.rec.Key)
			if rec.Session != profileConversation {
				t.Fatalf("finished a port another operation holds: %s", rec.Session)
			}
			release()
		}},
		{name: "a switch retried after the write reuses it", run: func(t *testing.T, w queueWorld, cx profile.Profile) {
			next := w.l
			next.toHarness = "codex"
			if _, err := next.QueueProfileSwitch(w.rec.Key, cx.ID, "again"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, _, cx := portWorld(t, false)
			*w.alive = false
			writes := 0
			w.l.core.PreparePort = func(core.Session, string, string) (core.PortPlan, error) {
				writes++
				return core.PortPlan{}, errors.New("wrote a second port")
			}
			ps, err := profile.Load(w.state)
			if err != nil {
				t.Fatal(err)
			}
			dest, err := profile.Find(ps, "codex", cx.ID)
			if err != nil {
				t.Fatal(err)
			}
			source, _ := w.l.records.Find(w.rec.Key)
			if err = saveMigration(w.state, migration{Version: 1, ResumeNote: "the note", Source: source, Destination: dest, Head: ported, State: "prepared", Since: time.Now()}); err != nil {
				t.Fatal(err)
			}
			c.run(t, w, cx)
			if writes != 0 {
				t.Fatalf("%d more ports written", writes)
			}
		})
	}
}

func TestNoConversationNeverCrossesHarnessOrReachesThePorter(t *testing.T) {
	for _, via := range []string{"resume", "revive"} {
		t.Run(via, func(t *testing.T) {
			w, _, cx := portWorld(t, false)
			*w.alive = false
			prepared := 0
			w.l.core.PreparePort = func(core.Session, string, string) (core.PortPlan, error) {
				prepared++
				return core.PortPlan{}, errors.New("reached the porter")
			}
			if err := os.Remove(filepath.Join(w.def.Root, w.rel)); err != nil {
				t.Fatal(err)
			}
			var err error
			if via == "resume" {
				_, _, err = w.l.Resume(profileConversation, "go on", "start over")
			} else {
				_, err = w.l.Revive(w.rec.Key)
			}
			var wait *core.QuotaWait
			if !errors.As(err, &wait) {
				t.Fatalf("%s = %v, want the wait for the reset", via, err)
			}
			if prepared != 0 {
				t.Fatal("reached the porter with nothing to port")
			}
			if rec, _ := w.l.records.Find(w.rec.Key); rec.Harness != "claude" || rec.Profile == cx.ID {
				t.Fatalf("crossed harness: %+v", rec.Harness)
			}
		})
	}
}

func TestAVerifiedQuotaStopIsProofWhateverTheRowsSay(t *testing.T) {
	for _, c := range []struct {
		name string
		used float64
		rows bool
		// waiting is the session at its quota dialog; idle has nothing to prove the stop with.
		status core.Status
		moved  bool
	}{
		{name: "row at 99", used: 99, rows: true, status: core.Waiting, moved: true},
		{name: "no row", status: core.Waiting, moved: true},
		{name: "idle, row at 99", used: 99, rows: true, status: core.Idle},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			w.session.Status = c.status
			now := time.Now()
			reset := now.Add(31 * time.Hour)
			if c.rows {
				quotaLimits(t, w.state, limit(w.def, c.used, reset), limit(w.work, 5, reset))
			} else {
				quotaLimits(t, w.state, limit(w.work, 5, reset))
			}
			e := quotaEvent(w, now)
			body, _ := json.Marshal(e)
			db, err := sql.Open("sqlite", filepath.Join(w.state, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS ym_usage_exhaustions(body TEXT)`); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO ym_usage_exhaustions VALUES(?)`, string(body)); err != nil {
				t.Fatal(err)
			}
			tickFailover(t, w, now, e)
			rec, _ := w.l.records.Find(w.rec.Key)
			if c.moved != (rec.Profile == "claude-work") {
				t.Fatalf("session on %s, moved=%v", rec.Profile, c.moved)
			}
			items, err := w.l.core.Store.Items(core.ItemFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if want := map[bool]int{true: 1, false: 0}[c.moved]; len(items) != want {
				t.Fatalf("items %d, want %d", len(items), want)
			}
		})
	}
}

func TestOwnerItemsCloseWhenTheyAnswerThemselves(t *testing.T) {
	for _, c := range []struct {
		name   string
		then   func(*testing.T, queueWorld, time.Time) time.Time
		closed bool
	}{
		{name: "still needed stays open", then: func(_ *testing.T, _ queueWorld, now time.Time) time.Time { return now.Add(time.Minute) }},
		{name: "the window passed", closed: true, then: func(_ *testing.T, _ queueWorld, now time.Time) time.Time { return now.Add(32 * time.Hour) }},
		{name: "no session left stays open until the login is usable", then: func(t *testing.T, w queueWorld, now time.Time) time.Time {
			*w.alive = false
			rec, _ := w.l.records.Find(w.rec.Key)
			setRecordProfile(&rec, w.work)
			if err := w.l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			return now.Add(time.Minute)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			w.session.Status = core.Idle
			now := time.Now()
			reset := now.Add(31 * time.Hour)
			quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 5, reset))
			tickFailover(t, w, now)
			items, err := w.l.core.Store.Items(core.ItemFilter{})
			if err != nil || len(items) != 1 {
				t.Fatalf("items %d, %v", len(items), err)
			}
			at := c.then(t, w, now)
			b, err := w.l.core.Board()
			if err != nil {
				t.Fatal(err)
			}
			b.At, b.Fleet = at, core.Fleet{At: at}
			if err = w.l.Failover(b); err != nil {
				t.Fatal(err)
			}
			it, err := w.l.core.Store.Item(items[0].Key())
			if err != nil {
				t.Fatal(err)
			}
			if c.closed != (it.State == core.ItemDismissed) {
				t.Fatalf("item %s, closed=%v", it.State, c.closed)
			}
			if c.closed && it.Delivered.IsZero() {
				t.Fatal("closing the item would message the session that happened to file it")
			}
		})
	}
}

func TestASwitchWaitingOnAHoldSaysSoOnItsRow(t *testing.T) {
	w := newQueueWorld(t, "claude", "")
	if out, err := w.l.QueueProfileSwitch(w.rec.Key, "claude-work", "owner"); err != nil || !out.Queued {
		t.Fatalf("%+v, %v", out, err)
	}
	if err := w.l.core.Store.SetOff(profileConversation, true); err != nil {
		t.Fatal(err)
	}
	w.serve(t)
	if got := w.row(t); !strings.Contains(got, heldOff) {
		t.Fatalf("row %q", got)
	}
	if err := w.l.core.Store.SetOff(profileConversation, false); err != nil {
		t.Fatal(err)
	}
	w.serve(t)
	if got := w.row(t); got != "switch pending -> claude-work" {
		t.Fatalf("a hold's reason outlived it: %q", got)
	}
}

// silentHarness shows the session held at the dialog with no event behind it.
type silentHarness struct {
	turnHarness
	problem error
}

func (h silentHarness) SilentQuotaStop(core.Session, string) (core.QuotaStop, bool, error) {
	return core.QuotaStop{At: h.at}, true, h.problem
}

func TestASessionHeldAtTheQuotaDialogWithNoEventMovesWithNoHuman(t *testing.T) {
	for _, c := range []struct {
		name    string
		queued  bool
		problem error
		profile string
		blocker string
	}{
		{name: "fails over by itself", profile: "claude-work"},
		{name: "a queued switch applies", queued: true, profile: "personal"},
		{name: "what cannot be proved shows on the row", queued: true, problem: errors.New("the turn behind the quota dialog has not ended"), profile: "default", blocker: "the turn behind the quota dialog has not ended"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			now := time.Now()
			reset := now.Add(31 * time.Hour)
			quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 5, reset), limit(personal(t, w), 5, reset))
			if c.queued {
				if out, err := w.l.QueueProfileSwitch(w.rec.Key, "personal", "owner"); err != nil || !out.Queued {
					t.Fatalf("%+v, %v", out, err)
				}
			}
			base := w.l.core.Harnesses[0].(turnHarness)
			w.l.core.Harnesses = []core.Harness{silentHarness{base, c.problem}}
			w.session.Status = core.Waiting
			tickFailover(t, w, now)
			tickFailover(t, w, now)
			rec, _ := w.l.records.Find(w.rec.Key)
			if rec.Profile != c.profile {
				t.Fatalf("session on %s, want %s", rec.Profile, c.profile)
			}
			if c.blocker != "" {
				if got := w.row(t); !strings.Contains(got, c.blocker) {
					t.Fatalf("row %q", got)
				}
				return
			}
			if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != c.profile {
				t.Fatalf("resumed as %q", got)
			}
			if items, err := w.l.core.Store.Items(core.ItemFilter{}); err != nil || len(items) != 1 {
				t.Fatalf("items %d, %v", len(items), err)
			}
		})
	}
}

func TestEveryWayOntoANewLoginStartsPastOnboarding(t *testing.T) {
	onboarded := func(t *testing.T, root string) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, ".claude.json"))
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Done bool `json:"hasCompletedOnboarding"`
		}
		if err = json.Unmarshal(raw, &config); err != nil || !config.Done {
			t.Fatalf("the login was left at onboarding: %s, %v", raw, err)
		}
	}
	for _, c := range []struct {
		name string
		move func(*testing.T, queueWorld)
	}{
		{name: "failover", move: func(t *testing.T, w queueWorld) {
			w.session.Status = core.Idle
			tickFailover(t, w, time.Now(), quotaEvent(w, time.Now().Add(-time.Hour)))
		}},
		{name: "queued switch at the turn end", move: func(t *testing.T, w queueWorld) {
			if out, err := w.l.QueueProfileSwitch(w.rec.Key, "claude-work", "owner"); err != nil || !out.Queued {
				t.Fatalf("%+v, %v", out, err)
			}
			w.session.Status = core.Idle
			w.serve(t)
		}},
		{name: "resume off the wall", move: func(t *testing.T, w queueWorld) {
			*w.alive = false
			if _, _, err := w.l.Resume(profileConversation, "go on", ""); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "revive off the wall", move: func(t *testing.T, w queueWorld) {
			*w.alive = false
			if _, err := w.l.Revive(w.rec.Key); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "start over", move: func(t *testing.T, w queueWorld) {
			*w.alive = false
			if err := os.Remove(filepath.Join(w.def.Root, w.rel)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := w.l.Resume(profileConversation, "go on", "start over"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			reset := time.Now().Add(31 * time.Hour)
			quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 5, reset))
			c.move(t, w)
			if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != "claude-work" {
				t.Fatalf("resumed as %q", got)
			}
			onboarded(t, w.work.Root)
		})
	}
	t.Run("a port recovered onto claude", func(t *testing.T) {
		const ported = "33333333-3333-4333-8333-333333333333"
		w := newQueueWorld(t, "codex", "")
		*w.alive = false
		dir := strings.SplitN(os.Getenv("PATH"), ":", 2)[0]
		script, err := os.ReadFile(filepath.Join(dir, "codex"))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "claude"), script, 0700); err != nil {
			t.Fatal(err)
		}
		dest := profile.Profile{Harness: "claude", ID: "claude-dest", Root: t.TempDir(), Explicit: true}
		if err = profile.Add(w.state, dest); err != nil {
			t.Fatal(err)
		}
		dest.Root = physicalProfileRoot(dest.Root)
		subscriptionIdentity(t, &dest)
		ps, err := profile.Load(w.state)
		if err != nil {
			t.Fatal(err)
		}
		if dest, err = profile.Find(ps, "claude", "claude-dest"); err != nil {
			t.Fatal(err)
		}
		source, _ := w.l.records.Find(w.rec.Key)
		if err = saveMigration(w.state, migration{Version: 1, ResumeNote: "the note", Source: source, Destination: dest, Head: ported, State: "prepared", Since: time.Now()}); err != nil {
			t.Fatal(err)
		}
		tickFailover(t, w, time.Now())
		if got := strings.Split(resumed(t, w.ran), "\n"); got[0] != "claude-dest" {
			t.Fatalf("resumed as %q", got)
		}
		onboarded(t, dest.Root)
	})
}

func TestARowSaysWhyASessionAtTheQuotaDialogCannotMove(t *testing.T) {
	type stage struct {
		name string
		then func(*testing.T, queueWorld, time.Time)
		want string
	}
	reason := func(reset time.Time) string {
		return "stopped at its limit, no usable login, resets " + reset.UTC().Format(time.RFC3339)
	}
	for _, c := range []struct {
		name   string
		silent bool
		rows   bool
		then   stage
	}{
		{name: "silent stop, login usable later", silent: true, rows: true, then: stage{"moves", func(t *testing.T, w queueWorld, now time.Time) {
			quotaLimits(t, w.state, limit(w.def, 100, now.Add(time.Hour)), limit(w.work, 5, now.Add(time.Hour)))
			tickFailover(t, w, now)
		}, "no row"}},
		{name: "the prompt it waits on is no quota dialog", silent: true, rows: true, then: stage{"prompt", func(t *testing.T, w queueWorld, now time.Time) {
			w.session.Prompt = &core.Prompt{Tool: "Bash"}
		}, "default"}},
		{name: "ledger event, back at work", rows: true, then: stage{"working", func(t *testing.T, w queueWorld, now time.Time) {
			w.session.Status = core.Busy
		}, "default"}},
		{name: "ledger event, reset not yet known", then: stage{"working", func(t *testing.T, w queueWorld, now time.Time) {
			w.session.Status = core.Busy
		}, "default"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			now := time.Now()
			reset := now.Add(31 * time.Hour).Truncate(time.Second)
			me := personal(t, w)
			out := []core.Limit{limit(w.work, 100, reset), limit(me, 100, reset)}
			if c.rows {
				out = append(out, limit(w.def, 100, reset))
			}
			quotaLimits(t, w.state, out...)
			w.session.Status = core.Waiting
			var events []core.Exhaustion
			if c.silent {
				base := w.l.core.Harnesses[0].(turnHarness)
				w.l.core.Harnesses = []core.Harness{silentHarness{base, nil}}
			} else {
				events = append(events, quotaEvent(w, now))
			}
			tickFailover(t, w, now, events...)
			want := reason(reset)
			if !c.rows {
				want = "stopped at its limit, no usable login, tries this login again "
			}
			if got := w.row(t); !strings.HasPrefix(got, want) || !strings.Contains(got, "ym switch "+core.Short(profileConversation)+" --profile") {
				t.Fatalf("row %q, want %q and how to get out", got, want)
			}
			if *w.signals != 0 {
				t.Fatalf("stopped a session nothing can take: %d signals", *w.signals)
			}
			c.then.then(t, w, now)
			if got := w.row(t); got != c.then.want {
				t.Fatalf("after %s: row %q, want %q", c.then.name, got, c.then.want)
			}
			if rec, _ := w.l.records.Find(w.rec.Key); c.then.name == "moves" && rec.Profile != "claude-work" {
				t.Fatalf("session on %s, want claude-work", rec.Profile)
			}
		})
	}
}

func TestSessionsNamesTheLoginTheSameWayTheRowsDo(t *testing.T) {
	for _, c := range []struct {
		name    string
		profile string
		want    string
	}{
		{name: "the native login among others", want: "claude/default "},
		{name: "a named login", profile: "claude-work", want: "claude/claude-work "},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			w.session.Status = core.Idle
			if c.profile != "" {
				w.session.Profile, w.session.ConfigRoot, w.session.Account = w.work.ID, w.work.Root, w.work.Account
			}
			w.l.core.ProfileList = func() []core.LoginProfile {
				return []core.LoginProfile{{Harness: "claude", ID: "default"}, {Harness: "claude", ID: "claude-work"}}
			}
			var out bytes.Buffer
			with := func(run runFunc) func(*cobra.Command, []string) error {
				return func(cmd *cobra.Command, args []string) error { return run(&env{core: w.l.core}, cmd, args) }
			}
			cmd := sessionsCmd(with)
			cmd.SetOut(&out)
			cmd.SetArgs(nil)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(out.String(), c.want) {
				t.Fatalf("sessions printed %q, want it to start %q", out.String(), c.want)
			}
		})
	}
}

// followLogin puts the live session on whichever login its record now names, held at the dialog again, as Claude does on a used-up login.
func dialogWorld(t *testing.T) (queueWorld, *time.Time) {
	t.Helper()
	w := newQueueWorld(t, "claude", "")
	base := w.l.core.Harnesses[0].(turnHarness)
	w.l.core.Harnesses = []core.Harness{silentHarness{base, nil}}
	clock := time.Now()
	w.l.core.Now = func() time.Time { return clock }
	w.session.Status = core.Waiting
	signal := w.l.core.Signal
	w.l.core.Signal = func(pid int, sig syscall.Signal) error {
		w.l.srv.Kill(tmux.SessionName(w.rec.Key))
		return signal(pid, sig)
	}
	return w, &clock
}

const ResumePaceForTest = core.ResumePace + time.Second

func TestAPassedLimitContinuesTheSessionWhereItIs(t *testing.T) {
	for _, c := range []struct {
		name string
		// silent has no ledger event, only the dialog.
		silent bool
		// row reads 20% after the stop rather than the window having passed.
		row   bool
		fails bool
		want  string
	}{
		{name: "ledger stop, window passed"},
		{name: "silent stop, window passed", silent: true},
		{name: "what cannot continue says so", fails: true, want: "could not continue"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, clock := dialogWorld(t)
			if !c.silent {
				base := w.l.core.Harnesses[0].(silentHarness).turnHarness
				w.l.core.Harnesses = []core.Harness{base}
			}
			start := *clock
			reset := start.Add(time.Hour)
			me := personal(t, w)
			quotaLimits(t, w.state, limit(w.def, 100, reset), limit(w.work, 100, reset.Add(time.Hour)), limit(me, 100, reset.Add(time.Hour)))
			var events []core.Exhaustion
			if !c.silent {
				events = append(events, quotaEvent(w, start))
			}
			tickFailover(t, w, start, events...)
			if got := w.row(t); !strings.Contains(got, "no usable login") {
				t.Fatalf("row %q", got)
			}
			after := reset.Add(time.Minute)
			if c.row {
				after = start.Add(time.Minute)
				row := limit(w.def, 20, reset)
				row.Seen = time.Now().Add(time.Minute)
				quotaLimits(t, w.state, row, limit(w.work, 100, reset.Add(time.Hour)), limit(me, 100, reset.Add(time.Hour)))
			} else {
				quotaLimits(t, w.state, limit(w.work, 100, reset.Add(time.Hour)), limit(me, 100, reset.Add(time.Hour)))
			}
			if c.fails {
				w.l.core.Signal = func(int, syscall.Signal) error { return errors.New("cannot stop it") }
			}
			*clock = after.Add(ResumePaceForTest)
			tickFailover(t, w, *clock, events...)
			if c.fails {
				if got := w.row(t); !strings.Contains(got, c.want) {
					t.Fatalf("row %q, want %q", got, c.want)
				}
				return
			}
			got := strings.Split(resumed(t, w.ran), "\n")
			if got[0] != "default" || !strings.Contains(got[1], "has passed") {
				t.Fatalf("resumed as %q", got)
			}
			if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "default" {
				t.Fatalf("moved to %s", rec.Profile)
			}
			items, _ := w.l.core.Store.Items(core.ItemFilter{})
			if len(items) != 1 {
				t.Fatalf("items %d: nothing may claim a limit its rows disprove", len(items))
			}
		})
	}
}

func TestADialogOnScreenIsAStopWhateverAnOlderRowSays(t *testing.T) {
	w, clock := dialogWorld(t)
	row := limit(w.def, 20, clock.Add(time.Hour))
	row.Seen = clock.Add(-time.Minute)
	quotaLimits(t, w.state, row, limit(w.work, 5, clock.Add(time.Hour)))
	tickFailover(t, w, *clock)
	if bar, err := readBar(barPath(w.state, w.def)); err != nil || !bar.Until.After(*clock) {
		t.Fatalf("bar %+v, %v", bar, err)
	}
	if items, err := w.l.core.Store.Items(core.ItemFilter{}); err != nil || len(items) != 1 {
		t.Fatalf("items %d, %v", len(items), err)
	}
}

func TestReviveStartsOverAConversationVerifiablyGone(t *testing.T) {
	w := newQueueWorld(t, "claude", "")
	*w.alive = false
	if err := os.Remove(filepath.Join(w.def.Root, w.rel)); err != nil {
		t.Fatal(err)
	}
	rec, _ := w.l.records.Find(w.rec.Key)
	rec.Prompt = "its first prompt"
	if err := w.l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	msg, err := w.l.Revive(w.rec.Key)
	if err != nil || !strings.Contains(msg, "started") {
		t.Fatalf("%q, %v", msg, err)
	}
	got := resumed(t, w.ran)
	if strings.Contains(got, "--resume") || !strings.Contains(got, "its first prompt") {
		t.Fatalf("ran %q", got)
	}
}

// noDialogHarness says the screen shows no quota dialog.
type noDialogHarness struct{ turnHarness }

func (noDialogHarness) QuotaDialog(string) bool { return false }

func TestRowsSayWhyAStoppedSessionCannotMoveAndOnlyAtTheDialog(t *testing.T) {
	for _, c := range []struct {
		name string
		make func(*testing.T, queueWorld, time.Time) []core.Exhaustion
		want string
	}{
		{name: "the only target is signed out", want: "stopped at its limit: ", make: func(t *testing.T, w queueWorld, now time.Time) []core.Exhaustion {
			dir := strings.SplitN(os.Getenv("PATH"), ":", 2)[0]
			if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			quotaLimits(t, w.state, limit(w.def, 100, now.Add(time.Hour)), limit(w.work, 5, now.Add(time.Hour)), limit(personal(t, w), 100, now.Add(time.Hour)))
			return []core.Exhaustion{quotaEvent(w, now)}
		}},
		{name: "a dialog it cannot prove over", want: "stopped at its limit: a child is busy", make: func(t *testing.T, w queueWorld, now time.Time) []core.Exhaustion {
			base := w.l.core.Harnesses[0].(turnHarness)
			w.l.core.Harnesses = []core.Harness{silentHarness{base, errors.New("a child is busy")}}
			quotaLimits(t, w.state, limit(w.work, 5, now.Add(time.Hour)))
			return nil
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude", "")
			w.session.Status = core.Waiting
			now := time.Now()
			events := c.make(t, w, now)
			tickFailover(t, w, now, events...)
			if got := w.row(t); !strings.Contains(got, c.want) {
				t.Fatalf("row %q, want %q", got, c.want)
			}
			if items, err := w.l.core.Store.Items(core.ItemFilter{}); err != nil || len(items) != 1 {
				t.Fatalf("items %d, %v", len(items), err)
			}
			w.session.Prompt = &core.Prompt{Tool: "Bash"}
			if got := w.row(t); got != "default" {
				t.Fatalf("waiting on a prompt: row %q", got)
			}
			w.session.Prompt = nil
			if base, ok := w.l.core.Harnesses[0].(turnHarness); ok {
				w.l.core.Harnesses = []core.Harness{noDialogHarness{base}}
				if got := w.row(t); got != "default" {
					t.Fatalf("no dialog on screen: row %q", got)
				}
			}
		})
	}
}

func TestAFailingPortKeepsBeingTriedOnAQuietHost(t *testing.T) {
	w, tick, _ := portWorld(t, false)
	w.session.Status = core.Idle
	mgr, err := w.l.core.Store.AddManager(core.Manager{Role: core.RoleManager, Harness: "claude", Profile: "default", ConfigRoot: w.def.Root})
	if err != nil {
		t.Fatal(err)
	}
	mgr.Session = "33333333-3333-4333-8333-333333333333"
	if err = w.l.core.Store.SaveManager(mgr); err != nil {
		t.Fatal(err)
	}
	if err = w.l.core.Store.Link(profileConversation, mgr.Session); err != nil {
		t.Fatal(err)
	}
	writes := 0
	prepare := w.l.core.PreparePort
	w.l.core.PreparePort = func(s core.Session, to, id string) (core.PortPlan, error) {
		plan, err := prepare(s, to, id)
		plan.Write = func() (string, error) { writes++; return "", errors.New("cs failed") }
		return plan, err
	}
	e := quotaEvent(w, time.Now().Add(-time.Hour))
	tick(e)
	*w.alive = false
	tick(e)
	tick(e)
	settled := writes
	tick(e)
	if writes != settled {
		t.Fatalf("retried every tick: %d then %d writes", settled, writes)
	}
	m, _ := loadMigration(w.state, "claude", profileConversation)
	m.BlockedAt = time.Now().Add(-core.ResumeRetry - time.Second)
	if err := saveMigration(w.state, m); err != nil {
		t.Fatal(err)
	}
	tick(e)
	if writes == settled {
		t.Fatalf("a failing port stopped being tried: still %d writes", writes)
	}
	events, err := w.l.core.Store.GoalEvents(mgr.ID)
	if err != nil {
		t.Fatal(err)
	}
	told := 0
	for _, e := range events {
		if e.Kind == core.EventAgentStopped && strings.Contains(e.Payload, "has not finished") {
			told++
		}
	}
	if told != 1 {
		t.Fatalf("the manager was told %d times: %+v", told, events)
	}
}

func TestARecoveredPortHeldByAThrottleOnItsOldIDWaits(t *testing.T) {
	const ported = "01a1b2c3-0000-7000-8000-000000000042"
	w, _, cx := portWorld(t, false)
	*w.alive = false
	dir := t.TempDir()
	if err := (pressure.Throttle{Paused: []pressure.Paused{{PID: 1, Session: profileConversation}}}).Save(dir); err != nil {
		t.Fatal(err)
	}
	w.l.core.ThrottleDir = func() (string, error) { return dir, nil }
	ps, _ := profile.Load(w.state)
	dest, err := profile.Find(ps, "codex", cx.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, _ := w.l.records.Find(w.rec.Key)
	if err = saveMigration(w.state, migration{Version: 1, ResumeNote: "n", Source: source, Destination: dest, Head: ported, State: "prepared", Since: time.Now()}); err != nil {
		t.Fatal(err)
	}
	tickFailover(t, w, time.Now())
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(w.ran); !os.IsNotExist(err) {
		t.Fatal("resumed while the throttle held its old id")
	}
}

func TestAnUnreadableConversationIsNotGoneToGC(t *testing.T) {
	w := newQueueWorld(t, "claude", "")
	rec, _ := w.l.records.Find(w.rec.Key)
	path := filepath.Join(w.def.Root, w.rel)
	if conversationGone(rec) {
		t.Fatal("a conversation on disk reads as gone")
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0600) })
	if conversationGone(rec) {
		t.Fatal("an unreadable conversation reads as gone")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !conversationGone(rec) {
		t.Fatal("a removed conversation does not read as gone")
	}
}

// wrappedHarness is the real profile harness over a session list a test controls, so core's assertions meet what the real binary wires.
type wrappedHarness struct {
	*profileHarness
	session *core.Session
}

func (h wrappedHarness) Owns(p proc.Proc) bool { return p.PID == 4242 }
func (h wrappedHarness) Discover(ps []proc.Proc) []core.Session {
	var out []core.Session
	for _, p := range ps {
		if p.PID == 4242 {
			s := *h.session
			s.PID, s.TTY = p.PID, p.TTY
			out = append(out, s)
		}
	}
	return out
}
func (h wrappedHarness) ParsePrompt(string) (core.Prompt, bool) { return core.Prompt{}, false }

// realScreenWorld is a session on the default login, behind the real wrapper, whose screen is text and transcript the shape Claude left.
func realScreenWorld(t *testing.T, text, status, lastTurn string) queueWorld {
	t.Helper()
	w := newQueueWorld(t, "claude", "")
	w.l.core.Harnesses = []core.Harness{wrappedHarness{&profileHarness{name: "claude", dir: w.state}, w.session}}
	w.l.core.Hosts = []core.Host{quotaHost{text: text}}
	cwd := w.rec.Worktree.Path
	path := filepath.Join(w.def.Root, "projects", "-real", profileConversation+".jsonl")
	user := `{"type":"user","sessionId":"` + profileConversation + `","timestamp":"` + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano) + `","message":{"role":"user","content":"go"}}`
	putProfileFile(t, path, user+"\n"+lastTurn+"\n")
	if err := os.Remove(filepath.Join(w.def.Root, w.rel)); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"pid": 4242, "sessionId": profileConversation, "cwd": cwd, "status": status, "statusUpdatedAt": time.Now().UnixMilli()})
	putProfileFile(t, filepath.Join(w.def.Root, "sessions", "4242.json"), string(raw))
	return w
}

func TestARealQuotaStopMovesThroughTheRealHarness(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "harness", "claude", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	ended := `{"type":"assistant","sessionId":"` + profileConversation + `","timestamp":"` + time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano) + `","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"FINISHED"}]}}`
	footer := read("quota-footer.txt")
	dialog := read("silent-quota-dialog.txt")
	for _, c := range []struct {
		name   string
		screen string
		status string
		live   core.Status
		used   float64
		moved  bool
	}{
		{name: "dialog, row at 100", screen: dialog, status: "waiting", live: core.Waiting, used: 100, moved: true},
		{name: "dialog with no row", screen: dialog, status: "waiting", live: core.Waiting, moved: true},
		{name: "footer reads idle, row at 100", screen: footer, status: "idle", live: core.Idle, used: 100, moved: true},
		{name: "footer outranks a row at 50", screen: footer, status: "idle", live: core.Idle, used: 50, moved: true},
		{name: "a reply reciting the footer, composer below", screen: "⚠ Usage limit reached · limit resets 3am\nContinuing automatically at 3am · esc to cancel\n✻ Done\n" + strings.Repeat("─", 40) + "\n❯ \n" + strings.Repeat("─", 40) + "\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n", status: "idle", live: core.Idle, used: 50},
		{name: "a reply reciting the dialog", screen: "● " + dialog + "\n✻ Brewed\n" + strings.Repeat("─", 40) + "\n❯ \n" + strings.Repeat("─", 40) + "\n", status: "idle", live: core.Idle, used: 50},
		{name: "an ordinary idle screen", screen: "✻ Done\n❯ \n", status: "idle", live: core.Idle, used: 50},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := realScreenWorld(t, c.screen, c.status, ended)
			w.session.Status = c.live
			now := time.Now()
			reset := now.Add(31 * time.Hour)
			rows := []core.Limit{limit(w.work, 5, reset)}
			if c.used > 0 {
				stale := limit(w.def, c.used, reset)
				stale.Seen = now.Add(-time.Minute)
				rows = append(rows, stale)
			}
			quotaLimits(t, w.state, rows...)
			tickFailover(t, w, now)
			rec, _ := w.l.records.Find(w.rec.Key)
			if c.moved != (rec.Profile == "claude-work") {
				t.Fatalf("session on %s, moved=%v", rec.Profile, c.moved)
			}
			items, err := w.l.core.Store.Items(core.ItemFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if want := map[bool]int{true: 1, false: 0}[c.moved]; len(items) != want {
				t.Fatalf("items %d, want %d", len(items), want)
			}
		})
	}
}

func TestAPaneOutlivingTheStopDoesNotFailTheResume(t *testing.T) {
	w := newQueueWorld(t, "claude", "")
	*w.alive = false
	rec, _ := w.l.records.Find(w.rec.Key)
	name := tmux.SessionName(rec.Key)
	if _, err := w.l.srv.Start(w.l.conf, name, rec.Worktree.Path, []string{"sleep", "30"}); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		w.l.srv.Kill(name)
	}()
	once := w.l
	once.operation = rec.Session
	if _, err := once.revive(rec); err != nil {
		t.Fatalf("resume hit a pane the switch had just stopped: %v", err)
	}
}
