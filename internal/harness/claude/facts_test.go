package claude

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

const sid = "55555555-eeee-4eee-8eee-555555555555"

// world lays out one Claude session with a transcript and subagents under a temp CLAUDE_CONFIG_DIR.
type world struct {
	dir, transcript, subagents string
}

func newWorld(t *testing.T, lines ...string) world {
	t.Helper()
	dir := t.TempDir()
	w := world{dir: dir, transcript: filepath.Join(dir, "projects", "-Users-me-app", sid+".jsonl")}
	w.subagents = filepath.Join(strings.TrimSuffix(w.transcript, ".jsonl"), "subagents")
	must(t, os.MkdirAll(w.subagents, 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "sessions"), 0o755))
	write(t, filepath.Join(dir, "sessions", "7.json"), `{"pid":7,"sessionId":"`+sid+`","cwd":"/Users/me/app","status":"busy"}`)
	write(t, w.transcript, strings.Join(lines, "\n")+"\n")
	return w
}

func (w world) agent(t *testing.T, id, meta string, lines ...string) {
	t.Helper()
	write(t, filepath.Join(w.subagents, "agent-"+id+".meta.json"), meta)
	write(t, filepath.Join(w.subagents, "agent-"+id+".jsonl"), strings.Join(lines, "\n")+"\n")
}

func (w world) session(t *testing.T, cache core.ScanCache) core.Session {
	t.Helper()
	a := &Adapter{Dir: w.dir, Now: time.Now, Cache: cache}
	return a.Discover([]proc.Proc{p(7, "claude")})[0]
}

