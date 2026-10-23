package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func TestNativeLoginUsesNamedIsolationAndPreservesLegacyDefaultRedirects(t *testing.T) {
	for _, h := range []string{"claude", "codex"} {
		t.Run(h, func(t *testing.T) {
			state, bins, legacy, redirect := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("YAMNAYA_STATE_DIR", state)
			t.Setenv("YAMNAYA_NO_SERVE", "1")
			t.Setenv("YAMNAYA_PROFILE", "")
			t.Setenv(profile.EnvKey(h), legacy)
			t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "")
			t.Setenv("CODEX_SQLITE_HOME", redirect)
			t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
			if err := os.WriteFile(filepath.Join(bins, h), []byte("#!/bin/sh\nprintf '%s\\n' \"$CLAUDE_CONFIG_DIR\" \"${CLAUDE_SECURESTORAGE_CONFIG_DIR-unset}\" \"$CODEX_HOME\" \"$CODEX_SQLITE_HOME\" \"$*\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			p := profile.Profile{Harness: h, ID: "work", Root: t.TempDir()}
			putProfileFile(t, filepath.Join(p.Root, "config.toml"), "sqlite_home = \"/copied/redirect\"\n")
			if err := profile.Add(state, p); err != nil {
				t.Fatal(err)
			}
			p.Root = physicalProfileRoot(p.Root)
			for _, id := range []string{"work", "default"} {
				cmd := NewRoot()
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&out)
				cmd.SetArgs([]string{"profiles", "login", h, id})
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
				got := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
				if len(got) != 5 {
					t.Fatalf("unexpected login output %q", got)
				}
				if h == "claude" {
					if id == "work" && (got[0] != p.Root || got[1] != p.Root) {
						t.Fatalf("named secure storage not pinned: %q", got)
					}
					if id == "default" && (got[0] != legacy || got[1] != "") {
						t.Fatalf("default empty override changed: %q", got)
					}
				} else {
					if id == "work" && (got[2] != p.Root || got[3] != p.Root || !strings.Contains(got[4], "sqlite_home=")) {
						t.Fatalf("named sqlite routing not pinned: %q", got)
					}
					if id == "default" && (got[2] != legacy || got[3] != redirect || got[4] != "login") {
						t.Fatalf("default sqlite redirect changed: %q", got)
					}
				}
			}
		})
	}
}
