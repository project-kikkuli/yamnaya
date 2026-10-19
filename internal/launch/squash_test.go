package launch

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type hub struct {
	byBranch map[string][]PR
	bySHA    map[string][]PR
	ancestor bool
	calls    int
}

func (h *hub) Merged(_, branch string) ([]PR, error) { h.calls++; return h.byBranch[branch], nil }
func (h *hub) Holding(_, sha string) ([]PR, error)   { h.calls++; return h.bySHA[sha], nil }
func (h *hub) Ancestor(_, _, _ string) (bool, error) { h.calls++; return h.ancestor, nil }

func pr(n int, head string) PR { return PR{Number: n, Head: head, Base: "main"} }

// stacked is pull request n with head, merged into base as commit merge.
func stacked(n int, head, base, merge string) PR {
	p := PR{Number: n, Head: head, Base: base}
	p.Merge.Oid = merge
	return p
}

// parentCommit is a commit on branch parent, off main's root and on no default branch.
func parentCommit(t *testing.T, repo string) string {
	run(t, repo, "git", "branch", "parent", "main~1")
	run(t, repo, "git", "checkout", "-q", "parent")
	run(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "squash of work into parent")
	x := Head(repo)
	run(t, repo, "git", "checkout", "-q", "main")
	return x
}

const elsewhere = "0123456789abcdef0123456789abcdef01234567"

// squashRepo is a repository whose origin claims to be github.com/o/r but pushes to a local bare repository, with a
// worktree on branch feature that has one commit main never got, main having moved on with a squash of it.
func squashRepo(t *testing.T) (repo, wt, head string) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	origin := filepath.Join(base, "origin.git")
	run(t, base, "git", "init", "-q", "--bare", "-b", "main", "origin.git")
	repo = filepath.Join(base, "repo")
	run(t, base, "git", "init", "-q", "-b", "main", "repo")
	run(t, repo, "git", "config", "url."+origin+".insteadOf", "https://github.com/o/r.git")
	run(t, repo, "git", "remote", "add", "origin", "https://github.com/o/r.git")
	run(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "root")
	run(t, repo, "git", "push", "-q", "origin", "main")
	run(t, repo, "git", "remote", "set-head", "origin", "main")
	wt = WorktreePath(repo, "feature")
	run(t, repo, "git", "worktree", "add", "-q", "-b", "feature", wt)
	run(t, wt, "git", "commit", "-q", "--allow-empty", "-m", "work")
	run(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "squash of work")
	run(t, repo, "git", "push", "-q", "origin", "main")
	return repo, wt, Head(wt)
}

func TestGCTreatsSquashMergedWorktreesAsMerged(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, repo, wt, head string) *hub
		keep  string
	}{
		{"pull request head is HEAD", func(t *testing.T, repo, wt, head string) *hub {
			return &hub{byBranch: map[string][]PR{"feature": {pr(7, head)}}}
		}, ""},
		{"pull request head descends from HEAD locally", func(t *testing.T, repo, wt, head string) *hub {
			run(t, wt, "git", "commit", "-q", "--allow-empty", "-m", "review fix")
			later := Head(wt)
			run(t, wt, "git", "reset", "-q", "--hard", head)
			return &hub{byBranch: map[string][]PR{"feature": {pr(7, later)}}}
		}, ""},
		{"pull request head descends from HEAD on GitHub only", func(t *testing.T, repo, wt, head string) *hub {
			return &hub{byBranch: map[string][]PR{"feature": {pr(7, elsewhere)}}, ancestor: true}
		}, ""},
		{"pull request head GitHub says does not descend", func(t *testing.T, repo, wt, head string) *hub {
			return &hub{byBranch: map[string][]PR{"feature": {pr(7, elsewhere)}}}
		}, KeepLocal},
		{"pull request under the upstream branch name", func(t *testing.T, repo, wt, head string) *hub {
			run(t, repo, "git", "config", "branch.feature.merge", "refs/heads/someone/feature")
			return &hub{byBranch: map[string][]PR{"someone/feature": {pr(7, head)}}}
		}, ""},
		{"pull request found only by commit", func(t *testing.T, repo, wt, head string) *hub {
			return &hub{bySHA: map[string][]PR{head: {pr(7, head)}}}
		}, ""},
		{"commits after the merge", func(t *testing.T, repo, wt, head string) *hub {
			run(t, wt, "git", "commit", "-q", "--allow-empty", "-m", "after merge")
			return &hub{byBranch: map[string][]PR{"feature": {pr(7, head)}}}
		}, KeepLocal},
		{"stacked pull request whose merge commit is on main", func(t *testing.T, repo, wt, head string) *hub {
			return &hub{byBranch: map[string][]PR{"feature": {stacked(7, head, "parent", Head(repo))}}}
		}, ""},
		{"stacked pull request whose parent squash-merged", func(t *testing.T, repo, wt, head string) *hub {
			x := parentCommit(t, repo)
			return &hub{byBranch: map[string][]PR{"feature": {stacked(7, head, "parent", x)}, "parent": {pr(8, x)}}}
		}, ""},
		{"stacked pull request whose parent never merged", func(t *testing.T, repo, wt, head string) *hub {
			return &hub{byBranch: map[string][]PR{"feature": {stacked(7, head, "parent", parentCommit(t, repo))}}}
		}, KeepLocal},
		{"no pull request", func(t *testing.T, repo, wt, head string) *hub {
			return &hub{}
		}, KeepLocal},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo, wt, head := squashRepo(t)
			h := c.setup(t, repo, wt, head)
			q := &Squash{Hub: h, Cache: filepath.Join(t.TempDir(), "squash.json"), Now: time.Now}
			g, err := State(wt, q)
			if err != nil {
				t.Fatal(err)
			}
			if ok, why := g.Landable(); ok != (c.keep == "") {
				t.Fatalf("Landable = %v (%s), want %v", ok, why, c.keep == "")
			}

			s, err := GC(repo, time.Now().Add(Idle+time.Hour), nil, q, false, false)
			if err != nil || len(s.Errors) > 0 {
				t.Fatal(err, s.Errors)
			}
			if len(s.Trees) != 1 || s.Trees[0].Keep != c.keep {
				t.Fatalf("decided %+v, want keep %q", s.Trees, c.keep)
			}
			_, statErr := os.Stat(wt)
			if removed := statErr != nil; removed != (c.keep == "") {
				t.Fatalf("worktree removed %v, want keep %q", removed, c.keep)
			}
			if deleted := slices.Contains(s.Branches, "feature"); deleted != (c.keep == "") {
				t.Fatalf("deleted branches %v, want feature deleted %v", s.Branches, c.keep == "")
			}
		})
	}
}

