package core

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBriefsAddrGivesEachStateDirectoryItsOwnPort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YAMNAYA_BRIEFS_ADDR", "")
	addr := func(state string) string {
		t.Setenv("YAMNAYA_STATE_DIR", state)
		return BriefsAddr()
	}
	for _, state := range []string{"", filepath.Join(home, ".local", "state", "yamnaya")} {
		if got := addr(state); got != "127.0.0.1:7477" {
			t.Errorf("state %q: %s, want the owner's 127.0.0.1:7477", state, got)
		}
	}
	seen := map[string]string{}
	for _, state := range []string{"/tmp/run1/state", "/tmp/run2/state", "/tmp/rehearsal/state"} {
		got := addr(state)
		port, err := strconv.Atoi(strings.TrimPrefix(got, "127.0.0.1:"))
		if err != nil || port == 7477 || port < 1024 || port > 65535 {
			t.Errorf("state %s: %s, want a loopback port other than 7477", state, got)
		}
		if again := addr(state); again != got {
			t.Errorf("state %s: %s then %s, want one stable address", state, got, again)
		}
		if other, ok := seen[got]; ok {
			t.Errorf("states %s and %s share %s", other, state, got)
		}
		seen[got] = state
	}
	t.Setenv("YAMNAYA_BRIEFS_ADDR", "127.0.0.1:9100")
	if got := addr(filepath.Join(home, "run1", "state")); got != "127.0.0.1:9100" {
		t.Errorf("YAMNAYA_BRIEFS_ADDR set: %s, want it", got)
	}
}

func TestBriefURLNamesThePublicURLWhenOneIsSet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YAMNAYA_STATE_DIR", "")
	t.Setenv("YAMNAYA_BRIEFS_ADDR", "")
	for _, tc := range []struct{ public, want string }{
		{"", "http://127.0.0.1:7477/b/goal1"},
		{"https://box.example/ym/", "https://box.example/ym/b/goal1"},
		{"https://box.example/ym", "https://box.example/ym/b/goal1"},
		{"https://box.example", "https://box.example/b/goal1"},
	} {
		t.Setenv("YAMNAYA_BRIEFS_URL", tc.public)
		if got := BriefURL("goal1"); got != tc.want {
			t.Errorf("YAMNAYA_BRIEFS_URL=%q: %s, want %s", tc.public, got, tc.want)
		}
	}
}
