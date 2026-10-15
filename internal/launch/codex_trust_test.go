package launch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustCodexDirAddsRepoRootAndDirOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(base, "repo")
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(sub, link); err != nil {
		t.Fatal(err)
	}
	existing := "model = \"x\"\n\n[projects.\"/elsewhere\"]\ntrust_level = \"untrusted\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := TrustCodexDir(link); err != nil {
			t.Fatal(err)
		}
	}
	if err := TrustCodexDir("/elsewhere"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	got := string(data)
	if !strings.HasPrefix(got, existing) {
		t.Fatalf("existing config changed:\n%s", got)
	}
	for _, key := range []string{repo, sub} {
		header := "[projects.\"" + key + "\"]\ntrust_level = \"trusted\"\n"
		if strings.Count(got, header) != 1 {
			t.Errorf("want one trusted entry for %s:\n%s", key, got)
		}
	}
	if strings.Count(got, "/elsewhere") != 1 {
		t.Errorf("an existing decision was overridden:\n%s", got)
	}
}
