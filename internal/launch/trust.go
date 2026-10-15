package launch

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func claudeConfigPath() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude.json"), nil
}

// trustKeys is dir as given and as Claude Code sees it once symlinks resolve, such as /tmp to /private/tmp on macOS.
func trustKeys(dir string) []string {
	keys := []string{filepath.Clean(dir)}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != keys[0] {
		keys = append(keys, real)
	}
	return keys
}

// TrustClaudeDir records dir as trusted the way accepting Claude Code's trust dialog does, so a new window does not stall on it.
func TrustClaudeDir(dir string) error {
	path, err := claudeConfigPath()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".ym.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}

	config := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			return err
		}
	}
	projects := map[string]map[string]json.RawMessage{}
	if raw, ok := config["projects"]; ok {
		if err := json.Unmarshal(raw, &projects); err != nil {
			return err
		}
	}
	changed := false
	for _, key := range trustKeys(dir) {
		entry := projects[key]
		if string(entry["hasTrustDialogAccepted"]) == "true" {
			continue
		}
		if entry == nil {
			entry = map[string]json.RawMessage{}
		}
		entry["hasTrustDialogAccepted"] = json.RawMessage("true")
		projects[key] = entry
		changed = true
	}
	if !changed {
		return nil
	}
	if config["projects"], err = json.Marshal(projects); err != nil {
		return err
	}
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	mode := fs.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".claude.json.ym-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
