package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func TestManagedClaudePreservesNativeTaskAndScopesPeerSettings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	binary := filepath.Join(dir, "claude-fixture")
	capture := filepath.Join(dir, "args")
	identities := filepath.Join(dir, "identities")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$YM_CAPTURE_ARGS\"\n"
	script += "printf '%s:%s:%s:%s:%s' \"$CODEX_THREAD_ID\" \"$CLAUDE_CODE_SESSION_ID\" \"$CLAUDECODE\" \"$CLAUDE_CODE_MESSAGING_SOCKET\" \"$CLAUDE_CODE_MESSAGING_TOKEN\" > \"$YM_CAPTURE_IDENTITIES\"\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YM_CAPTURE_ARGS", capture)
	t.Setenv("YM_CAPTURE_IDENTITIES", identities)
	for _, name := range []string{"CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID", "CLAUDECODE", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN"} {
		t.Setenv(name, "parent-identity")
	}
	native := []string{"--dangerously-skip-permissions", "--session-id", "native", "initial task"}
	argv, err := managedClaudeArgv(launch.Record{Harness: "claude", Session: "native"}, append([]string{binary}, native...))
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("direct native fixture failed: %v %s", err, output)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(args) != len(native)+2 || args[0] != "--settings" || args[1] != `{"crossSessionInbound":"accept"}` || !slices.Equal(args[2:], native) {
		t.Fatalf("native task, session or scoped peer settings changed: %q", args)
	}
	caller, err := os.ReadFile(identities)
	if err != nil || string(caller) != "::::" {
		t.Fatalf("native child inherited caller identity or peer token: %q %v", caller, err)
	}
}

func TestManagedClaudePeerSettingsPreserveExplicitPolicyAndOtherNativeSettings(t *testing.T) {
	for _, c := range []struct {
		name, settings, user, want string
		file, equals               bool
	}{
		{name: "default", want: "accept"},
		{name: "user hold", user: `{"crossSessionInbound":"hold"}`, want: "hold"},
		{name: "user refusal", user: `{"crossSessionInbound":"refuse","permissions":{"deny":["SendMessage"]}}`, want: "refuse"},
		{name: "invalid user inbound remains invalid", user: `{"crossSessionInbound":"future-value"}`, want: "future-value"},
		{name: "inline custom settings", settings: `{"permissions":{"deny":["Bash(rm:*)"]},"env":{"KEEP":"value"}}`, want: "accept"},
		{name: "file custom settings", settings: `{"permissions":{"deny":["Bash(rm:*)"]},"env":{"KEEP":"value"}}`, file: true, want: "accept"},
		{name: "explicit hold", settings: `{"crossSessionInbound":"hold","permissions":{"deny":["SendMessage"]}}`, want: "hold"},
		{name: "explicit refusal", settings: `{"crossSessionInbound":"refuse"}`, want: "refuse", equals: true},
		{name: "explicit CLI choice", settings: `{"crossSessionInbound":"accept"}`, user: `{"crossSessionInbound":"refuse"}`, want: "accept"},
		{name: "invalid explicit inbound", settings: `{"crossSessionInbound":"invalid"}`, want: "invalid"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", dir)
			if c.user != "" {
				if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(c.user), 0600); err != nil {
					t.Fatal(err)
				}
			}
			native := []string{"--chrome", "--resume", "session-id"}
			if c.settings != "" {
				value := c.settings
				if c.file {
					value = filepath.Join(dir, "launch-settings.json")
					if err := os.WriteFile(value, []byte(c.settings), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if c.equals {
					native = append(native, "--settings="+value)
				} else {
					native = append(native, "--settings", value)
				}
			}
			args, err := claudePeerArgs(native, dir, dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(args) != 5 || args[0] != "--settings" || !slices.Equal(args[2:], native[:3]) {
				t.Fatalf("native resume flags changed: %q", args)
			}
			var got, prior map[string]json.RawMessage
			if err := json.Unmarshal([]byte(args[1]), &got); err != nil {
				t.Fatal(err)
			}
			var inbound string
			if err := json.Unmarshal(got["crossSessionInbound"], &inbound); err != nil || inbound != c.want {
				t.Fatalf("inbound policy changed: %s, %v", args[1], err)
			}
			if c.settings != "" {
				if err := json.Unmarshal([]byte(c.settings), &prior); err != nil {
					t.Fatal(err)
				}
			}
			delete(got, "crossSessionInbound")
			delete(prior, "crossSessionInbound")
			if len(got) != len(prior) {
				t.Fatalf("settings were added or lost: %s", args[1])
			}
			for key, value := range prior {
				var a, b any
				if err := json.Unmarshal(value, &a); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(got[key], &b); err != nil || !reflect.DeepEqual(a, b) {
					t.Fatalf("native setting %s changed: %s -> %s", key, value, got[key])
				}
			}
			if c.user != "" {
				user, err := os.ReadFile(filepath.Join(dir, "settings.json"))
				if err != nil || string(user) != c.user {
					t.Fatalf("user settings mutated: %q, %v", user, err)
				}
			}
		})
	}
}

func TestManagedClaudeResolvesSettingsAgainstItsSelectedProfileAndWorktree(t *testing.T) {
	daemon, target, worktree := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", daemon)
	if err := os.WriteFile(filepath.Join(daemon, "settings.json"), []byte(`{"crossSessionInbound":"accept"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "settings.json"), []byte(`{"crossSessionInbound":"hold"}`), 0600); err != nil {
		t.Fatal(err)
	}
	custom := `{"permissions":{"deny":["SendMessage"]}}`
	path := filepath.Join(worktree, "launch.json")
	if err := os.WriteFile(path, []byte(custom), 0600); err != nil {
		t.Fatal(err)
	}
	rec := launch.Record{Harness: "claude", Session: "native", Profile: "claude-work", ConfigRoot: target, ConfigExplicit: true, Worktree: launch.Worktree{Path: worktree}}
	args, err := managedClaudeArgv(rec, []string{"claude", "--settings", "launch.json", "--resume", "native"})
	if err != nil {
		t.Fatal(err)
	}
	at := slices.Index(args, "--settings")
	if at < 0 || at+1 == len(args) {
		t.Fatalf("scoped settings missing: %q", args)
	}
	var settings struct {
		Inbound     string                  `json:"crossSessionInbound"`
		Permissions struct{ Deny []string } `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(args[at+1]), &settings); err != nil || settings.Inbound != "hold" || !slices.Equal(settings.Permissions.Deny, []string{"SendMessage"}) {
		t.Fatalf("selected profile policy or worktree settings lost: %+v, %v", settings, err)
	}
	if !slices.Equal(args[at+2:], []string{"--resume", "native"}) {
		t.Fatalf("native session changed: %q", args)
	}
	stored, err := os.ReadFile(path)
	if err != nil || string(stored) != custom {
		t.Fatalf("launch settings file mutated: %q, %v", stored, err)
	}
}
