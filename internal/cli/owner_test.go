package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRefuseForeign(t *testing.T) {
	account := func(uid int) string { return map[int]string{0: "root", 501: "rajat", 502: "guest"}[uid] }
	group := func(string) string { return "staff" }
	dirs := []string{"/Users/rajat/.local/state/yamnaya", "/Users/rajat/.local/state/agent-slot"}
	chown := "sudo chown -R rajat:staff /Users/rajat/.local/state/yamnaya /Users/rajat/.local/state/agent-slot"
	for _, tc := range []struct {
		name     string
		euid     int
		owner    int
		sudoUser string
		want     []string
	}{
		{"owner runs ym", 501, 501, "", nil},
		{"first run before any state", 501, -1, "", nil},
		{"sudo ym", 0, 501, "rajat", []string{"don't run ym with sudo", "rajat's ym state", "ym service keep", chown}},
		{"sudo ym before any state", 0, -1, "rajat", []string{"don't run ym with sudo", chown}},
		{"root shell on the owner's state", 0, 501, "", []string{"don't run ym with sudo", "rajat's ym state", chown}},
		{"root's own ym", 0, 0, "", nil},
		{"root's own ym before any state", 0, -1, "", nil},
		{"state a root run created", 501, 0, "", []string{dirs[0] + " belongs to root, not rajat", chown}},
		{"state another user owns", 501, 502, "", []string{"belongs to guest, not rajat", chown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseForeign(tc.euid, tc.owner, tc.sudoUser, account, group, dirs)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("ran")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("%q lacks %q", err, w)
				}
			}
		})
	}
}

func TestForeignFiles(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"board.json", "launches/a.json", "launches/b.json"} {
		path := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		uid  int
		want int
	}{{os.Getuid(), 0}, {os.Getuid() + 1, 1}} {
		t.Run(strconv.Itoa(tc.uid), func(t *testing.T) {
			if n, first := foreignFiles([]string{dir, filepath.Join(dir, "missing")}, tc.uid); n != tc.want || (n > 0) != (first == dir) {
				t.Fatalf("got %d, %q", n, first)
			}
		})
	}
}
