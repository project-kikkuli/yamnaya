package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var ymBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ym-bin")
	if err != nil {
		panic(err)
	}
	ymBin = filepath.Join(dir, "ym")
	if out, err := exec.Command("go", "build", "-o", ymBin, ".").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

var socketSeq atomic.Int64

type env struct {
	t      *testing.T
	vars   []string
	socket string
	cwd    string
	codex  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-harness")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(dir, "work")
	codex := filepath.Join(dir, "codex")
	os.MkdirAll(cwd, 0o755)
	os.MkdirAll(codex, 0o755)
	cwd, _ = filepath.EvalSymlinks(cwd)
	e := &env{t: t, socket: fmt.Sprintf("ymtest-%d-%d", os.Getpid(), socketSeq.Add(1)), cwd: cwd, codex: codex}
	e.vars = append(os.Environ(),
		"YAMNAYA_STATE_DIR="+filepath.Join(dir, "state"),
		"YAMNAYA_TMUX_SOCKET="+e.socket,
		"YAMNAYA_CLAUDE_BIN="+fake,
		"YAMNAYA_CODEX_BIN="+fake,
		"CODEX_HOME="+codex,
		"YM_PROJECT=", "YM_WINDOW=", "TMUX=",
	)
	t.Cleanup(func() { exec.Command("tmux", "-L", e.socket, "kill-server").Run() })
	return e
}

