package discover

import (
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const gitTTL = 30 * time.Second

type gitEntry struct {
	repo, branch string
	at           time.Time
}

func gitInfo(dir string) (string, string) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return "", ""
	}
	return filepath.Base(lines[0]), lines[1]
}

func (d *Discoverer) gitFor(dir string, now time.Time) (string, string) {
	if dir == "" {
		return "", ""
	}
	d.mu.Lock()
	e, ok := d.git[dir]
	d.mu.Unlock()
	if ok && now.Sub(e.at) < gitTTL {
		return e.repo, e.branch
	}
	repo, branch := d.Git(dir)
	d.mu.Lock()
	if d.git == nil {
		d.git = map[string]gitEntry{}
	}
	d.git[dir] = gitEntry{repo, branch, now}
	d.mu.Unlock()
	return repo, branch
}
