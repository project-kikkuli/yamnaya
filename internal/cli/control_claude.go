package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func managedClaudeArgv(rec launch.Record, native []string) ([]string, error) {
	root := claude.New().Dir
	if rec.ConfigRoot != "" {
		root = rec.ConfigRoot
	}
	args, err := claudePeerArgs(native[1:], root, rec.Worktree.Path)
	if err != nil {
		return nil, err
	}
	argv := []string{"env"}
	for _, name := range nativeIdentityEnv {
		argv = append(argv, "-u", name)
	}
	argv = append(argv, native[0])
	return scopedArgv(rec, append(argv, args...)), nil
}

func claudePeerArgs(native []string, root, cwd string) ([]string, error) {
	settings := map[string]json.RawMessage{}
	var args []string
	for i := 0; i < len(native); i++ {
		arg := native[i]
		if arg == "--" {
			args = append(args, native[i:]...)
			break
		}
		value, inline := strings.CutPrefix(arg, "--settings=")
		if arg != "--settings" && !inline {
			args = append(args, arg)
			continue
		}
		if !inline {
			i++
			if i == len(native) {
				return nil, fmt.Errorf("native Claude --settings needs a value")
			}
			value = native[i]
		}
		raw := []byte(value)
		if !strings.HasPrefix(strings.TrimSpace(value), "{") {
			var err error
			if !filepath.IsAbs(value) && cwd != "" {
				value = filepath.Join(cwd, value)
			}
			raw, err = os.ReadFile(value)
			if err != nil {
				return nil, fmt.Errorf("read native Claude --settings: %w", err)
			}
		}
		if err := json.Unmarshal(raw, &settings); err != nil || settings == nil {
			return nil, fmt.Errorf("native Claude --settings must contain a JSON object")
		}
	}
	if value, set := settings["crossSessionInbound"]; !set || string(value) == "null" {
		var user struct {
			Inbound json.RawMessage `json:"crossSessionInbound"`
		}
		raw, err := os.ReadFile(filepath.Join(root, "settings.json"))
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("read native Claude user settings: %w", err)
		}
		if len(raw) != 0 {
			if err = json.Unmarshal(raw, &user); err != nil {
				return nil, fmt.Errorf("read native Claude user settings: %w", err)
			}
		}
		value = user.Inbound
		if len(value) == 0 || string(value) == "null" {
			value = json.RawMessage(`"accept"`)
		}
		settings["crossSessionInbound"] = value
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	return append([]string{"--settings", string(raw)}, args...), nil
}
