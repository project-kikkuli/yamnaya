package cli

import (
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

type queueHarness struct {
	switchingHarness
	live *core.Session
}

func (h queueHarness) Discover(ps []proc.Proc) []core.Session {
	var out []core.Session
	for _, p := range ps {
		if p.PID != queuePID {
			continue
		}
		s := *h.live
		s.PID, s.TTY = p.PID, p.TTY
		out = append(out, s)
	}
	return out
}

const queuePID = 4242

type queueWorld struct {
	l        launcher
	state    string
	ran      string
	rec      launch.Record
	rel      string
	profiles map[string]profile.Profile
	session  *core.Session
	alive    *bool
	signals  *int
	external *[]proc.Proc
}

func newQueueWorld(t *testing.T, harness string) queueWorld {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	state, cwd, bins := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", state)
	t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
	ran := filepath.Join(t.TempDir(), "resumed")
	script := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in *\" auth status \"*|*\" login status \"*) printf '%%s\\n' '{\"loggedIn\":true,\"authMethod\":\"claude.ai\"}'; exit 0;; esac\nprintf '%%s\\n' \"$YAMNAYA_PROFILE\" \"$*\" > %s.tmp && mv %s.tmp %s\nexec sleep 30\n", shellQuote(ran), shellQuote(ran), shellQuote(ran))
	if err := os.WriteFile(filepath.Join(bins, harness), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	profiles := map[string]profile.Profile{}
	for _, id := range []string{"a", "b", "c"} {
		p := profile.Profile{Harness: harness, ID: id, Root: t.TempDir(), Explicit: true}
		if err := profile.Add(state, p); err != nil {
			t.Fatal(err)
		}
		p.Root = physicalProfileRoot(p.Root)
		subscriptionIdentity(t, &p)
		profiles[id] = p
	}
	a := profiles["a"]
	rel, data := filepath.Join("projects", "p", profileConversation+".jsonl"), profileResponse(harness, time.Now().UTC().Format(time.RFC3339Nano), "first", 100, 20)
	if harness == "codex" {
		rel = filepath.Join("sessions", "rollout-2026-10-23T10-00-00-"+profileConversation+".jsonl")
		data = profileMeta(profileConversation, "", 0) + data
	}
	putProfileFile(t, filepath.Join(a.Root, rel), data)
	st, err := core.OpenStore(filepath.Join(state, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	session := &core.Session{Harness: harness, ID: profileConversation, Profile: "a", ConfigRoot: a.Root, Account: a.Account, Status: core.Busy, Cwd: cwd}
	alive, signals := true, 0
	external := &[]proc.Proc{}
	l := testLauncher(t, state)
	native := queueHarness{switchingHarness: switchingHarness{session: *session}, live: session}
	l.core = &core.Core{Store: st, StateDir: state, Harnesses: []core.Harness{native},
		Procs: func() ([]proc.Proc, error) {
			out := append([]proc.Proc(nil), *external...)
			if alive {
				out = append(out, proc.Proc{PID: queuePID, TTY: "/dev/ttys999", Args: []string{harness, "resume", profileConversation}})
			}
			return out, nil
		},
		Signal: func(int, syscall.Signal) error { signals++; alive = false; return nil },
	}
	l.rootOf = func(pid int, key string) string {
		if key == "CODEX_HOME" {
			return a.Root
		}
		if key == "YAMNAYA_LAUNCH_KEY" && pid == queuePID {
			return "conversation"
		}
		return ""
	}
	l.core.Launches = l
	rec := launch.Record{Key: "conversation", Harness: harness, Session: profileConversation, NativePID: queuePID, Dedicated: harness == "codex", Adopted: true, Worktree: launch.Worktree{Path: cwd}}
	setRecordProfile(&rec, a)
	if err = l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	return queueWorld{l: l, state: state, ran: ran, rec: rec, rel: rel, profiles: profiles, session: session, alive: &alive, signals: &signals, external: external}
}

// row is the session's row on the board as ym serve and the TUI read it.
func (w queueWorld) row(t *testing.T) core.Live {
	t.Helper()
	b, err := w.l.core.Board()
	if err != nil {
		t.Fatal(err)
	}
	s, ok := b.Session(profileConversation)
	if !ok {
		t.Fatal("session is not on the board")
	}
	return s
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

func (w queueWorld) untouched(t *testing.T, id string) {
	t.Helper()
	if *w.signals != 0 {
		t.Fatalf("source was stopped %d times", *w.signals)
	}
	saved, _ := w.l.records.Find(w.rec.Key)
	if saved.Profile != "a" {
		t.Fatalf("record moved to %s", saved.Profile)
	}
	if _, err := os.Stat(filepath.Join(w.profiles[id].Root, w.rel)); !os.IsNotExist(err) {
		t.Fatalf("destination has the conversation before the switch applied: %v", err)
	}
}

func TestQueuedSwitchAppliesWhenTheSessionItselfIsIdle(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		for _, tc := range []struct {
			name    string
			crew    []core.Member
			running map[string]core.Action
			names   []string
		}{
			{name: "nothing running", names: []string{"ym switched this conversation from profile a to profile b"}},
			{name: "crew still busy", crew: []core.Member{{ID: "agent1", Name: "reviewer", Status: core.Busy}, {ID: "agent2", Name: "finished", Status: core.Done}},
				names: []string{"subagent reviewer (agent1)", "Re-check each one"}},
			{name: "background work", running: map[string]core.Action{"ba3nxn8vc": {Tool: "Bash", Detail: "go test"}, "a5c01": {Tool: "Agent", Detail: "Check TUI"}}, crew: []core.Member{{ID: "a5c01", Name: "Check TUI", Status: core.Busy}},
				names: []string{"Bash: go test (task ba3nxn8vc)", "Agent: Check TUI (task a5c01)"}},
		} {
			t.Run(harness+"/"+tc.name, func(t *testing.T) {
				w := newQueueWorld(t, harness)
				msg, err := w.l.SwitchProfile(w.rec.Key, "b")
				if err != nil || !strings.Contains(msg, "switch pending -> b") {
					t.Fatalf("busy switch: %q %v", msg, err)
				}
				w.untouched(t, "b")
				if got := w.row(t).Switch.String(); got != "switch pending -> b" {
					t.Fatalf("row shows %q", got)
				}
				w.serve(t)
				w.untouched(t, "b")
				w.session.Status, w.session.Prompt = core.Idle, &core.Prompt{Tool: "Bash"}
				w.serve(t)
				w.untouched(t, "b")

				w.session.Prompt, w.session.Crew, w.session.Running = nil, tc.crew, tc.running
				w.serve(t)
				if *w.signals == 0 {
					t.Fatal("source never stopped at the turn boundary")
				}
				saved, _ := w.l.records.Find(w.rec.Key)
				if saved.Profile != "b" || saved.ConfigRoot != w.profiles["b"].Root || saved.Session != w.rec.Session {
					t.Fatalf("record after apply: %#v", saved)
				}
				got := resumed(t, w.ran)
				if !strings.HasPrefix(got, "b\n") || !strings.Contains(got, profileConversation) {
					t.Fatalf("resume: %q", got)
				}
				for _, want := range tc.names {
					if !strings.Contains(got, want) {
						t.Fatalf("resume note lacks %q: %q", want, got)
					}
				}
				if strings.Contains(got, "finished") || strings.Count(got, "a5c01") > 1 {
					t.Fatalf("resume note names the wrong work: %q", got)
				}
				m, err := loadMigration(w.state, harness, profileConversation)
				if err != nil || m.State != "running" {
					t.Fatalf("migration: %+v %v", m, err)
				}
				*w.alive = true
				w.session.Profile, w.session.ConfigRoot, w.session.Account = "b", w.profiles["b"].Root, w.profiles["b"].Account
				w.session.Crew, w.session.Running = nil, nil
				if s := w.row(t); s.Profile != "b" || s.Switch.String() != "" {
					t.Fatalf("applied row: %s %q", s.Profile, s.Switch)
				}
			})
		}
	}
}

func TestSecondSwitchReplacesTheQueuedOne(t *testing.T) {
	for _, tc := range []struct {
		name, second string
		pending      string
		apply        string
	}{
		{name: "other login", second: "c", pending: "switch pending -> c", apply: "c"},
		{name: "same login", second: "b", pending: "switch pending -> b", apply: "b"},
		{name: "back to the current login", second: "a", apply: "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newQueueWorld(t, "claude")
			for _, id := range []string{"b", tc.second} {
				if _, err := w.l.SwitchProfile(w.rec.Key, id); err != nil {
					t.Fatal(err)
				}
			}
			w.untouched(t, "b")
			if got := w.row(t).Switch.String(); got != tc.pending {
				t.Fatalf("row shows %q, want %q", got, tc.pending)
			}
			w.session.Status = core.Idle
			w.serve(t)
			saved, _ := w.l.records.Find(w.rec.Key)
			if saved.Profile != tc.apply {
				t.Fatalf("session on %s, want %s", saved.Profile, tc.apply)
			}
			if tc.apply == "a" && *w.signals != 0 {
				t.Fatal("a dropped switch stopped the session")
			}
		})
	}
}

func TestRefusedQuiesceStaysQueuedWithAVisibleBlocker(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refuse  func(t *testing.T, w queueWorld) func()
		blocker string
	}{
		{name: "external owner", blocker: "external/shared Codex lifecycle pid 999", refuse: func(t *testing.T, w queueWorld) func() {
			*w.external = []proc.Proc{{PID: 999, Args: []string{"codex", "app-server"}}}
			return func() { *w.external = nil }
		}},
		{name: "live writer", blocker: "native thread publication is active", refuse: func(t *testing.T, w queueWorld) func() {
			release, err := writerLock(w.profiles["b"].Root, profileConversation)
			if err != nil {
				t.Fatal(err)
			}
			return func() {
				release()
				*w.external = []proc.Proc{{PID: queuePID + 1, PPID: queuePID, Args: []string{"sleep"}}}
				putProfileFile(t, filepath.Join(w.profiles["a"].Root, w.rel), profileMeta(profileConversation, "", 0))
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newQueueWorld(t, "codex")
			if _, err := w.l.SwitchProfile(w.rec.Key, "b"); err != nil {
				t.Fatal(err)
			}
			clear := tc.refuse(t, w)
			w.session.Status = core.Idle
			w.serve(t)
			w.untouched(t, "b")
			m, err := loadMigration(w.state, "codex", profileConversation)
			if err != nil || m.State != migrationQueued || !strings.Contains(m.Blocked, tc.blocker) {
				t.Fatalf("migration: %+v %v", m, err)
			}
			if got := w.row(t).Switch.String(); !strings.Contains(got, "switch -> b blocked: ") || !strings.Contains(got, tc.blocker) {
				t.Fatalf("row shows %q", got)
			}
			var listed core.PendingSwitch
			b, err := w.l.core.Board()
			if err != nil {
				t.Fatal(err)
			}
			for _, j := range sessionsJSON(w.l.core, b) {
				listed = core.PendingSwitch{To: j.SwitchTo, Blocked: j.SwitchBlocked}
			}
			if !strings.Contains(listed.Blocked, tc.blocker) {
				t.Fatalf("sessions JSON lost the blocker: %+v", listed)
			}
			clear()
			w.serve(t)
			saved, _ := w.l.records.Find(w.rec.Key)
			if saved.Profile != "b" {
				t.Fatalf("switch never applied once the owner cleared: %+v", saved)
			}
			if m, _ = loadMigration(w.state, "codex", profileConversation); m.Blocked != "" {
				t.Fatalf("blocker outlived the block: %q", m.Blocked)
			}
		})
	}
}

func TestQueuedSwitchWaitsForAnExitedSessionsRevive(t *testing.T) {
	w := newQueueWorld(t, "claude")
	if _, err := w.l.SwitchProfile(w.rec.Key, "b"); err != nil {
		t.Fatal(err)
	}
	*w.alive = false
	w.serve(t)
	w.untouched(t, "b")
	recovered, err := w.l.recoverMigration(w.rec)
	if err != nil || recovered.Profile != "b" {
		t.Fatalf("revive: %+v %v", recovered, err)
	}
}
