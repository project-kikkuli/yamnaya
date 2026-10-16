package progress

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"unicode/utf8"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func live(key string, st core.Status, branch string, ahead int, crew ...core.Member) core.Live {
	return core.Live{Session: core.Session{ID: key, Status: st, Branch: branch, Ahead: ahead, Crew: crew}}
}

func member(st core.Status, cut bool) core.Member { return core.Member{Status: st, Cut: cut} }

func TestFractionCountsFinishedCrewAndPassedGates(t *testing.T) {
	merged := core.PR{URL: "u1", Repo: "app", Number: 1, Status: &core.PRStatus{State: "MERGED", Head: "a"}}
	red := core.PR{URL: "u2", Repo: "app", Number: 2, Status: &core.PRStatus{State: "OPEN", Head: "b", Failed: 1, Passed: 3, Unresolved: 1, Threads: 2}}
	cases := []struct {
		name  string
		p     core.Project
		want  float64
		ok    bool
		crew  Crew
		units int
	}{
		{"nothing to measure", core.Project{Key: "root", Sessions: []core.Live{live("root", core.Busy, "main", 0)}}, 0, false, Crew{}, 0},
		{"one of two crew finished, the two cut ones left out", core.Project{Key: "root", Sessions: []core.Live{live("root", core.Busy, "", 0,
			member(core.Busy, false), member(core.Done, true), member(core.Done, false), member(core.Waiting, true))}},
			0.5, true, Crew{Finished: 1, Cut: 2, Dispatched: 2}, 0},
		{"idle teammates count finished, spawned sessions and exited ones count as crew", core.Project{Key: "root",
			Sessions: []core.Live{live("root", core.Busy, "", 0, member(core.Idle, false)), live("kid", core.Busy, "", 0)},
			Dormant:  []core.Dormant{{Key: "gone", Session: "gone"}}},
			2.0 / 3, true, Crew{Finished: 2, Dispatched: 3}, 0},
		{"a merged PR and a branch with commits", core.Project{Key: "root", Sessions: []core.Live{
			func() core.Live { l := live("root", core.Busy, "c", 4); l.PRs = []core.PR{merged}; return l }()}},
			8.0 / 12, true, Crew{}, 2},
		{"a red PR with an open thread, its branch not counted twice", core.Project{Key: "root", Sessions: []core.Live{
			func() core.Live {
				l := live("root", core.Busy, "b", 2, member(core.Done, false))
				l.Crew[0].PRs = []core.PR{red}
				return l
			}()}},
			4.0 / 7, true, Crew{Finished: 1, Dispatched: 1}, 1},
	}
	for _, c := range cases {
		m := Of(c.p)
		f, ok := m.Fraction()
		if ok != c.ok || (ok && abs(f-c.want) > 1e-9) || m.Crew != c.crew || len(m.Units) != c.units {
			t.Errorf("%s: fraction %v %v, crew %+v, units %d; want %v %v, %+v, %d (%s)", c.name, f, ok, m.Crew, len(m.Units), c.want, c.ok, c.crew, c.units, m.Parts())
		}
	}
}

func TestFractionOverCrewAndGateMixes(t *testing.T) {
	green := core.PR{URL: "g", Repo: "app", Number: 5, Status: &core.PRStatus{State: "OPEN", Head: "g", Passed: 3}}
	withPR := func(crew ...core.Member) core.Live {
		l := live("root", core.Busy, "", 0, crew...)
		l.PRs = []core.PR{green}
		return l
	}
	done, busy, waiting, cut := member(core.Done, false), member(core.Busy, false), member(core.Waiting, false), member(core.Done, true)
	cases := []struct {
		name string
		lead core.Live
		want float64
		ok   bool
	}{
		{"all crew finished", live("root", core.Busy, "", 0, done, done), 1, true},
		{"busy and waiting crew hold it back", live("root", core.Busy, "", 0, done, busy, waiting), 1.0 / 3, true},
		{"cut crew neither inflate nor block", live("root", core.Busy, "", 0, done, cut, cut), 1, true},
		{"cut crew beside busy crew", live("root", core.Busy, "", 0, busy, cut), 0, true},
		{"only cut crew leaves nothing to measure", live("root", core.Busy, "", 0, cut), 0, false},
		{"a green PR with no crew", withPR(), 5.0 / 6, true},
		{"a green PR, finished and cut crew", withPR(done, cut), 6.0 / 7, true},
		{"a green PR, busy crew", withPR(busy, waiting), 5.0 / 8, true},
	}
	for _, c := range cases {
		m := Of(core.Project{Key: "root", Sessions: []core.Live{c.lead}})
		f, ok := m.Fraction()
		if ok != c.ok || (ok && abs(f-c.want) > 1e-9) {
			t.Errorf("%s: %v %v, want %v %v (%s)", c.name, f, ok, c.want, c.ok, m.Parts())
		}
	}
}

func TestPhaseIsALabelAndNeverMovesTheBar(t *testing.T) {
	p := core.Project{Key: "root", Sessions: []core.Live{live("root", core.Busy, "", 0, member(core.Done, false), member(core.Busy, false))}}
	before, _ := Of(p).Fraction()
	p.Phases = core.Phases{Names: []string{"plan", "build", "ship"}, Current: 2}
	m := Of(p)
	after, _ := m.Fraction()
	if before != after || m.Phase != "ship" {
		t.Errorf("phase moved the bar %v -> %v or lost its label %q", before, after, m.Phase)
	}
}

