package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBigNSkipsThePickerAndNOpensIt(t *testing.T) {
	for _, c := range []struct {
		key    string
		picker bool
		scans  int
	}{
		{"N", false, 0},
		{"n", true, 1},
	} {
		t.Run(c.key, func(t *testing.T) {
			m, k, _ := setup(t)
			k.Launches = &launches{}
			calls := 0
			k.Roots = func() []string { calls++; return nil }
			m = press(t, to(t, place(t, m, k), mission("lead")), c.key)
			if picking := m.mode == modeInput && m.inKind == inNewRepo; picking != c.picker || calls != c.scans {
				t.Fatalf("%s: picker %v, roots read %d times", c.key, picking, calls)
			}
		})
	}
}

func TestNPicksTheGoalsRepositoryFromTheScannedRoots(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, c := range []struct {
		name   string
		recent string
		keys   []string
		repo   string
		err    string
	}{
		{"enter picks the first, scanned by name", "", []string{"enter"}, "work/api", ""},
		{"down picks the next", "", []string{"down", "enter"}, "solo", ""},
		{"down then up returns", "", []string{"down", "down", "up", "enter"}, "solo", ""},
		{"typing filters by name", "", []string{"web", "enter"}, "work/web", ""},
		{"name matches rank above path matches", "", []string{"w", "down", "enter"}, "work/api", ""},
		{"recent picks come first", "solo", []string{"enter"}, "solo", ""},
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
			m = press(t, m, "n")
			view := m.View().Content
			for _, want := range []string{"PICK A REPOSITORY", "api", "solo", "web"} {
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
				if len(l.started) != 0 || !strings.Contains(m.View().Content, c.err) || !strings.Contains(m.View().Content, "PICK A REPOSITORY") {
					t.Fatalf("started %s; picker should stay open saying %q:\n%s", fmtLaunches(l.started), c.err, m.View().Content)
				}
				return
			}
			press(t, m, "g", "o", "enter", "enter")
			want := c.repo
			if !filepath.IsAbs(want) {
				want = filepath.Join(root, want)
			}
			if len(l.started) != 1 || l.started[0].Repo != want || !strings.Contains(l.started[0].Prompt, "\ngo\n") {
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
	m = press(t, m, "n", "esc", "n")
	if calls != 1 || !m.scanDone {
		t.Fatalf("roots read %d times, scan done %v", calls, m.scanDone)
	}
	m.scanDone = false
	if !strings.Contains(m.repoPickView(), "scanning…") {
		t.Fatalf("an unfinished scan should say so:\n%s", m.repoPickView())
	}
}

func TestNPicksTheGoalsRepositoryFirst(t *testing.T) {
	for _, c := range []struct {
		name   string
		keys   []string
		repo   string
		prompt string
		harn   string
		starts bool
	}{
		{"pick then prompt launches", []string{"enter", "go", "enter"}, "api", "go", "claude", true},
		{"filter picks another repo", []string{"we", "enter", "go", "enter"}, "web", "go", "claude", true},
		{"esc at the picker", []string{"esc"}, "", "", "", false},
		{"esc at the prompt", []string{"enter", "go", "esc"}, "", "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			for _, r := range []string{"api", "app", "web"} {
				if err := os.MkdirAll(filepath.Join(root, r, ".git"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			m, k, _ := setup(t)
			l := &launches{}
			k.Launches = l
			k.Roots = func() []string { return []string{root} }
			if err := k.Store.PickRepo(filepath.Join(root, "api"), time.Now()); err != nil {
				t.Fatal(err)
			}
			m = to(t, place(t, m, k), mission("lead"))
			m = press(t, m, "n")
			view := m.View().Content
			if !strings.Contains(view, "PICK A REPOSITORY") || m.newL.Repo != "" || len(m.picks) == 0 || m.picks[0] != filepath.Join(root, "api") {
				t.Fatalf("n should open the picker with recents first and nothing preselected (repo %q, picks %v):\n%s", m.newL.Repo, m.picks, view)
			}
			for _, key := range c.keys {
				if len(key) > 1 && key != "enter" && key != "esc" && key != "tab" {
					m = press(t, m, strings.Split(key, "")...)
					continue
				}
				m = press(t, m, key)
			}
			if !c.starts {
				if len(l.started) != 0 || m.mode != modeList {
					t.Fatalf("started %s in mode %v", fmtLaunches(l.started), m.mode)
				}
				return
			}
			if len(l.started) != 1 {
				t.Fatalf("started %s", fmtLaunches(l.started))
			}
			got := l.started[0]
			real, _ := filepath.EvalSymlinks(root)
			if filepath.Join(real, c.repo) != got.Repo && filepath.Join(root, c.repo) != got.Repo || !strings.Contains(got.Prompt, "\n"+c.prompt+"\n") || got.Harness != c.harn || !strings.HasPrefix(got.Slug, "mgr1-") {
				t.Fatalf("started %s", fmtLaunches(l.started))
			}
		})
	}
}
