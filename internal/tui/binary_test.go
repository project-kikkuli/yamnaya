package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStaleWarningNamesANewerYmFirstOnPath(t *testing.T) {
	dir := t.TempDir()
	running, onPath := filepath.Join(dir, "gobin", "ym"), filepath.Join(dir, "local", "ym")
	for _, p := range []string{running, onPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(running, old, old); err != nil {
		t.Fatal(err)
	}
	started, _ := os.Stat(running)
	t.Setenv("PATH", filepath.Dir(onPath))
	if got := StaleWarning(running, started); !strings.Contains(got, running) || !strings.Contains(got, onPath) {
		t.Fatalf("warning %q does not name %s and %s", got, running, onPath)
	}
	if got := StaleWarning(onPath, mustStat(t, onPath)); got != "" {
		t.Fatalf("the ym on PATH warns %q", got)
	}
	t.Setenv("PATH", filepath.Dir(running))
	if got := StaleWarning(running, started); got != "" {
		t.Fatalf("warns %q with only itself on PATH", got)
	}
	newer := time.Now().Add(time.Hour)
	if err := os.Chtimes(running, newer, newer); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(onPath))
	if got := StaleWarning(running, mustStat(t, running)); got != "" {
		t.Fatalf("warns %q though the running ym is newer", got)
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}
