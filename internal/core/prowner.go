package core

import (
	"os/exec"
	"sync"
	"time"
)

type ownerEntry struct {
	own bool
	at  time.Time
}

var (
	ownersMu sync.Mutex
	owners   = map[string]ownerEntry{}
)

// ownsPR reports whether a pull request with status st that a session or crew member in worktree, on branch ahead of
// the default branch, links to is its own: always, unless the pull request's head is a same-named branch it does not own.
func ownsPR(worktree, branch string, ahead int, st PRStatus, now time.Time) bool {
	return worktree == "" || st.Head != branch || st.Merge.OID == "" || branchOwns(worktree, st.Merge.OID, ahead, now)
}

// prPlaces is the worktree, branch and commits ahead of whoever in l links to each of its pull requests.
func (l Live) prPlaces() map[string]Member {
	out := map[string]Member{}
	for _, pr := range l.PRs {
		out[pr.URL] = Member{Worktree: l.Worktree, Branch: l.Branch, Ahead: l.Ahead}
	}
	for _, m := range l.Crew {
		for _, pr := range m.PRs {
			if _, ok := out[pr.URL]; !ok {
				out[pr.URL] = m
			}
		}
	}
	return out
}

// branchOwns reports whether the pull request whose head is commit oid is the work of the branch checked out in worktree,
// not of a same-named branch in another clone: one tip descends from the other, and a tip with nothing of its own past
// the default branch owns nothing. It answers yes when the clone has never seen oid, having nothing to tell them apart by.
func branchOwns(worktree, oid string, ahead int, now time.Time) bool {
	key := worktree + "\x00" + oid
	ownersMu.Lock()
	e, ok := owners[key]
	ownersMu.Unlock()
	if ok && now.Sub(e.at) < gitTTL {
		return e.own
	}
	git := func(args ...string) bool {
		return exec.Command("git", append([]string{"-C", worktree}, args...)...).Run() == nil
	}
	own := !git("cat-file", "-e", oid+"^{commit}") || git("merge-base", "--is-ancestor", oid, "HEAD") ||
		(ahead > 0 && git("merge-base", "--is-ancestor", "HEAD", oid))
	ownersMu.Lock()
	owners[key] = ownerEntry{own, now}
	ownersMu.Unlock()
	return own
}
