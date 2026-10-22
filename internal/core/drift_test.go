package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/drift"
)

func looping(since time.Time) drift.Sign {
	return drift.Sign{Kind: drift.Looping, Count: 6, Since: since, At: since.Add(9 * time.Minute),
		Evidence: "failed the same way 6 times in 9m: Bash: Exit code 143 Command timed out after 2m 0s"}
}

func adriftEvents(t *testing.T, w *orgWorld, goal string) []string {
	t.Helper()
	events, err := w.core.Store.GoalEvents(goal)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		if e.Kind == EventAgentAdrift {
			out = append(out, e.Payload)
		}
	}
	return out
}

func heardAdrift(w *orgWorld, session string) []string {
	var out []string
	for _, text := range w.handedTo(session) {
		if strings.Contains(text, "looks adrift") {
			out = append(out, text)
		}
	}
	return out
}

func TestADriftingAgentWakesItsManagerOncePerEpisode(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "make the importer handle CSV", "/repo/app")
	w.spawn("agent", m.Session)
	w.spawn("mine", "")
	at := w.clock.Add(-10 * time.Minute)
	for _, id := range []string{"agent", "mine"} {
		w.set(id, func(s *Session) { s.Status, s.Drift = Busy, []drift.Sign{looping(at)} })
	}
	w.loop(t, 3)
	events := adriftEvents(t, w, g.ID)
	if len(events) != 1 || !strings.Contains(events[0], "agent agent") || !strings.Contains(events[0], "looping: failed the same way 6 times") {
		t.Fatalf("goal events %q, want the agent's evidence once", events)
	}
	if heard := heardAdrift(w, m.Session); len(heard) != 1 {
		t.Fatalf("manager heard %q, want one wake with the evidence", heard)
	}
	if n, ok := w.board(t).Node(m.ID); !ok || n.Standing != StandAdrift {
		t.Fatalf("goal stands %q, want %q", n.Standing, StandAdrift)
	}

	w.set("agent", func(s *Session) {
		s.Drift = []drift.Sign{looping(at), looping(at.Add(5 * time.Minute))}
		s.Crew = []Member{{ID: "c1", Name: "tester", Status: Busy, Facts: Facts{Drift: []drift.Sign{{Kind: drift.Compacting, Since: at, Evidence: "context compacted 4 times in 3m"}}}}}
	})
	w.loop(t, 3)
	events = adriftEvents(t, w, g.ID)
	if len(events) != 3 || !strings.Contains(strings.Join(events, "\n"), "crew member tester of agent agent") {
		t.Fatalf("goal events %q, want the agent's new episode and its crew member's sign added, each once", events)
	}

	w.set("agent", func(s *Session) { s.Drift, s.Crew = nil, nil })
	if n, _ := w.board(t).Node(m.ID); n.Standing == StandAdrift {
		t.Fatalf("goal still adrift after its agent recovered")
	}
}

func TestAChiefHearsWhenAManagerUnderItIsAdrift(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the exporter", "/repo/app")
	c := w.chief(t, "platform", m.ID)
	w.loop(t, 1)
	w.set(m.Session, func(s *Session) { s.Status, s.Drift = Busy, []drift.Sign{looping(w.clock.Add(-10 * time.Minute))} })
	w.loop(t, 3)
	if events := adriftEvents(t, w, g.ID); len(events) != 1 || !strings.Contains(events[0], "manager "+m.ID) {
		t.Fatalf("goal events %q, want the manager's evidence once", events)
	}
	chief, _ := w.core.Store.Manager(c.ID)
	if heard := heardAdrift(w, chief.Session); len(heard) == 0 {
		t.Fatalf("chief heard %q, want the manager's evidence", w.handedTo(chief.Session))
	}
}

func TestWritesInAnotherRepositoryShowAsOffRepo(t *testing.T) {
	root := t.TempDir()
	home, other, sibling, clone := filepath.Join(root, "app"), filepath.Join(root, "lib"), filepath.Join(root, "app-wt-fix"), filepath.Join(root, "app-copy")
	for _, d := range []string{home, other, sibling, clone} {
		if err := os.MkdirAll(filepath.Join(d, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	repos := map[string]GitInfo{
		home:    {Top: home, Common: filepath.Join(home, ".git"), Origin: "github.com/me/app"},
		sibling: {Top: sibling, Common: filepath.Join(home, ".git"), Origin: "github.com/me/app"},
		clone:   {Top: clone, Common: filepath.Join(clone, ".git"), Origin: "github.com/me/app"},
		other:   {Top: other, Common: filepath.Join(other, ".git"), Origin: "github.com/me/lib"},
	}
	in := func(dir string, names ...string) []string {
		var out []string
		for _, n := range names {
			out = append(out, filepath.Join(dir, "src", n))
		}
		return out
	}
	cases := []struct {
		name   string
		status Status
		files  []string
		want   bool
	}{
		{"three files in another repository", Busy, append(in(home, "a.go"), in(other, "x.go", "y.go", "z.go")...), true},
		{"two files is not enough", Busy, in(other, "x.go", "y.go"), false},
		{"another worktree of its own repository", Busy, in(sibling, "x.go", "y.go", "z.go"), false},
		{"another clone of its own repository", Busy, in(clone, "x.go", "y.go", "z.go"), false},
		{"files outside any repository", Busy, in(root, "x.go", "y.go", "z.go"), false},
		{"a session that ended", Done, in(other, "x.go", "y.go", "z.go"), false},
	}
	for _, c := range cases {
		w := newWorld(t)
		w.core.Git = func(dir string) GitInfo { return repos[dir] }
		w.run(10, 1, "/dev/t1", "claude", "s", c.status)
		w.claude.sessions[10] = Session{ID: "s", Status: c.status, Cwd: home, Facts: Facts{Files: c.files}}
		b := w.board(t)
		var signs []drift.Sign
		for _, s := range b.Sessions {
			signs = append(signs, s.Drift...)
		}
		got := len(signs) == 1 && signs[0].Kind == drift.OffRepo && strings.Contains(signs[0].Evidence, "wrote 3 files in lib")
		if got != c.want {
			t.Errorf("%s: signs %v, want off-repo %v", c.name, signs, c.want)
		}
	}
}