func (e *env) run(extra []string, args ...string) (string, error) {
	cmd := exec.Command(ymBin, args...)
	cmd.Dir = e.cwd
	cmd.Env = append(append([]string{}, e.vars...), extra...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (e *env) ym(args ...string) string {
	e.t.Helper()
	out, err := e.run(nil, args...)
	if err != nil {
		e.t.Fatalf("ym %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (e *env) tmux(args ...string) string {
	e.t.Helper()
	out, err := exec.Command("tmux", append([]string{"-L", e.socket}, args...)...).CombinedOutput()
	if err != nil {
		e.t.Fatalf("tmux %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

type lsWindow struct {
	Name       string `json:"name"`
	Role       string `json:"role"`
	Harness    string `json:"harness"`
	SessionID  string `json:"session_id"`
	TmuxTarget string `json:"tmux_target"`
	Alive      bool   `json:"alive"`
	OpenItems  int    `json:"open_items"`
}

type lsProject struct {
	Name      string     `json:"name"`
	Phase     string     `json:"phase"`
	OpenItems int        `json:"open_items"`
	Alive     bool       `json:"alive"`
	Windows   []lsWindow `json:"windows"`
}

func (e *env) ls() map[string]lsProject {
	e.t.Helper()
	var out struct {
		Projects []lsProject `json:"projects"`
	}
	if err := json.Unmarshal([]byte(e.ym("ls", "--json")), &out); err != nil {
		e.t.Fatal(err)
	}
	m := map[string]lsProject{}
	for _, p := range out.Projects {
		m[p.Name] = p
	}
	return m
}

func window(p lsProject, name string) lsWindow {
	for _, w := range p.Windows {
		if w.Name == name {
			return w
		}
	}
	return lsWindow{}
}

func TestNewSpawnAndLsReflectLiveTmux(t *testing.T) {
	e := newEnv(t)
	e.ym("new", "demo", "--phases", "design,build", "--prompt", "go")
	e.ym("-p", "demo", "spawn", "w1", "--harness", "codex")
	e.ym("-p", "demo", "phase", "next")

	p := e.ls()["demo"]
	if !p.Alive || p.Phase != "build" || len(p.Windows) != 2 {
		t.Fatalf("unexpected project %+v", p)
	}
	mgr := window(p, "manager")
	if !mgr.Alive || mgr.Role != "manager" || mgr.Harness != "claude" || mgr.SessionID == "" {
		t.Fatalf("unexpected manager %+v", mgr)
	}
	if env := e.tmux("show-environment", "-t", "=ym-demo", "YM_PROJECT"); env != "YM_PROJECT=demo" {
		t.Fatalf("session env %q", env)
	}
	w1 := window(p, "w1")
	if !w1.Alive || w1.Harness != "codex" {
		t.Fatalf("unexpected worker %+v", w1)
	}

	e.tmux("kill-window", "-t", w1.TmuxTarget)
	if window(e.ls()["demo"], "w1").Alive {
		t.Fatal("killed window still reported alive")
	}
	if !window(e.ls()["demo"], "manager").Alive {
		t.Fatal("manager should still be alive")
	}
}

func TestWindowsGetProjectAndWindowEnv(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe")
	script := fmt.Sprintf("#!/bin/sh\necho \"$YM_PROJECT/$YM_WINDOW\" > %s/$YM_WINDOW.env\nexec sleep 600\n", dir)
	os.WriteFile(probe, []byte(script), 0o755)
	extra := []string{"YAMNAYA_CLAUDE_BIN=" + probe}
	for _, args := range [][]string{{"new", "envp"}, {"-p", "envp", "spawn", "w2"}} {
		if out, err := e.run(extra, args...); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	for _, w := range []string{"manager", "w2"} {
		var got []byte
		for i := 0; i < 50 && len(got) == 0; i++ {
			got, _ = os.ReadFile(filepath.Join(dir, w+".env"))
			time.Sleep(20 * time.Millisecond)
		}
		if strings.TrimSpace(string(got)) != "envp/"+w {
			t.Fatalf("window %s saw %q", w, got)
		}
	}
}

func TestConcurrentItemAddProcessesGetUniqueIDs(t *testing.T) {
	e := newEnv(t)
	e.ym("new", "conc")
	const n = 40
	kinds := []string{"decision", "default", "chore"}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := map[string]bool{}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := e.run([]string{"YM_PROJECT=conc", "YM_WINDOW=manager"}, "item", "add", "--kind", kinds[i%3], "--question", fmt.Sprintf("q%d", i))
			if err != nil {
				t.Errorf("item add: %v %s", err, out)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if ids[out] {
				t.Errorf("duplicate id %s", out)
			}
			ids[out] = true
		}(i)
	}
	wg.Wait()
	if len(ids) != n {
		t.Fatalf("got %d ids, want %d", len(ids), n)
	}
	if p := e.ls()["conc"]; p.OpenItems != n || window(p, "manager").OpenItems != n {
		t.Fatalf("open counts %+v", p)
	}
	next := e.ym("-p", "conc", "item", "add", "--kind", "chore", "--question", "after")
	if next != fmt.Sprintf("C%d", n+1) {
		t.Fatalf("next id %s, want C%d", next, n+1)
	}
}

func TestItemLifecycleThroughCLI(t *testing.T) {
	e := newEnv(t)
	e.ym("new", "life")
	id := e.ym("-p", "life", "item", "add", "--kind", "decision", "--question", "Which db?", "--rec", "sqlite",
		"--option", "sqlite=embedded, no server", "--option", "postgres=needs a server", "--assumption", "single user")
	var it struct {
		Options []struct{ Label, Effect string } `json:"options"`
		State   string                           `json:"state"`
		Answer  string                           `json:"answer"`
	}
	json.Unmarshal([]byte(e.ym("-p", "life", "item", "show", strings.ToLower(id), "--json")), &it)
	if len(it.Options) != 2 || it.Options[1].Label != "postgres" || it.Options[1].Effect != "needs a server" {
		t.Fatalf("options %+v", it.Options)
	}
	e.ym("-p", "life", "item", "snooze", id)
	if out := e.ym("-p", "life", "item", "list"); out != "" {
		t.Fatalf("snoozed item listed as open: %q", out)
	}
	e.ym("-p", "life", "item", "answer", id, "use", "sqlite")
	json.Unmarshal([]byte(e.ym("-p", "life", "item", "show", id, "--json")), &it)
	if it.State != "answered" || it.Answer != "use sqlite" {
		t.Fatalf("after answer %+v", it)
	}
	if _, err := e.run(nil, "-p", "life", "item", "snooze", id); err == nil {
		t.Fatal("snoozing an answered item should fail")
	}
	if !strings.Contains(e.ym("-p", "life", "item", "list", "--all"), id) {
		t.Fatal("--all should include answered items")
	}
}

func TestLandKillsSessionAndKeepsState(t *testing.T) {
	e := newEnv(t)
	e.ym("new", "keep")
	e.ym("new", "other")
	id := e.ym("-p", "keep", "item", "add", "--kind", "chore", "--question", "tidy up")
	e.ym("land", "keep")

	if out, err := exec.Command("tmux", "-L", e.socket, "has-session", "-t", "=ym-keep").CombinedOutput(); err == nil {
		t.Fatalf("session survived land: %s", out)
	}
	if !e.ls()["other"].Alive {
		t.Fatal("landing one project touched another")
	}
	if _, ok := e.ls()["keep"]; ok {
		t.Fatal("landed project still in ls")
	}
	if !strings.Contains(e.ym("-p", "keep", "item", "show", id), "tidy up") {
		t.Fatal("item lost after land")
	}
	var kurgan []struct {
		Name      string `json:"name"`
		State     string `json:"state"`
		OpenItems int    `json:"open_items"`
	}
	json.Unmarshal([]byte(e.ym("kurgan", "--json")), &kurgan)
	if len(kurgan) != 1 || kurgan[0].Name != "keep" || kurgan[0].State != "landed" || kurgan[0].OpenItems != 1 {
		t.Fatalf("kurgan %+v", kurgan)
	}
	for _, args := range [][]string{{"land", "keep"}, {"-p", "keep", "spawn", "w"}, {"new", "keep"}, {"-p", "keep", "item", "add", "--kind", "chore", "--question", "x"}} {
		if out, err := e.run(nil, args...); err == nil {
			t.Fatalf("ym %v on a landed project succeeded: %s", args, out)
		}
	}
}

func TestJumpSwitchesClient(t *testing.T) {
	e := newEnv(t)
	e.ym("new", "hop")
	e.ym("-p", "hop", "spawn", "w1")
	e.tmux("new-session", "-d", "-s", "home", "sleep 600")
	client := exec.Command("tmux", "-L", e.socket, "-C", "attach", "-t", "=home")
	stdin, _ := client.StdinPipe()
	client.Stdout = io.Discard
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); client.Wait() })
	for i := 0; i < 50 && e.tmux("list-clients", "-F", "#{client_session}") != "home"; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	inside := []string{"TMUX=" + e.tmux("display", "-p", "-t", "=home", "#{socket_path}") + ",1,0"}

	cases := []struct {
		args   []string
		window string
	}{
		{[]string{"jump", "hop", "w1"}, "w1"},
		{[]string{"jump", "hop", "manager"}, "manager"},
	}
	for _, tc := range cases {
		if out, err := e.run(inside, tc.args...); err != nil {
			t.Fatalf("%v: %v %s", tc.args, err, out)
		}
		if got := e.tmux("list-clients", "-F", "#{client_session}:#{window_name}"); got != "ym-hop:"+tc.window {
			t.Fatalf("%v: client at %q", tc.args, got)
		}
	}
	if out, err := e.run(inside, "jump", "hop", "nope"); err == nil {
		t.Fatalf("jump to a missing window succeeded: %s", out)
	}
}

func TestCodexSessionIDIsRecordedFromCodexState(t *testing.T) {
	e := newEnv(t)
	db, err := sql.Open("sqlite", filepath.Join(e.codex, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, cwd TEXT, created_at_ms INTEGER, thread_source TEXT, first_user_message TEXT)`); err != nil {
		t.Fatal(err)
	}
	insert := func(id, cwd string, at time.Time, source, msg string) {
		if _, err := db.Exec(`INSERT INTO threads VALUES (?, ?, ?, ?, ?)`, id, cwd, at.UnixMilli(), source, msg); err != nil {
			t.Fatal(err)
		}
	}
	insert("stale", e.cwd, time.Now().Add(-time.Hour), "user", "")
	e.ym("new", "cx", "--harness", "codex", "--prompt", "plan it")
	if id := window(e.ls()["cx"], "manager").SessionID; id != "" {
		t.Fatalf("session id %q before codex wrote a thread", id)
	}
	after := time.Now().Add(time.Second)
	insert("subagent", e.cwd, after, "subagent", "")
	insert("elsewhere", "/somewhere/else", after, "user", "")
	insert("mine", e.cwd, after.Add(time.Second), "user", "You are the manager of the Yamnaya project cx. Use the ym skill to plan, spawn workers, and file open items.\n\nplan it")
	insert("sibling", e.cwd, after, "user", "something else")
	if id := window(e.ls()["cx"], "manager").SessionID; id != "mine" {
		t.Fatalf("session id %q, want mine", id)
	}
}
