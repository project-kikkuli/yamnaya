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

// recordGit stores where a project's cwd comes from in git when that is not known yet.
func (a *app) recordGit(p store.Project) error {
	if p.GitDir != "" {
		return nil
	}
	gitDir, branch := gitSource(p.Cwd)
	if gitDir == "" {
		return nil
	}
	return a.store.SetGit(p.Name, gitDir, branch)
}

// restoreCwd recreates a project's removed worktree from its recorded repository and branch.
func restoreCwd(p store.Project) error {
	if _, err := os.Stat(p.Cwd); err == nil {
		return nil
	}
	if p.GitDir == "" || p.Branch == "" {
		return fmt.Errorf("project %q: %s is gone and no git branch was recorded to recreate it", p.Name, p.Cwd)
	}
	exec.Command("git", "--git-dir="+p.GitDir, "worktree", "prune").Run()
	if out, err := exec.Command("git", "--git-dir="+p.GitDir, "worktree", "add", p.Cwd, p.Branch).CombinedOutput(); err != nil {
		return fmt.Errorf("recreate %s from %s %s: %w: %s", p.Cwd, p.GitDir, p.Branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}
