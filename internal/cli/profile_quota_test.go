package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
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
					{Harness: "claude", Profile: work.ID, Account: work.Account, Name: "7d", Used: workUsed, Seen: now, ResetsAt: now.Add(7 * 24 * time.Hour)},
				} {
					body, _ := json.Marshal(w)
					if _, err := db.Exec(`INSERT INTO ym_usage_limits VALUES(?)`, string(body)); err != nil {
						t.Fatal(err)
					}
				}
			}
			setLimits(c.workUsed)

			l := testLauncher(t, state)
			s := core.Session{Harness: "claude", ID: profileConversation, Profile: "default", ConfigRoot: home.Root, Account: home.Account, Status: core.Idle, Cwd: cwd}
			l.core = &core.Core{Store: st, StateDir: state, Harnesses: []core.Harness{switchingHarness{session: s}}, Hosts: []core.Host{quotaHost{}},
				Procs:  func() ([]proc.Proc, error) { return nil, nil },
				Signal: func(int, syscall.Signal) error { return nil },
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
				prior := failoverAttempt{State: core.FailoverState{Session: profileConversation, Profile: "default", Account: home.Account, Status: "blocked", Reason: "no different verified usable subscription pool", Reset: reset}, Pools: map[string]time.Time{home.Account: reset}, Event: hit.Event}
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
					b := core.Board{At: now, Fleet: core.Fleet{At: now, Exhaustions: []core.Exhaustion{hit, other}, Limits: profileLimits(state)}}
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
			if c.restart {
				wantItems = 0
			}
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
			_, _, err = l.Resume(profileConversation, workResumeTextFixture, "")
			if err == nil || !strings.Contains(err.Error(), "out of its subscription limit") {
				t.Fatalf("resume onto exhausted login: %v", err)
			}
			if _, err := os.Stat(ran); !os.IsNotExist(err) {
				t.Fatal("ym resumed the session onto its exhausted login")
			}

			setLimits(5)
			b := core.Board{At: now, Fleet: core.Fleet{At: now, Exhaustions: []core.Exhaustion{hit}, Limits: profileLimits(state)}}
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
			items, err := w.l.core.Store.Items(core.ItemFilter{})
			if err != nil || len(items) != 1 {
				t.Fatalf("owner items %d, %v, want 1", len(items), err)
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
			row := failoverAttempt{Event: "quota-1", Pools: map[string]time.Time{w.def.Account: reset},
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
	for _, lim := range limits {
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
			if m, _ := loadMigration(w.state, "claude", profileConversation); m.State != "running" || m.Head != ported {
				t.Fatalf("migration %+v", m)
			}
		})
	}
}
