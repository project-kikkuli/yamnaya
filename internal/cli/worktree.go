package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/store"
)

// gitSource is the shared git directory and checked-out branch of dir, or empty strings outside a branch checkout.
func gitSource(dir string) (gitDir, branch string) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", ""
	}
	b, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return "", ""
	}
	return strings.TrimSpace(string(out)), strings.TrimSpace(string(b))
}

// recordGit stores where a project's cwd comes from in git, when that is not known yet, and the commit it is on now.
func (a *app) recordGit(p store.Project) error {
	if p.GitDir == "" {
		if gitDir, branch := gitSource(p.Cwd); gitDir != "" {
			if err := a.store.SetGit(p.Name, gitDir, branch); err != nil {
				return err
			}
		}
	}
	out, err := exec.Command("git", "-C", p.Cwd, "rev-parse", "HEAD").Output()
	if err != nil {
		return nil
	}
	return a.store.SetHead(p.Name, strings.TrimSpace(string(out)))
}

// restoreCwd recreates a project's removed worktree from its recorded repository and branch, recreating the branch at its last seen commit when that is gone too.
func restoreCwd(p store.Project) error {
	if _, err := os.Stat(p.Cwd); err == nil {
		return nil
	}
	if p.GitDir == "" || p.Branch == "" {
		return fmt.Errorf("project %q: %s is gone and no git branch was recorded to recreate it", p.Name, p.Cwd)
	}
	exec.Command("git", "--git-dir="+p.GitDir, "worktree", "prune").Run()
	add := []string{"--git-dir=" + p.GitDir, "worktree", "add", p.Cwd, p.Branch}
	if exec.Command("git", "--git-dir="+p.GitDir, "show-ref", "--verify", "--quiet", "refs/heads/"+p.Branch).Run() != nil {
		if p.HeadSHA == "" {
			return fmt.Errorf("project %q: %s and its branch %s are gone and no commit was recorded to recreate them", p.Name, p.Cwd, p.Branch)
		}
		add = []string{"--git-dir=" + p.GitDir, "worktree", "add", "-b", p.Branch, p.Cwd, p.HeadSHA}
	}
	if out, err := exec.Command("git", add...).CombinedOutput(); err != nil {
		return fmt.Errorf("recreate %s from %s %s: %w: %s", p.Cwd, p.GitDir, p.Branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}
