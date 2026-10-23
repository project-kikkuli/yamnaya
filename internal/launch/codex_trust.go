package launch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// codexTrustKeys are the paths Codex asks about for dir: the enclosing git checkout's root when there is one, and dir itself, both with symlinks resolved.
func codexTrustKeys(dir string) []string {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		real = filepath.Clean(dir)
	}
	keys := []string{real}
	if out, err := exec.Command("git", "-C", real, "rev-parse", "--show-toplevel").Output(); err == nil {
		if root := strings.TrimSpace(string(out)); root != "" && root != real {
			keys = append([]string{root}, keys...)
		}
	}
	return keys
}

// TrustCodexDir records dir as trusted in Codex's config.toml the way accepting Codex's folder-access dialog does, unless Codex already has a decision for it.
func TrustCodexDir(dir string) error {
	home, err := codexHome()
	if err != nil {
		return err
	}
	return trustCodexAt(home, dir)
}

func trustCodexAt(home, dir string) error {
	path := filepath.Join(home, "config.toml")
	if err := os.MkdirAll(home, 0o755); err != nil {
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
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	config := string(data)
	var add strings.Builder
	for _, key := range codexTrustKeys(dir) {
		header := "[projects." + strconv.Quote(key) + "]"
		if strings.Contains(config, header) {
			continue
		}
		add.WriteString("\n" + header + "\ntrust_level = \"trusted\"\n")
	}
	if add.Len() == 0 {
		return nil
	}
	if config != "" && !strings.HasSuffix(config, "\n") {
		config += "\n"
	}
	mode := fs.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(home, ".config.toml.ym-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(config + add.String()); err != nil {
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

func codexHome() (string, error) {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

// Trust marks dir trusted for the harness so its first screen is the prompt, not a folder dialog.
func Trust(harnessName, dir string) error {
	trust := map[string]func(string) error{"claude": TrustClaudeDir, "codex": TrustCodexDir}[harnessName]
	if trust == nil {
		return fmt.Errorf("unknown harness %q (want claude or codex)", harnessName)
	}
	if err := trust(dir); err != nil {
		return fmt.Errorf("mark %s trusted for %s: %w", dir, harnessName, err)
	}
	return nil
}

func TrustRoot(harness, root, dir string, explicit bool) error {
	if root == "" {
		return Trust(harness, dir)
	}
	if harness == "codex" {
		return trustCodexAt(root, dir)
	}
	if !explicit {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		return trustClaudeAt(filepath.Join(home, ".claude.json"), dir)
	}
	return trustClaudeAt(filepath.Join(root, ".claude.json"), dir)
}
