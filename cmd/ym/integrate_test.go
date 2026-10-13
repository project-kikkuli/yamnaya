package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recorder writes a harness that appends its argv to <dir>/<window>.argv, runs body, then sleeps.
func recorder(t *testing.T, body string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "harness")
	script := "#!/bin/sh\necho \"$*\" >> " + dir + "/$YM_WINDOW.argv\n" + body + "\nexec sleep 600\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func lastArgv(t *testing.T, dir, window string) string {
	t.Helper()
	var lines []string
	for i := 0; i < 100; i++ {
		data, _ := os.ReadFile(filepath.Join(dir, window+".argv"))
		if lines = strings.Split(strings.TrimSpace(string(data)), "\n"); lines[0] != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return lines[len(lines)-1]
}

func waitArgvLines(t *testing.T, dir, window string, n int) string {
	t.Helper()
	for i := 0; i < 100; i++ {
		data, _ := os.ReadFile(filepath.Join(dir, window+".argv"))
		if lines := strings.Split(strings.TrimSpace(string(data)), "\n"); len(lines) >= n {
			return lines[len(lines)-1]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("window %s never launched %d times", window, n)
	return ""
}

func codexThreads(t *testing.T, e *env) func(id, msg string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(e.codex, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, cwd TEXT, created_at_ms INTEGER, thread_source TEXT, first_user_message TEXT)`); err != nil {
		t.Fatal(err)
	}
	return func(id, msg string) {
		if _, err := db.Exec(`INSERT INTO threads VALUES (?, ?, ?, 'user', ?)`, id, e.cwd, time.Now().Add(time.Second).UnixMilli(), msg); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReviveResumesEveryWindowSession(t *testing.T) {
	cases := []struct {
		name string
		kill func(e *env, project string)
	}{
		{"landed", func(e *env, p string) { e.ym("land", p) }},
		{"dead tmux", func(e *env, p string) { e.tmux("kill-session", "-t", "=ym-"+p) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			bin, dir := recorder(t, "")
			extra := []string{"YAMNAYA_CLAUDE_BIN=" + bin, "YAMNAYA_CODEX_BIN=" + bin}
			thread := codexThreads(t, e)
			for _, args := range [][]string{{"new", "rv"}, {"-p", "rv", "spawn", "w1"}, {"-p", "rv", "spawn", "cx", "--harness", "codex", "--prompt", "port it"}} {
				if out, err := e.run(extra, args...); err != nil {
					t.Fatalf("%v: %v %s", args, err, out)
				}
			}
			thread("cx-thread", "You are the worker cx in the Yamnaya project rv. Use the ym skill to file open items.\n\nport it")
			before := e.ls()["rv"]
			tc.kill(e, "rv")

			if out, err := e.run(extra, "revive", "rv"); err != nil {
				t.Fatalf("revive: %v %s", err, out)
			}
			after := e.ls()["rv"]
			if !after.Alive || len(after.Windows) != 3 {
				t.Fatalf("after revive %+v", after)
			}
			for _, w := range before.Windows {
				got := window(after, w.Name)
				if !got.Alive || got.Harness != w.Harness {
					t.Fatalf("window %s after revive %+v", w.Name, got)
				}
				want := bin + " --resume " + w.SessionID
				if w.Harness == "codex" {
					want = bin + " resume cx-thread"
				}
				if argv := bin + " " + waitArgvLines(t, dir, w.Name, 2); argv != want {
					t.Fatalf("window %s relaunched as %q, want %q", w.Name, argv, want)
				}
			}
			var kurgan []any
			json.Unmarshal([]byte(e.ym("kurgan", "--json")), &kurgan)
			if len(kurgan) != 0 {
				t.Fatalf("revived project still in kurgan: %v", kurgan)
			}
			if out, err := e.run(extra, "revive", "rv"); err == nil {
				t.Fatalf("reviving a live project succeeded: %s", out)
			}
		})
	}
}

func TestSwitchPortsIdleWindowAndRefusesBusyOne(t *testing.T) {
	e := newEnv(t)
	quiet, dir := recorder(t, "")
	busy, busyDir := recorder(t, "while :; do echo tick; sleep 0.2; done")
	csDir := t.TempDir()
	cs := filepath.Join(csDir, "cs")
	os.WriteFile(cs, []byte("#!/bin/sh\necho \"$@\" > "+csDir+"/args\necho porting >&2\necho ported-$2\n"), 0o755)
	extra := []string{"YAMNAYA_CLAUDE_BIN=" + quiet, "YAMNAYA_CODEX_BIN=" + quiet, "YAMNAYA_CS_BIN=" + cs}
	if out, err := e.run(extra, "new", "sw"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if out, err := e.run(append(extra, "YAMNAYA_CLAUDE_BIN="+busy), "-p", "sw", "spawn", "loud"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	lastArgv(t, busyDir, "loud")
	mgr := window(e.ls()["sw"], "manager")
	time.Sleep(2100 * time.Millisecond)

	if out, err := e.run(extra, "-p", "sw", "switch", "loud", "--to", "codex", "--min-idle", "2s"); err == nil || !strings.Contains(out, "busy") {
		t.Fatalf("switching a busy window: err=%v out=%s", err, out)
	}
	if out, err := e.run(extra, "-p", "sw", "switch", "manager", "--to", "claude"); err == nil {
		t.Fatalf("switching to the same harness succeeded: %s", out)
	}
	if out, err := e.run(extra, "-p", "sw", "switch", "manager", "--to", "codex", "--min-idle", "2s"); err != nil {
		t.Fatalf("switch: %v %s", err, out)
	}

	args, _ := os.ReadFile(filepath.Join(csDir, "args"))
	if strings.TrimSpace(string(args)) != "port "+mgr.SessionID+" --to codex" {
		t.Fatalf("cs called with %q", args)
	}
	if argv := waitArgvLines(t, dir, "manager", 2); argv != "resume ported-"+mgr.SessionID {
		t.Fatalf("manager relaunched as %q", argv)
	}
	got := window(e.ls()["sw"], "manager")
	if got.Harness != "codex" || got.SessionID != "ported-"+mgr.SessionID || got.TmuxTarget != mgr.TmuxTarget || !got.Alive {
		t.Fatalf("manager after switch %+v (was %+v)", got, mgr)
	}
	if loud := window(e.ls()["sw"], "loud"); loud.Harness != "claude" {
		t.Fatalf("busy window changed: %+v", loud)
	}
}

func TestInstallLinksSkillAndWritesTmuxConf(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755)
	os.Symlink("/elsewhere", filepath.Join(home, ".claude", "skills", "ym"))
	run := func() string {
		cmd := exec.Command(ymBin, "install")
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("install: %v %s", err, out)
		}
		return string(out)
	}
	run()
	out := run()
	for _, dir := range []string{".agents", ".claude", ".codex"} {
		data, err := os.ReadFile(filepath.Join(home, dir, "skills", "ym", "SKILL.md"))
		if err != nil || !strings.Contains(string(data), "name: ym") {
			t.Fatalf("%s skill: %v %q", dir, err, data)
		}
	}
	if dst, _ := os.Readlink(filepath.Join(home, ".codex", "skills", "ym")); dst != filepath.Join(home, ".agents", "skills", "ym") {
		t.Fatalf("codex link points at %q", dst)
	}
	conf, _ := os.ReadFile(filepath.Join(home, ".config", "yamnaya", "tmux.conf"))
	for _, want := range []string{"bind-key Space display-popup -E -w 80% -h 70% '", " board'", "bind-key n run-shell '", " next'"} {
		if !strings.Contains(string(conf), want) {
			t.Fatalf("tmux.conf missing %q:\n%s", want, conf)
		}
	}
	if !strings.Contains(out, "add to ~/.tmux.conf") {
		t.Fatalf("install did not ask for the source line: %s", out)
	}
	os.WriteFile(filepath.Join(home, ".tmux.conf"), []byte("source-file -q ~/.config/yamnaya/tmux.conf\n"), 0o644)
	if strings.Contains(run(), "add to ~/.tmux.conf") {
		t.Fatal("install asked for a source line that is already present")
	}
	os.RemoveAll(filepath.Join(home, ".codex", "skills", "ym"))
	os.MkdirAll(filepath.Join(home, ".codex", "skills", "ym"), 0o755)
	cmd := exec.Command(ymBin, "install")
	cmd.Env = append(os.Environ(), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("install replaced a real directory: %s", out)
	}
}