func human(text string) string {
	return `{"type":"user","origin":{"kind":"human"},"message":{"content":` + quote(text) + `}}`
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

func toolUse(id, name, input string) string {
	return `{"type":"assistant","cwd":"/Users/me/app","timestamp":"2026-10-15T10:00:00Z","message":{"model":"claude-opus-5-5","content":[{"type":"tool_use","id":"` +
		id + `","name":"` + name + `","input":` + input + `}],"stop_reason":"tool_use"}}`
}

func toolResult(id, text string) string {
	return `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"` + id + `","content":` + quote(text) + `}]}}`
}

func TestSessionFactsComeFromTheTranscript(t *testing.T) {
	w := newWorld(t,
		human("Ship the login fix"),
		toolUse("t1", "Edit", `{"file_path":"/Users/me/app/internal/login.go"}`),
		`{"type":"pr-link","prNumber":12,"prUrl":"https://github.com/me/app/pull/12","prRepository":"me/app"}`,
		`{"type":"pr-link","prNumber":40,"prUrl":"https://github.com/me/lib/pull/40","prRepository":"me/lib"}`,
		`{"type":"user","message":{"content":"Another Claude session sent a message: <teammate-message>done</teammate-message>"}}`,
		human("now update the changelog"),
		toolUse("t2", "Bash", `{"command":"cd /Users/me/app && gh pr create --fill\nsleep 1"}`),
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Opened the PR."}],"stop_reason":"end_turn"}}`,
	)
	s := w.session(t, nil)
	if s.Task != "Ship the login fix" || s.Ask != "now update the changelog" {
		t.Errorf("task %q, ask %q; want the first and the latest prompt a person typed", s.Task, s.Ask)
	}
	if got := s.Doing.String(); got != "Bash: gh pr create --fill" {
		t.Errorf("doing = %q", got)
	}
	if s.Result != "Opened the PR." || s.Model != "claude-opus-5-5" || s.Tools["Edit"] != 1 || s.Tools["Bash"] != 1 {
		t.Errorf("facts = %+v", s.Facts)
	}
	if len(s.PRs) != 2 || s.PRs[0].Number != 12 || s.PRs[1].Repo != "me/lib" {
		t.Errorf("PRs = %+v, want both pr-link records, including one for another repo", s.PRs)
	}
}

func TestSubagentBriefResultAndTheWorkItDid(t *testing.T) {
	w := newWorld(t, human("coordinate"))
	w.agent(t, "x1", `{"agentType":"general-purpose","description":"Fix the parser","model":"opus"}`,
		`{"type":"user","isSidechain":true,"message":{"content":"Fix forward.\n\nRepair the parser in internal/parse."}}`,
		toolUse("u1", "Write", `{"file_path":"/Users/me/app-wt-parse/internal/parse/parse.go"}`),
		toolUse("u2", "Edit", `{"file_path":"/Users/me/app-wt-parse/internal/parse/parse_test.go"}`),
		toolUse("u3", "Bash", `{"command":"gh pr create --title parser"}`),
		toolResult("u3", "Creating pull request... https://github.com/me/app/pull/77"),
		`{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"text","text":"Parser fixed in PR 77."}],"stop_reason":"end_turn"}}`,
	)
	s := w.session(t, nil)
	if len(s.Crew) != 1 {
		t.Fatalf("crew = %+v", s.Crew)
	}
	m := s.Crew[0]
	if m.ID != "x1" || m.Type != "general-purpose" || m.Description != "Fix the parser" || m.Model != "claude-opus-5-5" {
		t.Errorf("member = %+v", m)
	}
	if m.Brief != "Fix forward.\n\nRepair the parser in internal/parse." || m.Result != "Parser fixed in PR 77." {
		t.Errorf("brief %q result %q", m.Brief, m.Result)
	}
	if len(m.Files) != 2 || m.Files[0] != "/Users/me/app-wt-parse/internal/parse/parse.go" || len(m.Trail) != 3 {
		t.Errorf("files %v trail %v", m.Files, m.Trail)
	}
	if len(m.PRs) != 1 || m.PRs[0].URL != "https://github.com/me/app/pull/77" {
		t.Errorf("PRs = %+v, want the one its gh pr create printed", m.PRs)
	}
	if got := s.AllPRs(); len(got) != 1 {
		t.Errorf("session PRs = %+v, want the subagent's PR attributed to its lead", got)
	}
}

type memCache map[string]struct {
	off  int64
	data []byte
}

func (c memCache) LoadScan(path string) (int64, []byte, bool) {
	e, ok := c[path]
	return e.off, e.data, ok
}

func (c memCache) SaveScan(path string, off int64, data []byte) error {
	c[path] = struct {
		off  int64
		data []byte
	}{off, data}
	return nil
}

func TestAFreshRunResumesFromTheSavedOffset(t *testing.T) {
	w := newWorld(t, human("first ask"), toolUse("t1", "Read", `{"file_path":"/Users/me/app/go.mod"}`))
	cache := memCache{}
	w.session(t, cache)
	saved := cache[w.transcript].off
	f, err := os.OpenFile(w.transcript, os.O_APPEND|os.O_WRONLY, 0)
	must(t, err)
	_, err = f.WriteString(human("second ask") + "\n")
	must(t, err)
	must(t, f.Close())
	s := w.session(t, cache)
	if s.Task != "first ask" || s.Ask != "second ask" || s.Tools["Read"] != 1 {
		t.Fatalf("resumed facts = task %q ask %q tools %v", s.Task, s.Ask, s.Tools)
	}
	if cache[w.transcript].off <= saved {
		t.Fatalf("offset %d did not advance past %d", cache[w.transcript].off, saved)
	}
}

func TestAGrowingTranscriptIsSavedOnceAMinuteAndStillResumesWhole(t *testing.T) {
	w := newWorld(t, human("first ask"))
	cache := memCache{}
	a := &Adapter{Dir: w.dir, Now: time.Now, Cache: cache}
	a.Discover([]proc.Proc{p(7, "claude")})
	saved := cache[w.transcript].off
	for _, ask := range []string{"second ask", "third ask"} {
		f, err := os.OpenFile(w.transcript, os.O_APPEND|os.O_WRONLY, 0)
		must(t, err)
		_, err = f.WriteString(human(ask) + "\n")
		must(t, err)
		must(t, f.Close())
		if s := a.Discover([]proc.Proc{p(7, "claude")})[0]; s.Ask != ask {
			t.Fatalf("live ask = %q, want %q", s.Ask, ask)
		}
	}
	if cache[w.transcript].off != saved {
		t.Fatalf("saved offset moved to %d within a minute, want it held at %d", cache[w.transcript].off, saved)
	}
	if s := w.session(t, cache); s.Task != "first ask" || s.Ask != "third ask" {
		t.Fatalf("fresh run from the held save = task %q ask %q", s.Task, s.Ask)
	}
}

func TestASubagentIsCutOnlyWhenItStoppedMidTurn(t *testing.T) {
	ended := `{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn"}}`
	cases := []struct {
		name  string
		lines []string
		cut   bool
	}{
		{"replied and ended its turn", []string{human("go"), ended}, false},
		{"handed back its report", []string{human("go"), toolUse("h1", "SubagentHandback", `{"message":"report"}`),
			toolResult("h1", `{"success":true,"message":"Report delivered to your parent"}`)}, false},
		{"interrupted", []string{human("go"), toolUse("b1", "Bash", `{"command":"go test ./..."}`),
			`{"type":"user","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`}, true},
		{"killed waiting on a tool", []string{human("go"), toolUse("b1", "Bash", `{"command":"go test ./..."}`)}, true},
		{"denied a tool", []string{human("go"), toolUse("e1", "Edit", `{"file_path":"/x"}`), toolResult("e1", "Permission for this tool use was denied.")}, true},
		{"kept working after a handback", []string{human("go"), toolUse("h1", "SubagentHandback", `{"message":"report"}`),
			toolResult("h1", "ok"), toolUse("b1", "Bash", `{"command":"ls"}`), toolResult("b1", "a")}, true},
	}
	for _, c := range cases {
		w := newWorld(t, human("coordinate"))
		w.agent(t, "x1", `{"agentType":"general-purpose","description":"work"}`, c.lines...)
		a := &Adapter{Dir: w.dir, Now: time.Now}
		crew := a.crew(w.subagents, false, time.Now().Add(time.Hour))
		if len(crew) != 1 || crew[0].Status != core.Done || crew[0].Cut != c.cut {
			t.Errorf("%s: crew %+v, want done with cut %v", c.name, crew, c.cut)
		}
	}
}

func TestContextIsWhatTheLatestMainThreadReplyRead(t *testing.T) {
	reply := func(model string, in, write, read int64, sidechain bool) string {
		side := ""
		if sidechain {
			side = `"isSidechain":true,`
		}
		return `{"type":"assistant",` + side + `"timestamp":"2026-10-27T10:00:00Z","message":{"model":"` + model + `","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":` +
			strconv.FormatInt(in, 10) + `,"cache_creation_input_tokens":` + strconv.FormatInt(write, 10) + `,"cache_read_input_tokens":` + strconv.FormatInt(read, 10) + `}}}`
	}
	for _, tc := range []struct {
		name  string
		lines []string
		want  int64
	}{
		{"one reply", []string{human("go"), reply("claude-opus-5-5", 3, 2_000, 250_000, false)}, 252_003},
		{"the latest reply wins", []string{human("go"), reply("claude-opus-5-5", 3, 0, 250_000, false), reply("claude-opus-5-5", 1, 500, 20_000, false)}, 20_501},
		{"a subagent's reply is not the session's", []string{human("go"), reply("claude-opus-5-5", 3, 0, 90_000, false), reply("claude-haiku-5-5", 1, 0, 9_000, true)}, 90_003},
		{"the harness's own reply reads nothing", []string{human("go"), reply("claude-opus-5-5", 3, 0, 90_000, false), reply("<synthetic>", 0, 0, 0, false)}, 90_003},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := newWorld(t, tc.lines...).session(t, nil).Context; got != tc.want {
				t.Fatalf("context = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRunningIsTheBackgroundWorkThatHasNotReportedBack(t *testing.T) {
	bash := toolUse("b1", "Bash", `{"command":"go test ./...","run_in_background":true}`)
	bashStarted := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"b1","content":"Command running in background with ID: ba3nxn8vc."}]},"toolUseResult":{"stdout":"","backgroundTaskId":"ba3nxn8vc"}}`
	agent := toolUse("a1", "Agent", `{"description":"Check TUI","prompt":"look"}`)
	agentStarted := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a1","content":"launched"}]},"toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"a5c01","description":"Check TUI"}}`
	note := func(id, status string) string {
		return `<task-notification>\n<task-id>` + id + `</task-id>\n<status>` + status + `</status>\n</task-notification>`
	}
	for _, tc := range []struct {
		name  string
		lines []string
		want  map[string]string
	}{
		{"foreground only", []string{toolUse("f1", "Bash", `{"command":"ls"}`), `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"f1","content":"x"}]},"toolUseResult":{"stdout":"x"}}`}, map[string]string{}},
		{"string tool result", []string{toolUse("f1", "Bash", `{"command":"ls"}`), `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"f1","is_error":true,"content":"x"}]},"toolUseResult":"Error: x"}`}, map[string]string{}},
		{"started", []string{bash, bashStarted, agent, agentStarted}, map[string]string{"ba3nxn8vc": "Bash: go test ./...", "a5c01": "Agent: Check TUI"}},
		{"completed", []string{bash, bashStarted, agent, agentStarted, `{"type":"user","message":{"content":"` + note("ba3nxn8vc", "completed") + `"}}`}, map[string]string{"a5c01": "Agent: Check TUI"}},
		{"failed while queued", []string{bash, bashStarted, `{"type":"queue-operation","operation":"enqueue","content":"` + note("ba3nxn8vc", "failed") + `"}`}, map[string]string{}},
		{"stopped", []string{bash, bashStarted, toolUse("s1", "TaskStop", `{"task_id":"ba3nxn8vc"}`)}, map[string]string{}},
		{"killed shell", []string{bash, bashStarted, toolUse("s1", "KillShell", `{"shell_id":"ba3nxn8vc"}`)}, map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := map[string]string{}
			for id, a := range newWorld(t, tc.lines...).session(t, nil).Running {
				got[id] = a.String()
			}
			if len(got) != len(tc.want) {
				t.Fatalf("running %v, want %v", got, tc.want)
			}
			for id, want := range tc.want {
				if got[id] != want {
					t.Fatalf("running %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestRunningDropsWorkStartedBeforeTheProcess(t *testing.T) {
	w := newWorld(t, toolUse("b1", "Bash", `{"command":"go test ./..."}`), `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"b1","content":"bg"}]},"toolUseResult":{"backgroundTaskId":"old"}}`)
	a := &Adapter{Dir: w.dir, Now: time.Now}
	proc7 := p(7, "claude")
	proc7.Elapsed = time.Minute
	if got := a.Discover([]proc.Proc{proc7})[0].Running; len(got) != 0 {
		t.Fatalf("a task from before this process still counts: %v", got)
	}
}
