package core

import (
	"os/exec"
	"strings"
	"sync"
	"time"
)

type PRDiscovery interface {
	PRsForBranches([]PRBranch) (map[PRBranch][]PR, error)
}

type prDiscovery struct {
	mu      sync.Mutex
	checked map[PRBranch]time.Time
	targets map[string]prTarget
}

type prTarget struct {
	branch string
	at     time.Time
}

func (c *Core) prBranches(worktree, branch string, facts Facts) []PRBranch {
	out := append([]PRBranch(nil), facts.Pushes...)
	repo := GitHubRepo(worktree)
	if repo == "" || branch == "" || branch == "HEAD" {
		return out
	}
	if !DefaultBranch(branch) {
		out = append(out, PRBranch{Repo: repo, Head: branch})
	}
	key := worktree + "\x00" + branch
	c.prDiscovery.mu.Lock()
	target, ok := c.prDiscovery.targets[key]
	c.prDiscovery.mu.Unlock()
	if !ok || c.now().Sub(target.at) >= gitTTL {
		config := func(k string) string {
			out, _ := exec.Command("git", "-C", worktree, "config", "--get", "branch."+branch+"."+k).Output()
			return strings.TrimSpace(string(out))
		}
		target = prTarget{at: c.now()}
		if config("remote") == "origin" {
			target.branch = strings.TrimPrefix(config("merge"), "refs/heads/")
		}
		c.prDiscovery.mu.Lock()
		if c.prDiscovery.targets == nil {
			c.prDiscovery.targets = map[string]prTarget{}
		}
		c.prDiscovery.targets[key] = target
		c.prDiscovery.mu.Unlock()
	}
	if target.branch != "" && target.branch != branch {
		out = append(out, PRBranch{Repo: repo, Head: target.branch})
	}
	return out
}

func pushedPR(f Facts, pr PR, st PRStatus) bool {
	for _, b := range f.Pushes {
		if b.Repo == pr.Repo && b.Head == st.Head {
			return true
		}
	}
	return false
}

func (c *Core) discoverPRs(b Board) error {
	source, ok := c.PRs.(PRDiscovery)
	if !ok {
		return nil
	}
	interval := PRStale
	if bud, ok := c.PRs.(PRBudget); ok {
		if left, reset, known := bud.Budget(); known && c.now().Before(reset) {
			interval = prStale(left, true)
			if interval == 0 {
				return nil
			}
		}
	}
	var due []PRBranch
	seen := map[PRBranch]bool{}
	for _, s := range b.Sessions {
		if _, managed := b.Org.Managed(s.Key(), b.parents); !managed {
			continue
		}
		members := append([]Member{{Worktree: s.Worktree, Branch: s.Branch, Facts: s.Facts}}, s.Crew...)
		for _, m := range members {
			for _, branch := range c.prBranches(m.Worktree, m.Branch, m.Facts) {
				c.prDiscovery.mu.Lock()
				last := c.prDiscovery.checked[branch]
				c.prDiscovery.mu.Unlock()
				if !seen[branch] && c.now().Sub(last) >= interval {
					seen[branch] = true
					due = append(due, branch)
				}
			}
		}
	}
	for start := 0; start < len(due); start += PRBatchSize {
		chunk := due[start:min(start+PRBatchSize, len(due))]
		found, err := source.PRsForBranches(chunk)
		if err != nil {
			return err
		}
		c.prDiscovery.mu.Lock()
		if c.prDiscovery.checked == nil {
			c.prDiscovery.checked = map[PRBranch]time.Time{}
		}
		for _, branch := range chunk {
			if _, read := found[branch]; read {
				c.prDiscovery.checked[branch] = c.now()
			}
		}
		c.prDiscovery.mu.Unlock()
		for destination, prs := range found {
			for _, pr := range prs {
				for i := range b.Sessions {
					s := &b.Sessions[i]
					if _, managed := b.Org.Managed(s.Key(), b.parents); !managed {
						continue
					}
					add := func(f *Facts, worktree, branch string) {
						for _, target := range c.prBranches(worktree, branch, *f) {
							if target == destination {
								f.Opened(pr)
								break
							}
						}
					}
					add(&s.Facts, s.Worktree, s.Branch)
					for j := range s.Crew {
						m := &s.Crew[j]
						add(&m.Facts, m.Worktree, m.Branch)
					}
				}
			}
		}
	}
	return nil
}
