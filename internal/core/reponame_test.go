package core

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAManagersAuthorityFollowsOriginsNameNotTheCheckoutDirectory(t *testing.T) {
	for _, c := range []struct {
		name, dir, origin string
		want              string
	}{
		{"clone under another name", "proving-ground", "git@github.com:me/yamnaya-proving-ground.git", "may merge"},
		{"https origin", "pg", "https://github.com/me/yamnaya-proving-ground", "may merge"},
		{"no origin", "yamnaya-proving-ground", "", "may merge"},
		{"no origin, other name", "proving-ground", "", "may not merge"},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), c.dir)
			if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if c.origin != "" {
				if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", c.origin).CombinedOutput(); err != nil {
					t.Fatalf("%v: %s", err, out)
				}
			}
			w := newOrgWorld(t)
			if err := w.core.Store.SetAuthority("yamnaya-proving-ground", true); err != nil {
				t.Fatal(err)
			}
			w.goal(t, "add a flag", repo)
			if p := w.launches.starts[0].Prompt; !strings.Contains(p, "you "+c.want+" your own") && !strings.Contains(p, "you "+c.want+". ") {
				t.Fatalf("prompt for %s does not say the manager %s:\n%s", c.name, c.want, p)
			}
		})
	}
}
