package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/mcp"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func mcpFixture(t *testing.T, h string) (profile.Profile, string, string) {
	t.Helper()
	state := t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", state)
	p := profile.Profile{Harness: h, ID: "mcp-test", Root: t.TempDir(), Explicit: true}
	if err := profile.Add(state, p); err != nil {
		t.Fatal(err)
	}
	name, body := ".claude.json", `{"mcpServers":{"notion":{"type":"http","url":"https://native.invalid","headers":{"Authorization":"secret-sentinel"}}}}`
	if h == "codex" {
		name, body = "config.toml", "[mcp_servers.notion]\nurl='https://native.invalid'\n"
	}
	if err := os.WriteFile(filepath.Join(p.Root, name), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "calls")
	binary := filepath.Join(t.TempDir(), "native")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '" + log + "'\nif [ \"$1\" = app-server ]; then\n read -r line\n printf '%s\\n' '{\"id\":1,\"result\":{}}'\n read -r line\n read -r line\n printf '%s\\n' '{\"id\":2,\"result\":{\"data\":[{\"name\":\"notion\",\"authStatus\":\"oAuth\",\"runtimeStatus\":\"connected\"}]}}'\n read -r line\nelse\n printf '%s\\n' 'Status: ✓ Connected'\nfi\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if h == "claude" {
		t.Setenv("YAMNAYA_CLAUDE_BIN", binary)
	} else {
		t.Setenv("YAMNAYA_CODEX_BIN", binary)
	}
	return p, state, log
}

func mcpExecute(args ...string) (string, error) {
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestMCPCLIInventoryAndExplicitNativeChecks(t *testing.T) {
	for _, h := range []string{"claude", "codex"} {
		t.Run(h, func(t *testing.T) {
			p, state, log := mcpFixture(t, h)
			cwd := t.TempDir()
			base := []string{"--harness", h, "--profile", p.ID, "--root", p.Root, "--cwd", cwd}
			out, err := mcpExecute(append([]string{"mcp", "list", "--json"}, base...)...)
			if err != nil {
				t.Fatal(err)
			}
			var all []mcp.Inventory
			if json.Unmarshal([]byte(out), &all) != nil || len(all) != 1 || len(all[0].Servers) != 1 {
				t.Fatalf("inventory=%s", out)
			}
			if strings.Contains(out, "secret-sentinel") {
				t.Fatal("inventory leaked headers")
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("list invoked a native executable")
			}
			args := append([]string{"mcp", "check", "notion", "--scope", "user", "--source", all[0].Servers[0].Source, "--revision", all[0].Servers[0].Fingerprint}, base...)
			out, err = mcpExecute(args...)
			if err != nil || !strings.Contains(out, "health=connected") {
				t.Fatalf("check=%s %v", out, err)
			}
			files, _ := filepath.Glob(filepath.Join(state, "mcp-status", "*.json"))
			if len(files) != 1 {
				t.Fatal("explicit check did not save one scoped observation")
			}
			cached, _ := os.ReadFile(files[0])
			if bytes.Contains(cached, []byte("sentinel")) {
				t.Fatal("cache exposed credentials")
			}
		})
	}
}

func TestMCPCLIRefusesBroadenedStaleAndNoninteractiveTargets(t *testing.T) {
	for _, scenario := range []string{"profile without harness", "unknown profile", "root changed", "definition changed", "scope missing", "noninteractive auth"} {
		t.Run(scenario, func(t *testing.T) {
			p, state, log := mcpFixture(t, "claude")
			cwd := t.TempDir()
			args := []string{"mcp", "remove", "notion", "--harness", "claude", "--profile", p.ID, "--root", p.Root, "--cwd", cwd, "--scope", "user"}
			switch scenario {
			case "profile without harness":
				args = []string{"mcp", "list", "--profile", p.ID}
			case "unknown profile":
				args = []string{"mcp", "list", "--harness", "claude", "--profile", "missing"}
			case "root changed":
				if err := profile.Remove(state, "claude", p.ID); err != nil {
					t.Fatal(err)
				}
				p.Root = t.TempDir()
				if err := profile.Add(state, p); err != nil {
					t.Fatal(err)
				}
			case "definition changed":
				args = append(args, "--revision", "old-revision")
			case "scope missing":
				args = args[:len(args)-2]
			case "noninteractive auth":
				args[1] = "login"
				old := mcpTerminal
				mcpTerminal = func() bool { return false }
				t.Cleanup(func() { mcpTerminal = old })
			}
			if out, err := mcpExecute(args...); err == nil {
				t.Fatalf("unsafe target accepted: %s", out)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("refused operation reached native executable")
			}
		})
	}
}

func TestRemoteBoardPreservesExactMCPSessionRoots(t *testing.T) {
	for _, id := range []string{"default", "work"} {
		for _, root := range []string{"", "/remote/native-root"} {
			s := core.Live{Session: core.Session{Harness: "claude", Profile: id, ConfigRoot: root, ID: "conversation", Cwd: "/remote/repo"}, Remote: "host"}
			wire := sessionsJSON(&core.Core{}, core.Board{Sessions: []core.Live{s}})
			b, err := json.Marshal(boardJSON{Sessions: wire})
			if err != nil {
				t.Fatal(err)
			}
			var received boardJSON
			if json.Unmarshal(b, &received) != nil {
				t.Fatal("invalid wire")
			}
			remote := remoteBoard("host", received)
			if len(remote.Sessions) != 1 || remote.Sessions[0].ConfigRoot != root || remote.Sessions[0].Profile != id {
				t.Fatal("remote MCP namespace changed")
			}
		}
	}
}