func TestGCDeletesSquashMergedBranchesWithoutWorktrees(t *testing.T) {
	repo, wt, head := squashRepo(t)
	run(t, repo, "git", "worktree", "remove", wt)
	run(t, repo, "git", "branch", "unmerged", "feature")
	run(t, repo, "git", "checkout", "-q", "unmerged")
	run(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "own work")
	run(t, repo, "git", "checkout", "-q", "main")
	h := &hub{byBranch: map[string][]PR{"feature": {pr(7, head)}}}
	q := &Squash{Hub: h, Cache: filepath.Join(t.TempDir(), "squash.json"), Now: time.Now}

	s, err := GC(repo, time.Now(), nil, q, true, false)
	if err != nil || len(s.Errors) > 0 || !slices.Equal(s.Branches, []string{"feature"}) {
		t.Fatalf("dry run deletes %v, %v %v; want feature", s.Branches, err, s.Errors)
	}
	if ok, err := DropMerged(repo, "feature", q); !ok || err != nil {
		t.Fatalf("DropMerged = %v, %v", ok, err)
	}
	if exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/feature").Run() == nil {
		t.Fatal("feature survived")
	}
}

func TestSquashAsksGitHubOncePerCommitAndAgainAfterUnmergedExpires(t *testing.T) {
	for _, c := range []struct {
		name  string
		prs   []PR
		later time.Duration
		again bool
	}{
		{"merged, an hour later", []PR{pr(7, "")}, time.Hour, false},
		{"merged, a week later", []PR{pr(7, "")}, 7 * 24 * time.Hour, false},
		{"unmerged, an hour later", nil, time.Hour, false},
		{"unmerged, past expiry", nil, Unmerged + time.Hour, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo, wt, head := squashRepo(t)
			for i := range c.prs {
				c.prs[i].Head = head
			}
			h := &hub{byBranch: map[string][]PR{"feature": c.prs}}
			cache := filepath.Join(t.TempDir(), "squash.json")
			now := time.Now()
			first := &Squash{Hub: h, Cache: cache, Now: func() time.Time { return now }}
			if _, err := first.Merged(repo, "feature", Head(wt)); err != nil {
				t.Fatal(err)
			}
			asked := h.calls
			second := &Squash{Hub: h, Cache: cache, Now: func() time.Time { return now.Add(c.later) }}
			n, err := second.Merged(repo, "feature", head)
			if err != nil || (n > 0) != (len(c.prs) > 0) {
				t.Fatalf("Merged = %d, %v", n, err)
			}
			if again := h.calls > asked; again != c.again {
				t.Fatalf("asked GitHub again %v, want %v", again, c.again)
			}
		})
	}
}

func TestDiscoverFindsRepositoriesWithLinkedWorktreesOnly(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	repo := func(rel string, linked bool) {
		dir := filepath.Join(base, rel)
		os.MkdirAll(filepath.Dir(dir), 0o755)
		run(t, filepath.Dir(dir), "git", "init", "-q", "-b", "main", filepath.Base(dir))
		run(t, dir, "git", "commit", "-q", "--allow-empty", "-m", "root")
		if linked {
			run(t, dir, "git", "worktree", "add", "-q", "-b", "f", WorktreePath(dir, "f"))
		}
	}
	repo("org/with", true)
	repo("org/without", false)
	repo("deep/a/b/with", true)
	repo("org/with/nested", true)
	repo("app/node_modules/dep", true)
	repo(".hidden/with", true)

	got := Discover(base)
	want := []string{filepath.Join(base, "deep/a/b/with"), filepath.Join(base, "org/with")}
	if !slices.Equal(got, want) {
		t.Fatalf("Discover = %v, want %v", got, want)
	}
	cache := filepath.Join(t.TempDir(), "repos.json")
	now := time.Now()
	if got := Discovered(base, cache, now); !slices.Equal(got, want) {
		t.Fatalf("Discovered = %v", got)
	}
	repo("late/with", true)
	if got := Discovered(base, cache, now.Add(time.Hour)); !slices.Equal(got, want) {
		t.Fatalf("cached Discovered = %v, want %v", got, want)
	}
	if got := Discovered(base, cache, now.Add(Rescan+time.Hour)); len(got) != 3 || !strings.HasSuffix(got[1], "late/with") {
		t.Fatalf("rescanned Discovered = %v", got)
	}
}
