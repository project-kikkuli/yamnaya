package cli

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeLaunchd stands in for launchctl: it records what it is asked and loads and unloads labels.
type fakeLaunchd struct {
	calls  []string
	loaded map[string]bool
}

func (f *fakeLaunchd) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, args[0])
	switch args[0] {
	case "print":
		if f.loaded[args[1]] {
			return []byte("state = running\n\tpid = 4242\n"), nil
		}
		return []byte("Could not find service"), errors.New("exit status 113")
	case "bootout":
		delete(f.loaded, args[1])
	case "bootstrap":
		raw, err := os.ReadFile(args[2])
		if err != nil {
			return nil, err
		}
		label := strings.TrimSuffix(filepath.Base(args[2]), ".plist")
		if !strings.Contains(string(raw), "<string>"+label+"</string>") {
			return []byte("label mismatch"), errors.New("exit status 5")
		}
		f.loaded[args[1]+"/"+label] = true
	}
	return nil, nil
}

func fakeLaunchctl(t *testing.T) (*fakeLaunchd, string) {
	t.Helper()
	f := &fakeLaunchd{loaded: map[string]bool{}}
	dir := t.TempDir()
	run, agents := launchctl, agentsDir
	launchctl, agentsDir = f.run, func() (string, error) { return dir, nil }
	t.Cleanup(func() { launchctl, agentsDir = run, agents })
	return f, dir
}

func TestKeepingServeLoadsOneAgentPerStateDirAndLeavesALoadedOneRunning(t *testing.T) {
	f, agents := fakeLaunchctl(t)
	state := t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", state)
	t.Setenv("YAMNAYA_YM_SOCKET", "rehearsal")
	path, err := keepLaunchd()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != agents || !slices.Equal(f.calls, []string{"print", "bootstrap"}) {
		t.Fatalf("first keep wrote %s and ran launchctl %q, want a plist in %s loaded once", path, f.calls, agents)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"<key>RunAtLoad</key><true/>", "<key>KeepAlive</key><true/>", "<string>service</string><string>run</string>",
		"<key>YAMNAYA_STATE_DIR</key><string>" + state + "</string>", "<key>YAMNAYA_YM_SOCKET</key><string>rehearsal</string>",
		"<key>SHELL</key>", filepath.Join(state, "work-service.log")} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("plist lacks %s:\n%s", want, raw)
		}
	}
	if lint, err := exec.LookPath("plutil"); err == nil {
		if out, err := exec.Command(lint, "-lint", path).CombinedOutput(); err != nil {
			t.Fatalf("plutil rejects the plist: %s", out)
		}
	}
	for _, step := range []struct {
		name  string
		edit  func()
		calls []string
	}{
		{name: "kept again unchanged", calls: []string{"print"}},
		{name: "plist from an older ym", edit: func() { os.WriteFile(path, []byte("old"), 0o644) }, calls: []string{"print", "bootout", "bootstrap"}},
		{name: "unloaded by hand", edit: func() { clear(f.loaded) }, calls: []string{"print", "bootstrap"}},
	} {
		t.Run(step.name, func(t *testing.T) {
			if step.edit != nil {
				step.edit()
			}
			f.calls = nil
			if _, err := keepLaunchd(); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(f.calls, step.calls) {
				t.Fatalf("launchctl %q, want %q", f.calls, step.calls)
			}
		})
	}
	if _, loaded, pid := launchdState(); !loaded || pid != 4242 {
		t.Fatalf("service status sees loaded %v pid %d, want the agent's pid", loaded, pid)
	}
	other := t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", other)
	second, err := keepLaunchd()
	if err != nil {
		t.Fatal(err)
	}
	if second == path || len(f.loaded) != 2 {
		t.Fatalf("a second state directory's agent %s replaced the first's %s; loaded %v", second, path, f.loaded)
	}
	if _, err := unkeepLaunchd(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(second); !errors.Is(err, os.ErrNotExist) || len(f.loaded) != 1 {
		t.Fatalf("unkeep left %s (stat %v) or unloaded the wrong agent: %v", second, err, f.loaded)
	}
}

func TestTheDefaultStateDirsAgentNamesNoStateDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		state string
		named bool
	}{
		{filepath.Join(home, ".local", "state", "yamnaya"), false},
		{"/tmp/rehearsal/state", true},
	} {
		t.Setenv("YAMNAYA_STATE_DIR", "relative/elsewhere")
		_, named := keptEnv(tc.state)["YAMNAYA_STATE_DIR"]
		if named != tc.named || (named && keptEnv(tc.state)["YAMNAYA_STATE_DIR"] != tc.state) {
			t.Errorf("%s: agent names a state dir %v (%q), want %v and its absolute path", tc.state, named, keptEnv(tc.state)["YAMNAYA_STATE_DIR"], tc.named)
		}
	}
}

func TestAKeptServeTakesTheLoginShellsEnvironment(t *testing.T) {
	shell := filepath.Join(t.TempDir(), "sh")
	script := "#!/bin/sh\n[ \"$1\" = -l ] || exit 9\nprintf 'Welcome back\\nPATH=/opt/tools/bin:/usr/bin\\nNOTE=two\\nlines\\nGH_TOKEN=abc\\n'\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env, err := loginEnv(shell)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/opt/tools/bin:/usr/bin", "NOTE=two\nlines", "GH_TOKEN=abc"}
	if !slices.Equal(env, want) {
		t.Fatalf("login environment %q, want %q", env, want)
	}
}

func TestADeletedStateDirsAgentRemovesItselfInsteadOfRecreatingIt(t *testing.T) {
	f, _ := fakeLaunchctl(t)
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YAMNAYA_STATE_DIR", state)
	path, err := keepLaunchd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(state); err != nil {
		t.Fatal(err)
	}
	if err := serviceRun(io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) || len(f.loaded) != 0 {
		t.Fatalf("agent for deleted %s still kept: plist stat %v, loaded %v", state, err, f.loaded)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("service run recreated %s", state)
	}
}
