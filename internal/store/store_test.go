package store

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func openTemp(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newProject(t *testing.T, s *Store, name string) {
	t.Helper()
	w := Window{Project: name, Name: "manager", Role: RoleManager, Harness: "claude", Managed: true}
	if err := s.CreateProject(Project{Name: name, Cwd: t.TempDir()}, w, func(Window) (string, error) { return "@0", nil }); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentAddItemAllocatesUniqueIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	newProject(t, openTemp(t, path), "p")
	kinds := []string{KindDecision, KindDefault, KindChore}
	const n = 60
	ids := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := openTemp(t, path)
			it, err := s.AddItem(Item{Project: "p", Kind: kinds[i%3], Question: fmt.Sprintf("q%d", i)})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- it.ID
		}(i)
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	seqs := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
		seqs[id[1:]] = true
	}
	for i := 1; i <= n; i++ {
		if !seqs[fmt.Sprint(i)] {
			t.Fatalf("sequence %d missing; ids %v", i, seen)
		}
	}
}

func TestItemIDsAreNeverReused(t *testing.T) {
	s := openTemp(t, filepath.Join(t.TempDir(), "ym.db"))
	newProject(t, s, "p")
	newProject(t, s, "q")
	first, _ := s.AddItem(Item{Project: "p", Kind: KindDecision, Question: "a"})
	if _, err := s.Dismiss("p", first.ID, "moot"); err != nil {
		t.Fatal(err)
	}
	second, _ := s.AddItem(Item{Project: "p", Kind: KindDecision, Question: "b"})
	other, _ := s.AddItem(Item{Project: "q", Kind: KindChore, Question: "c"})
	if first.ID != "D1" || second.ID != "D2" || other.ID != "C1" {
		t.Fatalf("got %s %s %s, want D1 D2 C1", first.ID, second.ID, other.ID)
	}
}

func TestItemTransitions(t *testing.T) {
	cases := []struct {
		name  string
		steps []string
		want  string
		fails bool
	}{
		{"answer open", []string{"answer"}, ItemAnswered, false},
		{"dismiss open", []string{"dismiss"}, ItemDismissed, false},
		{"snooze then answer", []string{"snooze", "answer"}, ItemAnswered, false},
		{"snooze then reopen", []string{"snooze", "reopen"}, ItemOpen, false},
		{"reopen dismissed", []string{"dismiss", "reopen"}, ItemOpen, false},
		{"answer twice", []string{"answer", "answer"}, "", true},
		{"snooze answered", []string{"answer", "snooze"}, "", true},
		{"reopen open", []string{"reopen"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t, filepath.Join(t.TempDir(), "ym.db"))
			newProject(t, s, "p")
			it, _ := s.AddItem(Item{Project: "p", Kind: KindDefault, Question: "q"})
			var err error
			for _, step := range tc.steps {
				switch step {
				case "answer":
					it, err = s.Answer("p", it.ID, "yes")
				case "dismiss":
					it, err = s.Dismiss("p", it.ID, "no longer relevant")
				case "snooze":
					it, err = s.Snooze("p", it.ID)
				case "reopen":
					it, err = s.Reopen("p", it.ID)
				}
			}
			if tc.fails {
				if err == nil {
					t.Fatalf("want error, item is %s", it.State)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if it.State != tc.want {
				t.Fatalf("state %s, want %s", it.State, tc.want)
			}
		})
	}
}

func TestPhases(t *testing.T) {
	s := openTemp(t, filepath.Join(t.TempDir(), "ym.db"))
	newProject(t, s, "p")
	if _, err := s.SetPhases("p", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	p, err := s.NextPhase("p")
	if err != nil || p.PhaseIndex != 1 {
		t.Fatalf("next: index %d err %v", p.PhaseIndex, err)
	}
	if _, err := s.NextPhase("p"); err == nil {
		t.Fatal("advancing past the last phase should fail")
	}
	p, _ = s.SetPhases("p", []string{"only"})
	if p.PhaseIndex != 0 {
		t.Fatalf("shrinking phases should clamp the index, got %d", p.PhaseIndex)
	}
}

func TestFailedLaunchLeavesNoProject(t *testing.T) {
	s := openTemp(t, filepath.Join(t.TempDir(), "ym.db"))
	w := Window{Project: "p", Name: "manager", Role: RoleManager, Harness: "claude"}
	err := s.CreateProject(Project{Name: "p", Cwd: "/"}, w, func(Window) (string, error) { return "", fmt.Errorf("tmux down") })
	if err == nil {
		t.Fatal("want launch error")
	}
	if _, err := s.Project("p"); err == nil {
		t.Fatal("project row survived a failed launch")
	}
}
