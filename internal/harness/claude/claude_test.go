package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func fixture(t *testing.T) *Adapter {
	t.Helper()
	return &Adapter{
		Dir: filepath.Join("testdata", "home"),
		Bin: "claude",
		Cwd: func(int) string { return "" },
		Now: time.Now,
	}
}

func p(pid int, args ...string) proc.Proc {
	return proc.Proc{PID: pid, PPID: 1, TTY: "/dev/ttys00" + string(rune('0'+pid%10)), Args: args}
}

func byPID(sessions []core.Session) map[int]core.Session {
	m := map[int]core.Session{}
	for _, s := range sessions {
		m[s.PID] = s
	}
	return m
}

func TestDiscoverReadsStateTranscriptAndCrew(t *testing.T) {
	a := fixture(t)
	open := filepath.Join(a.Dir, "projects", "-Users-me-src-app", "33333333-cccc-4ccc-8ccc-333333333333.jsonl")
	info, err := os.Stat(open)
	must(t, err)
	a.Now = func() time.Time { return info.ModTime().Add(time.Minute) }
	got := byPID(a.Discover([]proc.Proc{
		p(101, "claude"),
		p(102, "claude", "--continue"),
		p(103, "claude", "--resume", "33333333-cccc-4ccc-8ccc-333333333333"),
		p(104, "claude"),
	}))
	waiting := got[101]
	if waiting.ID != "11111111-aaaa-4aaa-8aaa-111111111111" || waiting.Title != "Flaky login test" || waiting.Status != core.Waiting ||
		waiting.WaitingFor != "permission prompt" || waiting.Cwd != "/Users/me/src/app" {
		t.Errorf("101 = %+v", waiting)
	}
	if len(waiting.Crew) != 2 {
		t.Fatalf("101 crew = %+v, want the two readable agents", waiting.Crew)
	}
	crew := map[string]core.Member{}
	for _, m := range waiting.Crew {
		crew[m.Name] = m
	}
	if m := crew["reviewer"]; !m.Teammate || m.Status != core.Idle {
		t.Errorf("teammate = %+v", m)
	}
	if m := crew["Run the test suite"]; m.Teammate || m.Status != core.Done {
		t.Errorf("subagent = %+v", m)
	}
	if s := got[102]; s.Title != "Release notes" || s.Status != core.Idle {
		t.Errorf("102 = %+v", s)
	}
	if s := got[103]; s.ID != "33333333-cccc-4ccc-8ccc-333333333333" || s.Title != "Plan the migration" || s.Status != core.Busy {
		t.Errorf("corrupt state file should fall back to argv and transcript; 103 = %+v", s)
	}
	if s := got[104]; s.ID != "" || s.Status != core.Unknown || s.TTY == "" {
		t.Errorf("unreadable session should stay listed with unknown fields; 104 = %+v", s)
	}
}

func TestBusySessionHasBusyCrewMidToolCall(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "projects", "-Users-me-x", "44444444-dddd-4ddd-8ddd-444444444444", "subagents")
	must(t, os.MkdirAll(sub, 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "sessions"), 0o755))
	write(t, filepath.Join(dir, "sessions", "7.json"), `{"pid":7,"sessionId":"44444444-dddd-4ddd-8ddd-444444444444","cwd":"/Users/me/x","status":"busy"}`)
	write(t, filepath.Join(dir, "projects", "-Users-me-x", "44444444-dddd-4ddd-8ddd-444444444444.jsonl"), `{"type":"user","message":{"content":"go"}}`+"\n")
	write(t, filepath.Join(sub, "agent-b1.meta.json"), `{"description":"Build it"}`)
	write(t, filepath.Join(sub, "agent-b1.jsonl"), `{"type":"assistant","message":{"content":[],"stop_reason":"tool_use"}}`+"\n")
	a := &Adapter{Dir: dir, Now: time.Now}
	s := a.Discover([]proc.Proc{p(7, "claude")})[0]
	if s.Status != core.Busy || len(s.Crew) != 1 || s.Crew[0].Status != core.Busy {
		t.Fatalf("session = %+v", s)
	}
}

func TestSessionIDFromStateThenArgv(t *testing.T) {
	a := fixture(t)
	cases := []struct {
		proc proc.Proc
		want string
	}{
		{p(101, "claude"), "11111111-aaaa-4aaa-8aaa-111111111111"},
		{p(103, "claude", "-r", "abc"), "abc"},
		{p(105, "claude", "--session-id=def"), "def"},
		{p(106, "claude"), ""},
	}
	for _, c := range cases {
		if got := a.SessionID(c.proc); got != c.want {
			t.Errorf("SessionID(%v) = %q, want %q", c.proc.Args, got, c.want)
		}
	}
}

func TestOwnsSessionProcessesOnly(t *testing.T) {
	a := fixture(t)
	cases := map[string]struct {
		args []string
		want bool
	}{
		"interactive":   {[]string{"/opt/bin/claude", "--resume", "x"}, true},
		"headless":      {[]string{"claude", "-p", "do it"}, true},
		"node launcher": {[]string{"node", "/usr/lib/claude", "--continue"}, true},
		"prompt words":  {[]string{"claude", "fix", "it", "then", "update", "mcp"}, true},
		"subcommand":    {[]string{"claude", "update"}, false},
		"chrome host":   {[]string{"claude", "--chrome-native-host"}, false},
		"mcp server":    {[]string{"claude", "mcp", "serve"}, false},
		"other program": {[]string{"codex"}, false},
	}
	for name, c := range cases {
		if got := a.Owns(proc.Proc{PID: 1, Args: c.args}); got != c.want {
			t.Errorf("%s: Owns = %v, want %v", name, got, c.want)
		}
	}
}

func TestResumeNamesTheSession(t *testing.T) {
	got := fixture(t).Resume("abc")
	if len(got) != 3 || got[1] != "--resume" || got[2] != "abc" {
		t.Fatalf("Resume = %v", got)
	}
}

func dialog(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".txt"))
	must(t, err)
	return string(b)
}

func TestParsePromptFromRealDialogs(t *testing.T) {
	cases := map[string]core.Prompt{
		"claude-bash":     {Tool: "Bash", Detail: "rm -rf build/ && mkdir build"},
		"claude-write":    {Tool: "Write", Detail: "notes.txt"},
		"claude-trust":    {Tool: "trust folder", Detail: "/private/tmp/ymperm.yY4Y"},
		"claude-subagent": {Tool: "Bash", Detail: "rm -rf build/ && mkdir build", From: "general-purpose agent"},
	}
	for name, want := range cases {
		got, ok := ParsePrompt(dialog(t, name))
		if !ok || got != want {
			t.Errorf("%s: ParsePrompt = %+v, %v; want %+v", name, got, ok, want)
		}
	}
}

func TestParsePromptIgnoresScreensWithoutLiveDialog(t *testing.T) {
	scrolled := dialog(t, "claude-bash")
	for range footerLines + 1 {
		scrolled += "⏺ later output\n"
	}
	for name, text := range map[string]string{
		"empty":    "",
		"working":  "❯ fix the tests\n⏺ Running the suite\n  ⎿  ok\n",
		"scrolled": scrolled,
	} {
		if got, ok := ParsePrompt(text); ok {
			t.Errorf("%s: found %+v", name, got)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	must(t, os.WriteFile(path, []byte(data), 0o644))
}
