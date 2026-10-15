package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// limitsLine feeds the statusline input to ym in the background, so the statusline gains no latency.
func limitsLine(ym string) string {
	return `printf '%s' "$input" | ` + shellQuote(ym) + ` limits record >/dev/null 2>&1 &`
}

// wireStatusline makes Claude Code's statusline under home record limits with ym: it adds a statusline when there is none,
// and otherwise adds the record line to the statusline's script right after it reads its input.
func wireStatusline(home, ym string) (string, error) {
	settings := filepath.Join(home, ".claude", "settings.json")
	doc := map[string]any{}
	b, err := os.ReadFile(settings)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &doc); err != nil {
			return "", fmt.Errorf("%s: %w", settings, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return "", err
	}
	line := limitsLine(ym)
	sl, _ := doc["statusLine"].(map[string]any)
	if sl == nil {
		script := filepath.Join(home, ".claude", "statusline-ym.sh")
		if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(script, []byte("#!/bin/sh\ninput=$(cat)\n"+line+"\n"), 0o755); err != nil {
			return "", err
		}
		doc["statusLine"] = map[string]any{"type": "command", "command": "sh " + shellQuote(script)}
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(doc); err != nil {
			return "", err
		}
		if err := os.WriteFile(settings, out.Bytes(), 0o644); err != nil {
			return "", err
		}
		return "statusline: added " + script, nil
	}
	command, _ := sl["command"].(string)
	script := statuslineScript(home, command)
	if script == "" {
		return fmt.Sprintf("statusline: %q runs no script ym can edit; add this after it reads stdin into $input:\n  %s", command, line), nil
	}
	b, err = os.ReadFile(script)
	if err != nil {
		return "", err
	}
	text := string(b)
	if strings.Contains(text, " limits record") {
		return "statusline: " + script + " already records limits", nil
	}
	lines := strings.SplitAfter(text, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "input=$(cat)" {
			lines = append(lines[:i+1], append([]string{line + "\n"}, lines[i+1:]...)...)
			if err := os.WriteFile(script, []byte(strings.Join(lines, "")), 0o755); err != nil {
				return "", err
			}
			return "statusline: added the limits record line to " + script, nil
		}
	}
	return fmt.Sprintf("statusline: %s does not read stdin into $input; add this where it does:\n  %s", script, line), nil
}

// statuslineScript is the script file a statusline command runs, or "".
func statuslineScript(home, command string) string {
	for _, f := range strings.Fields(command) {
		f = strings.Trim(f, `'"`)
		if strings.HasPrefix(f, "~/") {
			f = filepath.Join(home, f[2:])
		}
		f = strings.ReplaceAll(f, "$HOME", home)
		if !filepath.IsAbs(f) || strings.HasPrefix(f, "/bin/") || strings.HasPrefix(f, "/usr/") {
			continue
		}
		if info, err := os.Stat(f); err == nil && info.Mode().IsRegular() {
			return f
		}
	}
	return ""
}
