package core

import (
	"path/filepath"
)

func (c *Core) workerHost(n NewLaunch) (string, bool) {
	if n.Host != "" {
		return n.Host, n.Host != c.SelfName()
	}
	return c.RepoHost(n.Repo)
}

// RepoForHost names a locally available checkout independently of this machine's paths.
func (c *Core) RepoForHost(repo string) string {
	if !repoHere(repo) || repo == "" {
		return repo
	}
	if c.Git != nil {
		if name := c.Git(repo).Name(); name != "" {
			return name
		}
	}
	return filepath.Base(MainCheckout(repo))
}

func pinLaunchHost(host string, args []string) []string {
	out := []string{args[0], "--host", host}
	return append(out, args[1:]...)
}

func (c *Core) NewChiefOn(host, name, remit, repo, harness, selected string, children []string) (string, error) {
	if err := c.refuseSupervisorWork(""); err != nil {
		return "", err
	}
	args := c.chiefArgs(name, remit, c.RepoForHost(repo), harness, selected, children)
	return c.RunOrRelay(host, pinLaunchHost(host, args)...)
}
