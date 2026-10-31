package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBinaryWatchChanged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, path string)
		want   []bool
	}{
		{"unchanged", func(*testing.T, string) {}, []bool{false, false, false}},
		{"mtime changed", func(t *testing.T, path string) {
			later := time.Now().Add(time.Minute)
			if err := os.Chtimes(path, later, later); err != nil {
				t.Fatal(err)
			}
		}, []bool{false, true, true}},
		{"replaced file", func(t *testing.T, path string) {
			next := path + ".new"
			if err := os.WriteFile(next, []byte("v1"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(next, path); err != nil {
				t.Fatal(err)
			}
		}, []bool{false, true, true}},
		{"removed", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}, []bool{false, false, false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ym")
			if err := os.WriteFile(path, []byte("v0"), 0o755); err != nil {
				t.Fatal(err)
			}
			w, err := watchBinary(path)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(t, path)
			for i, want := range tc.want {
				if got := w.changed(); got != want {
					t.Fatalf("poll %d: changed() = %v, want %v", i, got, want)
				}
			}
		})
	}
}

func TestBinaryWatchWaitsForWritesToStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym")
	if err := os.WriteFile(path, []byte("v0"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := watchBinary(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"v1-partial", "v1-partial-more", "v1-partial-more-done"} {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		if w.changed() {
			t.Fatalf("changed() while %q was still being written", body)
		}
	}
	if !w.changed() {
		t.Fatal("changed() = false once the file stopped changing")
	}
}

func TestBoardRestartsWhenItsBinaryChanges(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode mode
		want bool
	}{
		{"on the list", modeList, true},
		{"while typing", modeInput, false},
		{"while confirming", modeConfirm, false},
		{"while picking", modePick, false},
		{"while merging", modeMerge, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ym")
			if err := os.WriteFile(path, []byte("v0"), 0o755); err != nil {
				t.Fatal(err)
			}
			w, err := watchBinary(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("v1-new"), 0o755); err != nil {
				t.Fatal(err)
			}
			m := Model{self: w, mode: tc.mode, interval: time.Hour}
			for range 2 {
				next, _ := m.Update(tickMsg{})
				m = next.(Model)
			}
			if m.restart != tc.want {
				t.Fatalf("restart = %v, want %v", m.restart, tc.want)
			}
		})
	}
}

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
