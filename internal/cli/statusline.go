package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func wireProfileStatusline(root, home, ym string) (string, error) {
	settings := filepath.Join(root, "settings.json")
	data, err := os.ReadFile(settings)
	if os.IsNotExist(err) {
		return wireStatuslineAt(root, home, ym)
	}
	if err != nil {
		return "", err
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal(data, &doc); err != nil {
		return "", err
	}
	var sl struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	if raw := doc["statusLine"]; len(raw) > 0 {
		if err = json.Unmarshal(raw, &sl); err != nil {
			return "", err
		}
	}
	script := filepath.Join(root, "statusline-ym.sh")
	if sl.Command == "" || sl.Command == "sh "+shellQuote(script) {
		return wireStatuslineAt(root, home, ym)
	}
	text := "#!/bin/sh\ninput=$(cat)\n" + limitsLine(ym) + "\nprintf '%s' \"$input\" | sh -c " + shellQuote(sl.Command) + "\n"
	if err = profile.Write(script, []byte(text)); err != nil {
		return "", err
	}
	doc["statusLine"], err = json.Marshal(map[string]string{"type": "command", "command": "sh " + shellQuote(script)})
	if err != nil {
		return "", err
	}
	data, err = json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	if err = profile.Write(settings, data); err != nil {
		return "", err
	}
	return "statusline: records limits inside " + root, nil
}

// limitsLine feeds the statusline input to ym in the background, so the statusline gains no latency.
func limitsLine(ym string) string {
	return `printf '%s' "$input" | ` + shellQuote(ym) + ` limits record >/dev/null 2>&1 &`
}

// wireStatusline makes Claude Code's statusline under home record limits with ym: it adds a statusline when there is none,
// and otherwise adds the record line to the statusline's script right after it reads its input.
func wireStatusline(home, ym string) (string, error) {
	return wireStatuslineAt(filepath.Join(home, ".claude"), home, ym)
}

func wireStatuslineAt(root, home, ym string) (string, error) {
	settings := filepath.Join(root, "settings.json")
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
		script := filepath.Join(root, "statusline-ym.sh")
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
