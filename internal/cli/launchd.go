package cli

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// serveEnv are the variables a kept ym serve takes from the shell that keeps it, beside the login shell's environment.
var serveEnv = []string{"YAMNAYA_STATE_DIR", "YAMNAYA_YM_SOCKET", "YAMNAYA_TMUX_SOCKET", "YAMNAYA_BRIEFS_ADDR"}

// launchctl runs launchctl; a variable so tests can watch what keeping ym serve asks of launchd.
var launchctl = func(args ...string) ([]byte, error) {
	return exec.Command("launchctl", args...).CombinedOutput()
}

// agentsDir is where the per-user LaunchAgents live; a variable so tests write their plists elsewhere.
var agentsDir = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents"), nil
}

// serveLabel is the launchd label of the agent that keeps ym serve running for state: io.yamnaya.ym-serve for the default state
// directory, and one of its own for any other.
func serveLabel(state string) (string, error) {
	unit, err := serveUnit(state)
	if err != nil {
		return "", err
	}
	return "io.yamnaya." + unit, nil
}

// servePlist is the LaunchAgent that runs ym service run for state at login and whenever it exits.
func servePlist(label, self, state string, env map[string]string) string {
	var b strings.Builder
	esc := func(s string) string {
		var out bytes.Buffer
		xml.EscapeText(&out, []byte(s))
		return out.String()
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + esc(label) + `</string>
  <key>ProgramArguments</key><array>
    <string>` + esc(self) + `</string><string>service</string><string>run</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Interactive</string>
  <key>EnvironmentVariables</key><dict>
`)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		b.WriteString("    <key>" + esc(k) + "</key><string>" + esc(env[k]) + "</string>\n")
	}
	log := esc(filepath.Join(state, "work-service.log"))
	b.WriteString(`  </dict>
  <key>StandardOutPath</key><string>` + log + `</string>
  <key>StandardErrorPath</key><string>` + log + `</string>
</dict></plist>
`)
	return b.String()
}

// keptEnv is what the plist hands ym service run: the home and login shell to read the owner's environment from, and the ym
// variables that name this state directory's server, socket and brief page.
func keptEnv(state string) map[string]string {
	env := map[string]string{"HOME": os.Getenv("HOME"), "SHELL": loginShell()}
	for _, k := range serveEnv {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	delete(env, "YAMNAYA_STATE_DIR")
	if unit, err := serveUnit(state); err == nil && unit != "ym-serve" {
		env["YAMNAYA_STATE_DIR"] = state
	}
	return env
}

func loginShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/zsh"
}

// keepLaunchd writes the LaunchAgent that keeps ym serve running for this state directory and loads it into the owner's login
// session, so ym serve starts at login and comes back whenever it exits. An agent already loaded from the same plist is left running.
func keepLaunchd() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return "", err
	}
	state, err := stateDir()
	if err != nil {
		return "", err
	}
	if state, err = filepath.Abs(state); err != nil {
		return "", err
	}
	label, err := serveLabel(state)
	if err != nil {
		return "", err
	}
	dir, err := agentsDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, label+".plist")
	text := servePlist(label, self, state, keptEnv(state))
	old, _ := os.ReadFile(path)
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_, printErr := launchctl("print", domain+"/"+label)
	loaded := printErr == nil
	if loaded && string(old) == text {
		return path, nil
	}
	if loaded {
		if out, err := launchctl("bootout", domain+"/"+label); err != nil {
			return "", fmt.Errorf("launchctl bootout %s: %w: %s", label, err, strings.TrimSpace(string(out)))
		}
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	if out, err := launchctl("bootstrap", domain, path); err != nil {
		return "", fmt.Errorf("launchctl bootstrap %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return path, nil
}

// unkeepLaunchd unloads and removes this state directory's LaunchAgent; ym serve keeps running until it next exits.
func unkeepLaunchd() (string, error) {
	state, err := stateDir()
	if err != nil {
		return "", err
	}
	label, err := serveLabel(state)
	if err != nil {
		return "", err
	}
	dir, err := agentsDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, label+".plist")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
	if _, err := launchctl("print", target); err == nil {
		if out, err := launchctl("bootout", target); err != nil {
			return "", fmt.Errorf("launchctl bootout %s: %w: %s", label, err, strings.TrimSpace(string(out)))
		}
	}
	return path, nil
}

// keepDefault keeps the default state directory's ym serve running across logins and restarts on macOS, set up the first time the
// board or ym install runs there, so the owner never sets it up.
func keepDefault(w io.Writer) {
	if runtime.GOOS != "darwin" || os.Getenv("YAMNAYA_NO_SERVE") == "1" {
		return
	}
	state, err := stateDir()
	if err != nil {
		return
	}
	if unit, err := serveUnit(state); err != nil || unit != "ym-serve" {
		return
	}
	if _, err := keepLaunchd(); err != nil {
		fmt.Fprintln(w, "ym serve is not kept across restarts:", err)
	}
	for _, err := range dropDeadAgents() {
		fmt.Fprintln(w, err)
	}
}

var plistString = regexp.MustCompile(`<string>([^<]*)</string>`)
var plistStateDir = regexp.MustCompile(`<key>YAMNAYA_STATE_DIR</key><string>([^<]*)</string>`)

// dropDeadAgents unloads and removes every other state directory's agent whose ym binary or state directory is gone, as a
// rehearsal in a deleted scratch directory leaves, so launchd stops retrying it at every login.
func dropDeadAgents() []error {
	dir, err := agentsDir()
	if err != nil {
		return []error{err}
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "io.yamnaya.ym-serve-*.plist"))
	var errs []error
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		strs := plistString.FindAllSubmatch(raw, 3)
		if len(strs) < 2 {
			continue
		}
		dead := missingPath(unescape(string(strs[1][1])))
		if m := plistStateDir.FindSubmatch(raw); m != nil {
			dead = dead || missingPath(unescape(string(m[1])))
		}
		if !dead {
			continue
		}
		target := fmt.Sprintf("gui/%d/%s", os.Getuid(), unescape(string(strs[0][1])))
		if _, err := launchctl("print", target); err == nil {
			if out, err := launchctl("bootout", target); err != nil {
				errs = append(errs, fmt.Errorf("launchctl bootout %s: %w: %s", target, err, strings.TrimSpace(string(out))))
				continue
			}
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errs
}

func missingPath(p string) bool {
	_, err := os.Stat(p)
	return errors.Is(err, os.ErrNotExist)
}

func unescape(s string) string {
	var out string
	if xml.Unmarshal([]byte("<s>"+s+"</s>"), &out) != nil {
		return s
	}
	return out
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// loginEnv is the environment the owner's login shell builds, so a ym serve launchd starts finds claude, codex, git and gh, and
// their credentials, as a terminal would.
func loginEnv(shell string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, shell, "-l", "-c", "/usr/bin/env").Output()
	if err != nil {
		return nil, fmt.Errorf("%s -l: %w", shell, err)
	}
	var env []string
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case envName.MatchString(line):
			env = append(env, line)
		case len(env) > 0 && line != "":
			env[len(env)-1] += "\n" + line
		}
	}
	return env, nil
}

