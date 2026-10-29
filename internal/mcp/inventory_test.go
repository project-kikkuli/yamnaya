package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func testTarget(t *testing.T, harness string) Target {
	t.Helper()
	p := profile.Profile{Host: "host-a", Harness: harness, ID: "work", Root: t.TempDir(), Explicit: true, Account: "account-a"}
	target, err := NewTarget(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func TestClaudeInventorySeparatesScopesAndNeverInheritsDefaultServers(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(map[bool]string{false: "project shadows user", true: "local shadows project"}[local], func(t *testing.T) {
			target := testTarget(t, "claude")
			put(t, filepath.Join(target.Cwd, ".git"), "")
			entry := map[string]any{"enabledMcpjsonServers": []string{"notion"}}
			if local {
				entry["mcpServers"] = map[string]any{"notion": map[string]any{"type": "http", "url": "https://local.invalid"}}
			}
			doc := map[string]any{"mcpServers": map[string]any{"notion": map[string]any{"type": "http", "url": "https://global.invalid", "headers": map[string]string{"Authorization": "secret-sentinel"}}}, "projects": map[string]any{target.Cwd: entry}}
			b, _ := json.Marshal(doc)
			put(t, target.ConfigFile, string(b))
			put(t, filepath.Join(target.Cwd, ".mcp.json"), `{"mcpServers":{"notion":{"type":"http","url":"https://project.invalid"}}}`)
			i, err := Read(target)
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			effective := "project"
			if local {
				want = 3
				effective = "local"
			}
			if len(i.Servers) != want {
				t.Fatalf("servers=%+v", i.Servers)
			}
			for _, s := range i.Servers {
				if s.Effective != (s.Scope == effective) || s.Auth != "unknown" || s.Health != "unchecked" {
					t.Fatalf("wrong state %+v", s)
				}
				if s.Scope == "project" && s.Approval != "approved" {
					t.Fatal(s.Approval)
				}
			}
			encoded, _ := json.Marshal(i)
			if strings.Contains(string(encoded), "secret-sentinel") || strings.Contains(string(encoded), "https://") {
				t.Fatalf("inventory exposed native values: %s", encoded)
			}
			other := target
			other.Root = t.TempDir()
			other.ConfigFile = filepath.Join(other.Root, ".claude.json")
			other.Cwd = t.TempDir()
			empty, err := Read(other)
			if err != nil || len(empty.Servers) != 0 {
				t.Fatalf("another root inherited MCPs: %+v %v", empty, err)
			}
		})
	}
}

func TestDefaultClaudeRootDistinguishesUnsetFromExplicit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, explicit := range []bool{false, true} {
		p := profile.Profile{Harness: "claude", ID: "default", Root: filepath.Join(home, ".claude"), Explicit: explicit}
		target, err := NewTarget(p, home)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(home, ".claude.json")
		if explicit {
			want = filepath.Join(home, ".claude", ".claude.json")
		}
		if target.ConfigFile != want {
			t.Fatalf("explicit=%v got=%s want=%s", explicit, target.ConfigFile, want)
		}
		found := false
		for _, entry := range p.Environment([]string{"CLAUDE_CONFIG_DIR=/contaminated"}) {
			if strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=") {
				found = true
			}
		}
		if found != explicit {
			t.Fatal("native environment changed implicit root behavior")
		}
	}
}

func TestCodexProjectLayersStaySeparateAndRespectNativeTrust(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		target := testTarget(t, "codex")
		put(t, filepath.Join(target.Cwd, ".git"), "")
		config := "[mcp_servers.notion]\nurl='https://global.invalid'\n"
		if trusted {
			config += "[projects." + fmtQuote(target.Cwd) + "]\ntrust_level='trusted'\n"
		}
		put(t, target.ConfigFile, config)
		put(t, filepath.Join(target.Cwd, ".codex", "config.toml"), "[mcp_servers.notion]\nurl='https://project.invalid'\nenabled=false\n")
		i, err := Read(target)
		if err != nil {
			t.Fatal(err)
		}
		if len(i.Servers) != 2 {
			t.Fatal(i.Servers)
		}
		for _, s := range i.Servers {
			if s.Scope == "project" && (s.Effective != trusted || s.Mutable || s.Configuration != "disabled") {
				t.Fatalf("project=%+v", s)
			}
			if s.Scope == "user" && s.Effective == trusted {
				t.Fatalf("user=%+v", s)
			}
		}
	}
}

func fmtQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestObservationValidityTracksAccountRootApprovalAndDefinition(t *testing.T) {
	for _, transition := range []string{"account", "root", "definition", "revoke", "grant", "shadow", "disabled", "expired"} {
		t.Run(transition, func(t *testing.T) {
			target := testTarget(t, "claude")
			state := t.TempDir()
			s := Server{Name: "notion", Scope: "project", Source: "/repo/.mcp.json", Context: target.Cwd, Fingerprint: "v1", Effective: true, Configuration: "configured", Approval: "approved", Auth: "unknown", Health: "unchecked"}
			oldHealth := "connected"
			if transition == "grant" {
				s.Approval = "required"
				oldHealth = "approval-required"
			}
			now := time.Now().UTC()
			at := now
			if transition == "expired" {
				at = now.Add(-time.Hour)
			}
			if err := SaveObservation(state, target, s, Observation{Auth: "oauth", Health: oldHealth, At: at}); err != nil {
				t.Fatal(err)
			}
			switch transition {
			case "account":
				target.Native.Account = "account-b"
			case "root":
				target.Root += "-other"
			case "definition":
				s.Fingerprint = "v2"
			case "revoke":
				s.Approval = "rejected"
			case "grant":
				s.Approval = "approved"
			case "shadow":
				s.Effective = false
			case "disabled":
				s.Configuration = "disabled"
			}
			i := Inventory{Target: target, Servers: []Server{s}}
			i.Observations(state, now)
			got := i.Servers[0]
			if got.Health == oldHealth || got.Auth == "oauth" {
				t.Fatalf("stale state reused after %s: %+v", transition, got)
			}
		})
	}
}

func TestReadHasNoProcessOrWriteSideEffects(t *testing.T) {
	target := testTarget(t, "claude")
	marker := filepath.Join(t.TempDir(), "started")
	put(t, target.ConfigFile, `{"mcpServers":{"danger":{"command":"touch","args":[`+fmtQuote(marker)+`]}}}`)
	before, _ := os.ReadFile(target.ConfigFile)
	for range 2 {
		if _, err := Read(target); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := os.ReadFile(target.ConfigFile)
	if string(before) != string(after) {
		t.Fatal("inventory rewrote configuration")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("inventory started stdio server")
	}
}
