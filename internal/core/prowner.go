package core

import (
	"os/exec"
	"strings"
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

// ownsPR checks the linked pull request against the session's upstream and commit history.
func ownsPR(worktree, branch string, ahead int, st PRStatus, now time.Time) bool {
	return worktree == "" || st.Merge.OID == "" || branchOwns(worktree, branch, st.Head, st.Merge.OID, ahead, now)
}

// prPlaces is the worktree, branch and commits ahead of whoever in l links to each of its pull requests.
func (l Live) prPlaces() map[string]Member {
	out := map[string]Member{}
	for _, pr := range l.PRs {
		out[pr.URL] = Member{Worktree: l.Worktree, Branch: l.Branch, Ahead: l.Ahead, Facts: l.Facts}
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

// branchOwns accepts the PR's upstream or related history, including a head this clone has not fetched.
func branchOwns(worktree, branch, head, oid string, ahead int, now time.Time) bool {
	key := worktree + "\x00" + branch + "\x00" + head + "\x00" + oid
	ownersMu.Lock()
	e, ok := owners[key]
	ownersMu.Unlock()
	if ok && now.Sub(e.at) < gitTTL {
		return e.own
	}
	git := func(args ...string) bool {
		return exec.Command("git", append([]string{"-C", worktree}, args...)...).Run() == nil
	}
	config := func(key string) string {
		out, _ := exec.Command("git", "-C", worktree, "config", "--get", "branch."+branch+"."+key).Output()
		return strings.TrimSpace(string(out))
	}
	own := config("remote") == "origin" && config("merge") == "refs/heads/"+head || !git("cat-file", "-e", oid+"^{commit}") || git("merge-base", "--is-ancestor", oid, "HEAD") ||
		(ahead > 0 && git("merge-base", "--is-ancestor", "HEAD", oid))
	ownersMu.Lock()
	owners[key] = ownerEntry{own, now}
	ownersMu.Unlock()
	return own
}
