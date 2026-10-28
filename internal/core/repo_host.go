package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RepoHost is the other host that holds repo when this one does not: the host whose board shows a goal, manager, chief or session
// in it. ok is false for a repository on this host, or one no other host shows.
func (c *Core) RepoHost(repo string) (host string, ok bool) {
	if c.Remotes == nil || repoHere(repo) {
		return "", false
	}
	return HostHolding(c.Remotes.Boards(), repo)
}

// HostHolding is RepoHost read from boards already in hand.
func HostHolding(boards []RemoteBoard, repo string) (string, bool) {
	if repoHere(repo) {
		return "", false
	}
	repo = repoDir(repo)
	for _, r := range boards {
		if remoteHolds(r, repo) {
			return r.Host, true
		}
	}
	return "", false
}

func repoHere(repo string) bool {
	if repo == "" {
		return true
	}
	_, err := os.Stat(repo)
	return err == nil
}

func remoteHolds(r RemoteBoard, repo string) bool {
	in := func(p string) bool {
		p = repoDir(p)
		return p != "" && (p == repo || strings.HasPrefix(p, repo+string(filepath.Separator)))
	}
	for _, g := range r.Goals {
		if in(g.Repo) {
			return true
		}
	}
	for _, m := range r.Managers {
		if in(m.Repo) {
			return true
		}
	}
	for _, s := range r.Sessions {
		if in(s.RepoKey) || in(s.Cwd) {
			return true
		}
	}
	for _, d := range r.Dormant {
		if in(d.RepoKey) {
			return true
		}
	}
	return false
}

func repoDir(p string) string {
	if p == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Clean(p), string(filepath.Separator)+".git")
}

// goalArgs is the ym command that starts a goal for ask in repo on another host.
func goalArgs(ask, repo, harness, profile string) []string {
	args := []string{"goal", "--repo", repo}
	if harness == "codex" {
		args = append(args, "--codex")
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	return append(args, "--", ask)
}

// chiefArgs is the ym command that starts a chief named name over children of this host in repo on another host.
func (c *Core) chiefArgs(name, remit, repo, harness, profile string, children []string) []string {
	args := []string{"chief", "--repo", repo}
	if harness == "codex" {
		args = append(args, "--codex")
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	if remit != "" {
		args = append(args, "--remit", remit)
	}
	var over []string
	for _, id := range children {
		if _, _, remote := SplitRemote(id); remote {
			over = append(over, id)
		} else {
			over = append(over, RemoteKey(c.SelfName(), id))
		}
	}
	if len(over) > 0 {
		args = append(args, "--over", strings.Join(over, ","))
	}
	return append(args, "--", name)
}

// handOff moves m, recorded here though its repository is on host, to host: its goal or chief starts there, and this copy is
// retired, its goal dropped, so the goal has the one host that holds its code.
func (c *Core) handOff(o Org, m Manager, host string) Manager {
	g, hasGoal := o.Goals[m.GoalID]
	var args []string
	if hasGoal {
		args = goalArgs(g.Ask, m.Repo, m.Harness, "")
	} else {
		remit := ""
		if body, err := os.ReadFile(m.BriefPath); err == nil {
			remit = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(body)), "# "+m.Name))
		}
		var children []string
		for _, r := range o.Managers {
			if r.ParentID == m.ID && !r.Retired && r.Host == "" {
				children = append(children, r.ID)
			}
		}
		args = c.chiefArgs(m.Name, remit, m.Repo, m.Harness, "", children)
	}
	out, err := c.RunOrRelay(host, args...)
	if err != nil {
		m.LastError = fmt.Sprintf("%s is on %s; starting it there failed: %v", m.Repo, host, err)
		c.Store.SaveManager(m)
		return m
	}
	first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	m.Retired, m.Failures, m.NextAttempt, m.LastError = true, 0, timeZero, ""
	c.Store.SaveManager(m)
	m.Host = host
	if hasGoal {
		g.State, g.Evidence, g.DoneAt, g.AckedAt = GoalDropped, "moved to "+host+", which holds "+m.Repo+": "+first, c.now(), c.now()
		c.Store.SaveGoal(g)
	}
	return m
}
