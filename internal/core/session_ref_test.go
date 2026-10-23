package core

import (
	"errors"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

var collidingSessions = []struct {
	name, first, second, short, prefix string
}{
	{"codex UUIDv7", "01a16a70-a4c4-7670-a0a2-e092c1f5e145", "01a16a70-a4c4-7670-a0a2-e0a21f96395f", "e092c1f5e145", "01a16a70"},
	{"claude UUIDv4", "61ffadaf-e067-418c-bf5a-71518e8fba10", "61ffadaf-e067-418c-bf5a-71518e8fba11", "61ffadaf", "61ffadaf"},
}

func TestSessionReferencesResolveFullIDsAndRejectAmbiguity(t *testing.T) {
	for _, c := range collidingSessions {
		t.Run(c.name, func(t *testing.T) {
			if got := Short(c.first); got != c.short {
				t.Fatalf("short = %q, want %q", got, c.short)
			}
			ids := []string{c.first, c.second, c.first}
			for _, id := range []string{c.first, c.second} {
				if got, err := SessionRef(id, ids); err != nil || got != id {
					t.Fatalf("full %s = %s, %v", id, got, err)
				}
			}
			if _, err := SessionRef(c.prefix, ids); !errors.Is(err, ErrAmbiguous) {
				t.Fatalf("shared prefix resolved: %v", err)
			}
			if got, err := SessionRef(c.short, []string{c.first, c.first}); err != nil || got != c.first {
				t.Fatalf("duplicate processes for one session = %s, %v", got, err)
			}
		})
	}
	const id = "01a16a70-a4c4-7670-a0a2-e092c1f5e145"
	for _, c := range []struct{ ref, want string }{
		{"e092c1f5e145", id}, {"01a16a70", id}, {"01a16", ""}, {"missing", ""},
	} {
		got, err := SessionRef(c.ref, []string{id})
		if got != c.want || (err == nil) != (c.want != "") {
			t.Errorf("%q = %q, %v, want %q", c.ref, got, err, c.want)
		}
	}
	if _, err := SessionRef(Short(id), []string{id, "01a16a71-a4c4-7670-a0a2-e092c1f5e145"}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("shared random tails resolved: %v", err)
	}
	if got, err := SessionRef(id, []string{id + "-copy", id}); err != nil || got != id {
		t.Fatalf("exact id lost to a prefix match: %s, %v", got, err)
	}
}

func TestFindAndCallerKeepCollidingSessionsOnTheirOwnPanes(t *testing.T) {
	c := collidingSessions[0]
	w := newWorld(t)
	for i, id := range []string{c.first, c.second} {
		tty, pane := []string{"/dev/t1", "/dev/t2"}[i], []string{"%1", "%2"}[i]
		w.run(10+i, 1, tty, "codex", id, Busy)
		w.tmux.ttys[tty] = pane
		for _, ref := range []string{id, Short(id)} {
			live, err := w.core.Find(ref)
			if err != nil || live.ID != id || live.PID != 10+i || live.Handle == nil || live.Handle.ID != pane {
				t.Fatalf("Find(%s) = %+v, %v", ref, live, err)
			}
			caller, err := w.core.CallerOf(ref)
			if err != nil || caller.Session != id || caller.PID != 10+i {
				t.Fatalf("CallerOf(%s) = %+v, %v", ref, caller, err)
			}
		}
	}
	for _, resolve := range []func(string) error{
		func(ref string) error { _, err := w.core.Find(ref); return err },
		func(ref string) error { _, err := w.core.CallerOf(ref); return err },
	} {
		if err := resolve(c.prefix); !errors.Is(err, ErrAmbiguous) {
			t.Fatalf("shared prefix resolved: %v", err)
		}
	}
	other := collidingSessions[1]
	w.run(20, 1, "/dev/t3", "claude", other.first, Busy)
	w.run(21, 1, "/dev/t4", "codex", other.second, Busy)
	if _, err := w.core.Find(other.prefix); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("shared prefix across harnesses resolved: %v", err)
	}
}