// serviceRun replaces itself with ym serve --wait in the login shell's environment, keeping the ym variables launchd set. For a
// state directory that was deleted it removes its own agent instead, so launchd stops starting a serve that would recreate it.
func serviceRun(w io.Writer) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if state, err := stateDir(); err == nil && deletedState(state) {
		fmt.Fprintf(w, "%s was deleted; ym stops keeping its serve running\n", state)
		_, err := unkeepLaunchd()
		return err
	}
	if state, err := stateDir(); err == nil {
		if unit, err := serveUnit(state); err == nil && unit == "ym-serve" {
			for _, err := range dropDeadAgents() {
				fmt.Fprintln(w, err)
			}
		}
	}
	env, err := loginEnv(loginShell())
	if err != nil {
		fmt.Fprintln(w, "ym serve runs in launchd's environment:", err)
		env = os.Environ()
	}
	set := map[string]string{}
	for _, k := range append([]string{"HOME"}, serveEnv...) {
		if v, ok := os.LookupEnv(k); ok {
			set[k] = v
		}
	}
	path := filepath.Dir(self)
	env = slices.DeleteFunc(env, func(kv string) bool {
		k, v, _ := strings.Cut(kv, "=")
		if k == "PATH" {
			path += ":" + v
			return true
		}
		_, over := set[k]
		return over
	})
	env = append(env, "PATH="+path)
	for k, v := range set {
		env = append(env, k+"="+v)
	}
	return syscall.Exec(self, []string{self, "serve", "--wait"}, env)
}

// launchdState is how launchd stands with this state directory's agent: its plist path, whether it is loaded, and its pid.
func launchdState() (string, bool, int) {
	state, err := stateDir()
	if err != nil {
		return "", false, 0
	}
	label, err := serveLabel(state)
	if err != nil {
		return "", false, 0
	}
	dir, _ := agentsDir()
	out, err := launchctl("print", fmt.Sprintf("gui/%d/%s", os.Getuid(), label))
	if err != nil {
		return filepath.Join(dir, label+".plist"), false, 0
	}
	pid := 0
	if m := regexp.MustCompile(`(?m)^\s*pid = (\d+)`).FindSubmatch(out); m != nil {
		pid, _ = strconv.Atoi(string(m[1]))
	}
	return filepath.Join(dir, label+".plist"), true, pid
}

// deletedState reports whether dir is a state directory other than the owner's that no longer exists, as a finished rehearsal leaves.
func deletedState(dir string) bool {
	if unit, err := serveUnit(dir); err != nil || unit == "ym-serve" {
		return false
	}
	_, err := os.Stat(dir)
	return errors.Is(err, os.ErrNotExist)
}

// keptServe is the label of the launchd agent keeping ym serve for this state directory, when one is loaded.
func keptServe() (string, bool) {
	state, err := stateDir()
	if err != nil {
		return "", false
	}
	label, err := serveLabel(state)
	if err != nil {
		return "", false
	}
	if _, err := launchctl("print", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)); err != nil {
		return "", false
	}
	return label, true
}

// kickstart asks launchd to start its agent label unless it already runs; a serve it starts waits for any other to exit.
func kickstart(label string) error {
	if out, err := launchctl("kickstart", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)); err != nil {
		return fmt.Errorf("launchctl kickstart %s: %w: %s", label, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// scrubAgentEnv drops from this process's environment what it inherited from an agent's session, so a serve started from an
// agent's shell, its restarts and what it starts carry none of that session's identity.
func scrubAgentEnv() {
	keep := tmux.AgentEnv(os.Environ())
	os.Clearenv()
	for _, kv := range keep {
		if k, v, ok := strings.Cut(kv, "="); ok {
			os.Setenv(k, v)
		}
	}
}
