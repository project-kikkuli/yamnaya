package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTrustClaudeDirTrustsWhereClaudeLooks(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, base string) (dir string, want []string)
	}{
		{"symlinked dir", func(t *testing.T, base string) (string, []string) {
			real := filepath.Join(base, "real")
			link := filepath.Join(base, "link")
			if err := os.Mkdir(real, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, link); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(link, "."), []string{link, real}
		}},
		{"linked worktree", func(t *testing.T, base string) (string, []string) {
			run(t, base, "git", "init", "-q", "-b", "main", "repo")
			repo := filepath.Join(base, "repo")
			run(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "root")
			wt := WorktreePath(repo, "feature")
			run(t, repo, "git", "worktree", "add", "-q", "-b", "feature", wt)
			return wt, []string{wt, repo}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			t.Setenv("CLAUDE_CONFIG_DIR", base)
			dir, want := c.setup(t, base)
			if err := TrustClaudeDir(dir); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(base, ".claude.json"))
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Projects map[string]struct {
					Trusted bool `json:"hasTrustDialogAccepted"`
				} `json:"projects"`
			}
			if err := json.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			for _, key := range want {
				if !config.Projects[key].Trusted {
					t.Errorf("%s not trusted: %s", key, data)
				}
			}
		})
	}
}
