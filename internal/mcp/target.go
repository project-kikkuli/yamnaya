package mcp

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/project-kikkuli/yamnaya/internal/profile"
)

type Target struct {
	Host       string          `json:"host"`
	Harness    string          `json:"harness"`
	Profile    string          `json:"profile"`
	Root       string          `json:"root"`
	Cwd        string          `json:"cwd"`
	ConfigFile string          `json:"config_file"`
	Native     profile.Profile `json:"-"`
}

func NewTarget(p profile.Profile, cwd string) (Target, error) {
	t := Target{Host: p.Host, Harness: p.Harness, Profile: profile.ID(p.ID), Root: p.Root, Cwd: cwd, Native: p}
	if p.Harness != "claude" && p.Harness != "codex" {
		return t, fmt.Errorf("unsupported MCP harness")
	}
	if err := p.DefaultConstraint(); err != nil {
		return t, err
	}
	if !filepath.IsAbs(p.Root) || !filepath.IsAbs(cwd) {
		return t, fmt.Errorf("MCP root and cwd must be absolute on the selected host")
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return t, fmt.Errorf("MCP cwd is not an accessible directory on this host")
	}
	t.Cwd = filepath.Clean(cwd)
	if real, err := filepath.EvalSymlinks(t.Cwd); err == nil {
		t.Cwd = real
	}
	t.ConfigFile = filepath.Join(p.NativeRoot(), "config.toml")
	if p.Harness == "claude" {
		t.ConfigFile = filepath.Join(p.NativeRoot(), ".claude.json")
		if profile.ID(p.ID) == "default" && !p.Explicit && !p.RootSet {
			home, err := os.UserHomeDir()
			if err != nil {
				return t, err
			}
			t.ConfigFile = filepath.Join(home, ".claude.json")
		}
	}
	return t, nil
}
