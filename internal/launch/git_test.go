package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFetchingWorktreesDoesNotStartAutomaticGitMaintenance(t *testing.T) {
	base := t.TempDir()
	run(t, base, "git", "init", "-q", "--bare", "-b", "main", "origin.git")
	run(t, base, "git", "init", "-q", "-b", "main", "repo")
	repo := filepath.Join(base, "repo")
	run(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "root")
	run(t, repo, "git", "remote", "add", "origin", filepath.Join(base, "origin.git"))
	run(t, repo, "git", "push", "-q", "origin", "main")
	trace := filepath.Join(base, "trace.jsonl")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	startsMaintenance := func() bool {
		t.Helper()
		raw, err := os.ReadFile(trace)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			var event struct {
				Event string   `json:"event"`
				Argv  []string `json:"argv"`
			}
			if json.Unmarshal([]byte(line), &event) == nil && event.Event == "child_start" && slices.Contains(event.Argv, "maintenance") {
				return true
			}
		}
		return false
	}
	run(t, repo, "git", "-c", "maintenance.auto=true", "-c", "maintenance.autoDetach=false", "-c", "gc.autoDetach=false", "fetch", "--quiet", "origin", "main")
	if !startsMaintenance() {
		t.Fatal("ordinary fetch did not exercise automatic maintenance")
	}
	if err := os.Remove(trace); err != nil {
		t.Fatal(err)
	}
	if _, err := Fresh(repo); err != nil {
		t.Fatal(err)
	}
	if startsMaintenance() {
		t.Fatal("ym fetch launched automatic maintenance")
	}
}

func TestWorktreeToolsInheritMaintenancePolicyAndExistingGitSettings(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "test.existing")
	t.Setenv("GIT_CONFIG_VALUE_0", "kept")
	for _, c := range []struct{ key, want string }{{"test.existing", "kept"}, {"gc.auto", "0"}, {"maintenance.auto", "false"}} {
		cmd := gitCommand("config", "--get", c.key)
		raw, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(raw)) != c.want {
			t.Fatalf("%s: %q, %v; want %q", c.key, raw, err, c.want)
		}
	}
}