func TestPRGates(t *testing.T) {
	cases := []struct {
		name   string
		st     *core.PRStatus
		passed int
		strip  string
		closed bool
	}{
		{"not fetched", nil, 3, "✓ branch  ✓ commits  ✓ pr  · ci  · threads  · merged", false},
		{"fetch failed", &core.PRStatus{State: "UNKNOWN"}, 3, "✓ branch  ✓ commits  ✓ pr  · ci  · threads  · merged", false},
		{"ci running", &core.PRStatus{State: "OPEN", Passed: 2, Pending: 1}, 4, "✓ branch  ✓ commits  ✓ pr  · ci  ✓ threads  · merged", false},
		{"ci failing, thread open", &core.PRStatus{State: "OPEN", Passed: 2, Failed: 1, Threads: 1, Unresolved: 1}, 3, "✓ branch  ✓ commits  ✓ pr  ✗ ci  · threads  · merged", false},
		{"green and resolved", &core.PRStatus{State: "OPEN", Passed: 5, Threads: 3}, 5, "✓ branch  ✓ commits  ✓ pr  ✓ ci  ✓ threads  · merged", false},
		{"no checks at all", &core.PRStatus{State: "OPEN"}, 5, "✓ branch  ✓ commits  ✓ pr  ✓ ci  ✓ threads  · merged", false},
		{"merged with threads left", &core.PRStatus{State: "MERGED", Unresolved: 2}, 6, "✓ branch  ✓ commits  ✓ pr  ✓ ci  ✓ threads  ✓ merged", false},
		{"closed unmerged", &core.PRStatus{State: "CLOSED"}, 3, "✓ branch  ✓ commits  ✓ pr  - ci  - threads  - merged", true},
	}
	for _, c := range cases {
		u := PRUnit(core.PR{URL: "u", Repo: "app", Number: 3, Status: c.st})
		if u.Passed() != c.passed || Strip(u) != c.strip || u.Closed != c.closed {
			t.Errorf("%s: passed %d strip %q closed %v", c.name, u.Passed(), Strip(u), u.Closed)
		}
	}
}

func TestBranchGates(t *testing.T) {
	cases := []struct {
		ahead, passed int
	}{{0, 1}, {3, 2}}
	for _, c := range cases {
		if u := BranchUnit("x", c.ahead); u.Passed() != c.passed {
			t.Errorf("%d commits ahead: passed %d, want %d", c.ahead, u.Passed(), c.passed)
		}
	}
}

func TestBarFillsItsWidth(t *testing.T) {
	cases := []struct {
		f     float64
		width int
		want  string
	}{
		{0, 4, "····"},
		{1, 4, "████"},
		{0.5, 4, "██··"},
		{0.3, 4, "█▎··"},
		{1.7, 3, "███"},
		{-1, 3, "···"},
		{0.5, 0, ""},
	}
	for _, c := range cases {
		got := Bar(c.f, c.width)
		if got != c.want || utf8.RuneCountInString(got) != c.width {
			t.Errorf("Bar(%v, %d) = %q, want %q", c.f, c.width, got, c.want)
		}
	}
}

func abs(f float64) float64 { return max(f, -f) }

func repoAt(t *testing.T, defaultBranch, checkout string, commits int) core.GitInfo {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", defaultBranch)
	git("commit", "-q", "--allow-empty", "-m", "base")
	git("update-ref", "refs/remotes/origin/"+defaultBranch, "HEAD")
	git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+defaultBranch)
	if checkout != defaultBranch {
		git("checkout", "-q", "-b", checkout)
		for range commits {
			git("commit", "-q", "--allow-empty", "-m", "work")
		}
	}
	return core.ReadGit(dir)
}

func inRepo(g core.GitInfo) core.Live {
	return core.Live{Session: core.Session{Repo: g.Name(), RepoKey: g.Common, Branch: g.Branch, Ahead: g.Ahead, Default: g.Default}}
}

func TestBranchUnitsAreKeyedByRepoAndSkipTheDefaultBranch(t *testing.T) {
	cases := []struct {
		name  string
		repos []struct{ def, checkout string }
		units int
	}{
		{"same branch name in two repos", []struct{ def, checkout string }{{"main", "feat"}, {"main", "feat"}}, 2},
		{"same branch name twice in one repo is one unit", nil, 1},
		{"develop default checked out", []struct{ def, checkout string }{{"develop", "develop"}}, 0},
		{"develop default beside a feature in another repo", []struct{ def, checkout string }{{"develop", "develop"}, {"main", "feat"}}, 1},
		{"main checked out", []struct{ def, checkout string }{{"main", "main"}}, 0},
	}
	for _, c := range cases {
		var lives []core.Live
		for _, r := range c.repos {
			lives = append(lives, inRepo(repoAt(t, r.def, r.checkout, 2)))
		}
		if c.repos == nil {
			l := inRepo(repoAt(t, "main", "feat", 2))
			lives = []core.Live{l, l}
		}
		if got := Units(lives); len(got) != c.units {
			t.Errorf("%s: %d units, want %d", c.name, len(got), c.units)
		}
	}
}

func TestCrewBranchUnitsCarryTheirOwnRepo(t *testing.T) {
	a, b := repoAt(t, "main", "feat", 1), repoAt(t, "develop", "feat", 3)
	l := inRepo(a)
	l.Crew = []core.Member{{Branch: b.Branch, Ahead: b.Ahead, RepoKey: b.Common, Default: b.Default}}
	if got := Units([]core.Live{l}); len(got) != 2 {
		t.Errorf("%d units, want 2", len(got))
	}
}
