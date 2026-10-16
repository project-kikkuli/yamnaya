package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewPicksItsRepositoryFromTheScannedRoots(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, c := range []struct {
		name   string
		recent string
		keys   []string
		repo   string
		err    string
	}{
		{"enter keeps the selected row's repo", "", []string{"enter"}, "/src/app", ""},
		{"down picks the next, scanned by name", "", []string{"down", "enter"}, "work/api", ""},
		{"down then up returns", "", []string{"down", "down", "up", "enter"}, "work/api", ""},
		{"typing filters by name", "", []string{"web", "enter"}, "work/web", ""},
		{"name matches rank above path matches", "", []string{"w", "down", "enter"}, "work/api", ""},
		{"recent picks follow the selected row's", "solo", []string{"down", "enter"}, "solo", ""},
		{"a ~ path is used as typed", "", []string{"~", "enter"}, home, ""},
		{"an absolute path is used as typed", "", []string{"ROOT/notes/old", "enter"}, "notes/old", ""},
		{"a name matching nothing stays in the picker", "", []string{"telc", "enter"}, "", "no repository matches \"telc\""},
		{"a missing path stays in the picker", "", []string{"/nowhere/at/all", "enter"}, "", "/nowhere/at/all is not a directory"},
		{"down past the last match keeps the last", "", []string{"web", "down", "down", "enter"}, "work/web", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			for _, p := range []string{"work/api/.git", "work/web/.git", "solo/.git", "notes/old"} {
				if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "work", "api-wt-x"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			m, k, _ := setup(t)
			l := &launches{}
			k.Launches = l
			k.Roots = func() []string { return []string{root} }
			if c.recent != "" {
				if err := k.Store.PickRepo(filepath.Join(root, c.recent), time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			m = to(t, place(t, m, k), mission("lead"))
			m = press(t, m, "n", "g", "o", "enter")
			view := m.View()
			for _, want := range []string{"PICK A REPOSITORY", " > app", "api", "solo", "web"} {
				if !strings.Contains(view, want) {
					t.Fatalf("picker lacks %q:\n%s", want, view)
				}
			}
			for _, key := range c.keys {
				if len(key) > 1 && key != "enter" && key != "down" && key != "up" {
					m = press(t, m, strings.Split(strings.Replace(key, "ROOT", root, 1), "")...)
					continue
				}
				m = press(t, m, key)
			}
			if c.err != "" {
				if len(l.started) != 0 || !strings.Contains(m.View(), c.err) || !strings.Contains(m.View(), "PICK A REPOSITORY") {
					t.Fatalf("started %s; picker should stay open saying %q:\n%s", fmtLaunches(l.started), c.err, m.View())
				}
				return
			}
			m = press(t, m, "enter")
			want := c.repo
			if !filepath.IsAbs(want) {
				want = filepath.Join(root, want)
			}
			if len(l.started) != 1 || l.started[0].Repo != want || l.started[0].Prompt != "go" {
				t.Fatalf("started %s, want %s", fmtLaunches(l.started), want)
			}
			recent, _ := k.Store.RecentRepos()
			if strings.HasPrefix(want, root) && (len(recent) == 0 || recent[0] != want) {
				t.Fatalf("picked repo %s should be remembered first: %v", want, recent)
			}
		})
	}
}

func TestPickerScansOnceAndShowsWhatItFoundSoFar(t *testing.T) {
	m, k, _ := setup(t)
	k.Launches = &launches{}
	calls := 0
	k.Roots = func() []string { calls++; return nil }
	m = press(t, m, "n", "esc", "n", "g", "o", "enter")
	if calls != 1 || !m.scanDone {
		t.Fatalf("roots read %d times, scan done %v", calls, m.scanDone)
	}
	m.scanDone = false
	if !strings.Contains(m.repoPickView(), "scanning…") {
		t.Fatalf("an unfinished scan should say so:\n%s", m.repoPickView())
	}
}