func TestCollidingSessionPrefixesKeepItemsAndGoalAgentsSeparate(t *testing.T) {
	w := newOrgWorld(t)
	_, first := w.goal(t, "import CSV", "/repo/csv")
	_, second := w.goal(t, "import TSV", "/repo/tsv")
	c := collidingSessions[0]
	for i, id := range []string{c.first, c.second} {
		manager := []Manager{first, second}[i]
		w.spawn(id, manager.Session)
		caller, err := w.core.CallerOf(Short(id))
		if err != nil || caller.Session != id {
			t.Fatalf("caller = %+v, %v", caller, err)
		}
		it := w.ask(t, caller.Session, "which delimiter?")
		if it.Session != id || it.Recipient != manager.ID {
			t.Fatalf("item = %+v, want %s under %s", it, id, manager.ID)
		}
		if _, _, err := w.core.Answer(w.board(t), it.Key(), "use this delimiter"); err != nil {
			t.Fatal(err)
		}
		if got := w.handedTo(id); len(got) != 1 {
			t.Fatalf("answers to %s: %v", id, got)
		}
	}
	b := w.board(t)
	for i, manager := range []Manager{first, second} {
		n := node(t, b, manager.ID)
		id := []string{c.first, c.second}[i]
		if len(n.Agents) != 1 || n.Agents[0].ID != id || len(n.Workers) != 1 || n.Workers[0].Session != id {
			t.Fatalf("%s agents = %+v, workers = %+v", manager.ID, n.Agents, n.Workers)
		}
	}
}

func TestRemoteSessionReferencesRejectAmbiguity(t *testing.T) {
	w := newWorld(t)
	c := collidingSessions[0]
	w.core.Remotes = &fakeRemotes{boards: []RemoteBoard{
		{Host: "one", Sessions: []Live{{Session: Session{ID: c.first, Harness: "codex"}}}},
		{Host: "two", Sessions: []Live{{Session: Session{ID: c.second, Harness: "codex"}}}},
	}}
	if _, err := w.core.Find(c.prefix); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("shared remote prefix resolved: %v", err)
	}
	if host, _, ok := w.core.RemoteSession(c.prefix); ok {
		t.Fatalf("shared remote prefix picked %s", host)
	}
	for i, id := range []string{c.first, c.second} {
		if s, err := w.core.Find(Short(id)); err != nil || s.ID != id || s.Remote != []string{"one", "two"}[i] {
			t.Fatalf("remote lookup = %+v, %v", s, err)
		}
	}
	w.run(10, 1, "/dev/t1", "codex", c.first, Busy)
	w.run(11, 1, "/dev/t2", "codex", c.second, Busy)
	w.core.Remotes = &fakeRemotes{boards: []RemoteBoard{{Host: "one", Sessions: []Live{{Session: Session{ID: c.first}}}}}}
	if _, err := w.core.Find(c.prefix); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("ambiguous local prefix fell back to a remote session: %v", err)
	}
}

func TestCodexThreadEnvironmentIdentifiesDetachedAndMismatchedCallers(t *testing.T) {
	id := collidingSessions[0].first
	for _, c := range []struct {
		name, harness, ancestor string
		unreadable              bool
		wantPID                 int
	}{
		{"no session ancestor", "", "", false, 0},
		{"process table unavailable", "", "", true, 0},
		{"codex ancestor agrees", "codex", id, false, 6},
		{"codex ancestor names another thread", "codex", collidingSessions[0].second, false, 0},
		{"codex ancestor has no thread yet", "codex", "", false, 0},
		{"claude ancestor disagrees", "claude", "inherited", false, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			if c.harness != "" {
				w.run(6, 1, "/dev/t1", c.harness, c.ancestor, Busy)
			}
			w.procs = append(w.procs, proc.Proc{PID: 7, PPID: 6, Args: []string{"sh"}})
			if c.unreadable {
				w.core.Procs = func() ([]proc.Proc, error) { return nil, errors.New("process table unavailable") }
			}
			getenv := func(key string) string {
				return map[string]string{"CODEX_THREAD_ID": id, "CLAUDE_CODE_SESSION_ID": "inherited"}[key]
			}
			caller, err := w.core.Calling(7, getenv)
			if err != nil || caller.Harness != "codex" || caller.Session != id || caller.PID != c.wantPID {
				t.Fatalf("caller = %+v, %v", caller, err)
			}
		})
	}
}
