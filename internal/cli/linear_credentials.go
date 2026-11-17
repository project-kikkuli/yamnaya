package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type linearConfig struct{ OwnerID, OwnerName, WorkspaceID, Workspace, Storage string }

const linearService = "yamnaya.linear"

func linearConfigAt(dir string) (linearConfig, error) {
	var cfg linearConfig
	raw, err := os.ReadFile(filepath.Join(dir, "linear.json"))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal(raw, &cfg)
	return cfg, err
}

func keychainLinear(operation, account, token string) (string, error) {
	var cmd *exec.Cmd
	switch operation {
	case "save":
		cmd = exec.Command("/usr/bin/security", "-i")
		cmd.Stdin = strings.NewReader("add-generic-password -U -s " + strconv.Quote(linearService) + " -a " + strconv.Quote(account) + " -w " + strconv.Quote(token) + "\n")
	case "read":
		cmd = exec.Command("/usr/bin/security", "find-generic-password", "-s", linearService, "-a", account, "-w")
	case "delete":
		cmd = exec.Command("/usr/bin/security", "delete-generic-password", "-s", linearService, "-a", account)
	default:
		return "", errors.New("unknown Linear credential operation")
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", errors.New("Linear Keychain operation failed; unlock the login keychain or reconnect")
	}
	if operation == "read" {
		return strings.TrimSpace(out.String()), nil
	}
	return "", nil
}

func saveLinearConfig(dir string, cfg linearConfig, token string) error {
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return errors.New("Linear API key is empty or malformed")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		if _, err := keychainLinear("save", cfg.OwnerID, token); err != nil {
			return err
		}
		cfg.Storage = "keychain"
	} else {
		if err := writeLinearPrivateFile(dir, "linear.token", []byte(token)); err != nil {
			return err
		}
		cfg.Storage = "file"
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return writeLinearPrivateFile(dir, "linear.json", raw)
}

func loadLinearToken(dir string, cfg linearConfig) (string, error) {
	if token := os.Getenv("LINEAR_API_KEY"); token != "" {
		return token, nil
	}
	if cfg.OwnerID == "" {
		return "", nil
	}
	if cfg.Storage == "keychain" {
		if runtime.GOOS != "darwin" {
			return "", errors.New("Linear Keychain credential belongs to another operating system; connect on this host")
		}
		return keychainLinear("read", cfg.OwnerID, "")
	}
	path := filepath.Join(dir, "linear.token")
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("Linear credential file must be owner-only: chmod 600 %s", path)
	}
	raw, err := os.ReadFile(path)
	return strings.TrimSpace(string(raw)), err
}

func disconnectLinear(dir string) error {
	cfg, err := linearConfigAt(dir)
	if err != nil {
		return err
	}
	if cfg.Storage == "keychain" && cfg.OwnerID != "" {
		if _, err = keychainLinear("delete", cfg.OwnerID, ""); err != nil {
			return err
		}
	}
	for _, file := range []string{"linear.json", "linear.token"} {
		if err = os.Remove(filepath.Join(dir, file)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func writeLinearPrivateFile(dir, name string, data []byte) error {
	f, err := os.CreateTemp(dir, ".linear-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, name))
}
