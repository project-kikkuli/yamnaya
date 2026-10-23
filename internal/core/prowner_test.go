package core

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func headOf(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// TestAPullRequestOnASameNamedBranchOfAnotherCloneIsNotTheSessions builds the clones the bug came from: two clones of one
// origin each with a local branch x, one of which pushed x and opened a pull request from it.
func TestAPullRequestOnASameNamedBranchOfAnotherCloneIsNotTheSessions(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	origin := filepath.Join(root, "origin.git")
	git(t, root, "init", "-q", "--bare", "-b", "main", origin)
	seed := clone(t, root, "seed", origin)
	git(t, seed, "push", "-q", "origin", "main")
	repo := func(dir string) string {
		path := filepath.Join(root, dir)
		git(t, root, "clone", "-q", origin, path)
		return path
	}
	opener, other, fresh, unseen, rebased := repo("opener"), repo("other"), repo("fresh"), repo("unseen"), repo("rebased")
	elsewhere, untracked, fork := repo("tracks-another-branch"), repo("untracked"), repo("tracks-a-fork")
	git(t, opener, "switch", "-q", "-c", "x")
	git(t, opener, "commit", "-q", "--allow-empty", "-m", "opener's work")
	git(t, opener, "push", "-q", "origin", "x")
	prHead := headOf(t, opener)
	for _, dir := range []string{other, fresh} {
		git(t, dir, "fetch", "-q", "origin")
		git(t, dir, "switch", "-q", "-c", "x", "origin/main")
	}
	git(t, other, "commit", "-q", "--allow-empty", "-m", "other work")
	git(t, rebased, "fetch", "-q", "origin")
	git(t, rebased, "switch", "-q", "x")
	git(t, rebased, "reset", "-q", "--hard", "origin/main")
	git(t, rebased, "commit", "-q", "--allow-empty", "-m", "opener's work, amended before its force-push")
	for _, dir := range []string{elsewhere, untracked, fork} {
		git(t, dir, "fetch", "-q", "origin")
		git(t, dir, "switch", "-q", "--no-track", "-c", "x", "origin/main")
		git(t, dir, "commit", "-q", "--allow-empty", "-m", "unrelated work on a same-named branch")
	}
	git(t, elsewhere, "config", "branch.x.remote", "origin")
	git(t, elsewhere, "config", "branch.x.merge", "refs/heads/add-consonants-flag")
	git(t, fork, "remote", "add", "fork", origin)
	git(t, fork, "config", "branch.x.remote", "fork")
	git(t, fork, "config", "branch.x.merge", "refs/heads/x")
	git(t, unseen, "switch", "-q", "-c", "x")
	git(t, unseen, "commit", "-q", "--allow-empty", "-m", "unseen work")

	const url = "https://github.com/me/app/pull/28"
	w := newOrgWorld(t)
	w.core.Git = ReadGit
	g, m := w.goal(t, "count consonants", opener)
	sessions := []struct {
		id, dir string
		owns    bool
	}{
		{"opener", opener, true},
		{"other-clone", other, false},
		{"fresh-branch", fresh, false},
		{"never-fetched", unseen, true},
		{"rebased-since-push", rebased, true},
		{"tracks-another-branch", elsewhere, false},
		{"untracked-unrelated", untracked, false},
		{"tracks-a-fork", fork, false},
	}
	for _, s := range sessions {
		w.spawn(s.id, m.Session)
		w.set(s.id, func(x *Session) { x.Cwd, x.PRs = s.dir, []PR{{URL: url}} })
	}
	if err := w.core.Store.SavePRStatus(url, PRStatus{State: "OPEN", Head: "x", Merge: PRMergeFacts{OID: prHead}, Fetched: w.clock}); err != nil {
		t.Fatal(err)
	}
	b := w.board(t)
	for _, s := range sessions {
		l, _ := b.Session(s.id)
		if got := len(l.PRs) == 1; got != s.owns {
			t.Errorf("%s links the pull request: %v, want %v", s.id, got, s.owns)
		}
	}
	if err := w.core.Notice(b); err != nil {
		t.Fatal(err)
	}
	evs, err := w.core.Store.GoalEvents(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	var opened []string
	for _, e := range evs {
		if e.Kind == EventPROpened {
			opened = append(opened, e.Payload)
		}
	}
	if len(opened) != 1 || strings.Contains(opened[0], "other-clone") || strings.Contains(opened[0], "fresh-branch") {
		t.Errorf("pr-opened events %q, want one, from a session that owns the branch", opened)
	}
}
