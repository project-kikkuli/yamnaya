package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ym = "/home/u/.local/bin/ym"

func TestWireStatusline(t *testing.T) {
	for _, c := range []struct {
		name     string
		settings string
		script   string
		want     string
	}{
		{"no settings", "", "", "#!/bin/sh\ninput=$(cat)\n" + limitsLine(ym) + "\n"},
		{"settings without statusline", `{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"a && b > /dev/null"}]}]}}`, "", "#!/bin/sh\ninput=$(cat)\n" + limitsLine(ym) + "\n"},
		{"script reads input", `{"statusLine":{"type":"command","command":"sh ~/.claude/sl.sh"}}`, "#!/bin/sh\ninput=$(cat)\necho hi\n",
			"#!/bin/sh\ninput=$(cat)\n" + limitsLine(ym) + "\necho hi\n"},
		{"already records", `{"statusLine":{"type":"command","command":"sh ~/.claude/sl.sh"}}`, "input=$(cat)\nprintf '%s' \"$input\" | ym limits record &\n",
			"input=$(cat)\nprintf '%s' \"$input\" | ym limits record &\n"},
		{"script never reads input", `{"statusLine":{"type":"command","command":"$HOME/.claude/sl.sh"}}`, "echo hi\n", "echo hi\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".claude")
			os.MkdirAll(dir, 0o755)
			if c.settings != "" {
				os.WriteFile(filepath.Join(dir, "settings.json"), []byte(c.settings), 0o644)
			}
			script := filepath.Join(dir, "statusline-ym.sh")
			if c.script != "" {
				script = filepath.Join(dir, "sl.sh")
				os.WriteFile(script, []byte(c.script), 0o755)
			}
			for range 2 {
				if _, err := wireStatusline(home, ym); err != nil {
					t.Fatal(err)
				}
			}
			if got, _ := os.ReadFile(script); string(got) != c.want {
				t.Errorf("script = %q, want %q", got, c.want)
			}
			b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
			var doc map[string]any
			if err := json.Unmarshal(b, &doc); err != nil {
				t.Fatalf("settings: %v", err)
			}
			sl, _ := doc["statusLine"].(map[string]any)
			if cmd, _ := sl["command"].(string); !strings.Contains(cmd, filepath.Base(script)) {
				t.Errorf("statusLine = %v", doc["statusLine"])
			}
			if strings.Contains(c.settings, "opus") && (doc["model"] != "opus" || !strings.Contains(string(b), "a && b > /dev/null")) {
				t.Errorf("settings lost model: %s", b)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{"new": "new", "--codex": "--codex", "": "''", "say hi": "'say hi'", "it's": `'it'\''s'`, "$HOME": "'$HOME'"} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
