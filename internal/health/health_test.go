package health

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 14, 9, 0, 0, 0, time.Local)

func at(min int) string {
	return t0.Add(time.Duration(min) * time.Minute).UTC().Format(time.RFC3339Nano)
}

func TestAsks(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"markdown heading with numbered list", "Done.\n\n## Open for you\n1. **Merge #12?** I recommend yes.", true},
		{"bold heading, blank line, list", "**Open for you**\n\n1. Ship it?", true},
		{"inline question after heading", "**Open for you:** should I start a shepherd on #16783?", true},
		{"still open heading", "Still open for you:\n- Who owns the rollout?", true},
		{"decisions for you", "Two decisions for you:\n1. Tap or curl?\n2. Who pilots?", true},
		{"needs your call", "Needs your call:\n1. Drop the pin?", true},
		{"still waiting on you", "**Still waiting on you:**\n1. Merge #18303.", true},
		{"one qualified decision", "**One doc decision for you:** soften section 1.5?", true},
		{"empty count", "**Open for you (0).**", false},
		{"nothing", "## Open for you\nNothing.", false},
		{"none inline", "Open for you: none.", false},
		{"nothing as list item", "Open for you:\n1. Nothing right now.", false},
		{"prose mention", "End every reply with a numbered Open for you block.", false},
		{"heading with prose after", "## Open for you\nI merged it and the deploy is green.", false},
		{"no heading", "1. Merge it?\n2. Ship it?", false},
		{"second heading counts after an empty first", "Open for you: none.\n\nDecisions for you:\n1. Ship?", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Asks(c.text); got != c.want {
				t.Fatalf("Asks = %v, want %v", got, c.want)
			}
		})
	}
}

func claudeText(min int, text string) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":false,"timestamp":%q,"message":{"model":"claude-opus-5-5","content":[{"type":"text","text":%q}]}}`, at(min), text)
}

func claudeTool(min int, input string) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":false,"timestamp":%q,"message":{"model":"claude-opus-5-5","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":%q}}]}}`, at(min), input)
}

func claudeUser(min int, origin, text string) string {
	o := ""
	if origin != "" {
		o = fmt.Sprintf(`,"origin":{"kind":%q}`, origin)
	}
	return fmt.Sprintf(`{"type":"user","isSidechain":false,"timestamp":%q%s,"message":{"role":"user","content":%q}}`, at(min), o, text)
}

func claudeResult(min int, source string) string {
	return fmt.Sprintf(`{"type":"user","isSidechain":false,"timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},"permissionDecision":{"decision":"accept","source":%q}}`, at(min), source)
}

func turnEnd(min int) string {
	return fmt.Sprintf(`{"type":"system","subtype":"turn_duration","durationMs":1000,"timestamp":%q}`, at(min))
}

const ask = "## Open for you\n1. Merge it?"

func TestClaudeWaitsAndReprints(t *testing.T) {
	cases := []struct {
		name     string
		lines    []string
		reprints int
		ask      time.Duration
		perm     time.Duration
	}{
		{"turn ends asking, owner replies 10m later",
			[]string{claudeUser(0, "human", "go"), claudeText(1, ask), turnEnd(1), claudeUser(11, "human", "yes")}, 1, 10 * time.Minute, 0},
		{"turn ends without asking",
			[]string{claudeUser(0, "human", "go"), claudeText(1, "Done."), turnEnd(1), claudeUser(11, "human", "thanks")}, 0, 0, 0},
		{"ask followed by more text in the same turn is not waiting",
			[]string{claudeUser(0, "human", "go"), claudeText(1, ask), claudeText(2, "Actually, merged it myself."), turnEnd(2), claudeUser(11, "human", "ok")}, 1, 0, 0},
		{"filed item without a block",
			[]string{claudeUser(0, "human", "go"), claudeTool(1, "ym item add --kind decision --question 'x'"), claudeText(2, "Filed."), turnEnd(2), claudeUser(32, "human", "answered")}, 0, 30 * time.Minute, 0},
		{"notification turn that still asks resumes the wait after the agent works",
			[]string{claudeUser(0, "human", "go"), claudeText(1, ask), turnEnd(1), claudeUser(5, "task-notification", "<task-notification>done</task-notification>"), claudeText(6, ask), turnEnd(6), claudeUser(21, "human", "yes")}, 2, 19 * time.Minute, 0},
		{"notification turn that stops asking ends the wait at the notification",
			[]string{claudeUser(0, "human", "go"), claudeText(1, ask), turnEnd(1), claudeUser(5, "task-notification", "<task-notification>done</task-notification>"), claudeText(9, "All done, nothing needed."), turnEnd(9), claudeUser(60, "human", "great")}, 1, 4 * time.Minute, 0},
		{"owner replies while the agent works a notification",
			[]string{claudeUser(0, "human", "go"), claudeText(1, ask), turnEnd(1), claudeUser(5, "task-notification", "<task-notification>done</task-notification>"), claudeText(6, ask), claudeUser(8, "human", "yes")}, 2, 6 * time.Minute, 0},
		{"permission prompt answered by the owner",
			[]string{claudeUser(0, "human", "go"), claudeTool(1, "rm -rf build"), claudeResult(4, "user_temporary")}, 0, 0, 3 * time.Minute},
		{"permission allowed by config is no wait",
			[]string{claudeUser(0, "human", "go"), claudeTool(1, "ls"), claudeResult(4, "config")}, 0, 0, 0},
		{"untagged transcript counts typed prompts and skips injected ones",
			[]string{claudeUser(0, "", "go"), claudeText(1, ask), claudeUser(3, "", "<command-name>/clear</command-name>"), claudeUser(13, "", "yes")}, 1, 12 * time.Minute, 0},
		{"once tagged, untagged prompts are injected",
			[]string{claudeUser(0, "human", "go"), claudeText(1, ask), turnEnd(1), claudeUser(3, "", "injected text"), claudeText(4, ask), turnEnd(4), claudeUser(13, "human", "yes")}, 2, 11 * time.Minute, 0},
		{"sidechain text is ignored",
			[]string{claudeUser(0, "human", "go"), strings.Replace(claudeText(1, ask), `"isSidechain":false`, `"isSidechain":true`, 1), claudeText(2, "Done."), turnEnd(2), claudeUser(12, "human", "ok")}, 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s state
			var f found
			for _, l := range c.lines {
				s.claudeLine([]byte(l), &f)
			}
			checkFound(t, f, c.reprints, c.ask, c.perm)
		})
	}
}

func checkFound(t *testing.T, f found, reprints int, ask, perm time.Duration) {
	t.Helper()
	var gotAsk, gotPerm time.Duration
	for _, w := range f.waits {
		if w.Kind == WaitPermission {
			gotPerm += w.End.Sub(w.Start)
		} else {
			gotAsk += w.End.Sub(w.Start)
		}
	}
	if len(f.reprints) != reprints || gotAsk != ask || gotPerm != perm {
		t.Fatalf("reprints %d ask %s perm %s, want %d %s %s", len(f.reprints), gotAsk, gotPerm, reprints, ask, perm)
	}
}

func codexMeta(source string) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":"x","source":%s}}`, at(0), source)
}

func codexMsg(min int, role, text string) string {
	kind := "input_text"
	if role == "assistant" {
		kind = "output_text"
	}
	return fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"message","role":%q,"content":[{"type":%q,"text":%q}]}}`, at(min), role, kind, text)
}

func codexDone(min int) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"x"}}`, at(min))
}

func codexCall(min int, name, args string) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"function_call","name":%q,"arguments":%q}}`, at(min), name, args)
}

func TestCodexWaitsAndReprints(t *testing.T) {
	cases := []struct {
		name     string
		lines    []string
		reprints int
		ask      time.Duration
	}{
		{"cli thread asks, owner replies",
			[]string{codexMeta(`"cli"`), codexMsg(0, "user", "# AGENTS.md instructions"), codexMsg(0, "user", "go"), codexMsg(1, "assistant", ask), codexDone(1), codexMsg(16, "user", "yes")}, 1, 15 * time.Minute},
		{"injected context is not the owner",
			[]string{codexMeta(`"vscode"`), codexMsg(1, "assistant", ask), codexDone(1), codexMsg(3, "user", "<environment_context>\n</environment_context>"), codexMsg(6, "user", "yes")}, 1, 5 * time.Minute},
		{"question tool waits",
			[]string{codexMeta(`"cli"`), codexMsg(0, "user", "go"), codexCall(1, "request_user_input_async", "{}"), codexMsg(1, "assistant", "Asked."), codexDone(1), codexMsg(9, "user", "<send_user_message_question_reply>\n[]")}, 0, 8 * time.Minute},
		{"filed item through exec waits",
			[]string{codexMeta(`"cli"`), codexMsg(0, "user", "go"), codexCall(1, "exec_command", `{"cmd":"ym item add --question x"}`), codexMsg(1, "assistant", "Filed."), codexDone(1), codexMsg(4, "user", "ok")}, 0, 3 * time.Minute},
		{"exec run is skipped",
			[]string{codexMeta(`"exec"`), codexMsg(1, "assistant", ask), codexDone(1), codexMsg(6, "user", "yes")}, 0, 0},
		{"child thread is skipped",
			[]string{codexMeta(`{"subagent":{"thread_spawn":{"parent_thread_id":"p"}}}`), codexMsg(1, "assistant", ask), codexDone(1), codexMsg(6, "user", "yes")}, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s state
			var f found
			for _, l := range c.lines {
				s.codexLine([]byte(l), &f)
			}
			checkFound(t, f, c.reprints, c.ask, 0)
		})
	}
}

func TestByDaySplitsAtLocalMidnight(t *testing.T) {
	cases := []struct {
		name       string
		start, end time.Time
		want       map[string]time.Duration
	}{
		{"within a day", t0, t0.Add(time.Hour), map[string]time.Duration{"2026-10-14": time.Hour}},
		{"across midnight", t0.Add(14 * time.Hour), t0.Add(16 * time.Hour), map[string]time.Duration{"2026-10-14": time.Hour, "2026-10-15": time.Hour}},
		{"across two midnights", t0.Add(14 * time.Hour), t0.Add(40 * time.Hour), map[string]time.Duration{"2026-10-14": time.Hour, "2026-10-15": 24 * time.Hour, "2026-10-16": time.Hour}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := map[string]time.Duration{}
			for _, p := range byDay(c.start, c.end) {
				got[p.day] += p.span
			}
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestRollupItems(t *testing.T) {
	now := t0.Add(48 * time.Hour)
	mk := func(createdMin int, state string, answeredMin int) item {
		it := item{Created: t0.Add(time.Duration(createdMin) * time.Minute), State: state}
		if answeredMin >= 0 {
			it.Answered = t0.Add(time.Duration(answeredMin) * time.Minute)
		}
		return it
	}
	cases := []struct {
		name           string
		items          []item
		answered       int
		median, p90    time.Duration
		stale, settled int
	}{
		{"latencies", []item{mk(0, "answered", 10), mk(0, "answered", 20), mk(0, "answered", 30), mk(0, "answered", 600)}, 4, 20 * time.Minute, 10 * time.Hour, 0, 4},
		{"dismissed counts stale", []item{mk(0, "dismissed", -1), mk(0, "answered", 5)}, 1, 5 * time.Minute, 5 * time.Minute, 1, 2},
		{"open past a day is stale", []item{mk(0, "open", -1), mk(0, "snoozed", -1)}, 0, 0, 0, 2, 2},
		{"open under a day is not yet counted", []item{mk(47*60, "open", -1)}, 0, 0, 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := rollup(now, 8, nil, c.items)
			if len(r.Weeks) != 1 {
				t.Fatalf("weeks %d, want 1: only the week with data", len(r.Weeks))
			}
			w := r.Weeks[0]
			if w.Answered != c.answered || w.Median != c.median || w.P90 != c.p90 || w.Stale != c.stale || w.Settled != c.settled {
				t.Fatalf("got answered %d median %s p90 %s stale %d/%d", w.Answered, w.Median, w.P90, w.Stale, w.Settled)
			}
		})
	}
}

func TestScanIsIncrementalAndDedupesCopies(t *testing.T) {
	root := t.TempDir()
	h, err := Open(filepath.Join(root, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err := h.db.Exec(`CREATE TABLE IF NOT EXISTS ym_items (seq INTEGER PRIMARY KEY, created_at TEXT, state TEXT);
		CREATE TABLE IF NOT EXISTS ym_item_events (item TEXT, at TEXT, to_state TEXT)`); err != nil {
		t.Fatal(err)
	}
	h.ClaudeDir, h.CodexHome = filepath.Join(root, "claude"), filepath.Join(root, "codex")
	path := filepath.Join(h.ClaudeDir, "projects", "p", "11111111-aaaa-4aaa-8aaa-111111111111.jsonl")
	appendLines(t, path, claudeUser(0, "human", "go"), claudeText(1, ask), turnEnd(1))
	scan(t, h)
	appendLines(t, path, claudeUser(11, "human", "yes"), claudeText(12, ask), turnEnd(12))
	scan(t, h)
	copyOf := filepath.Join(h.ClaudeDir, "projects", "p", "22222222-bbbb-4bbb-8bbb-222222222222.jsonl")
	raw, _ := os.ReadFile(path)
	appendLines(t, copyOf, string(raw))
	pickup := filepath.Join(h.ClaudeDir, "projects", "p", "33333333-cccc-4ccc-8ccc-333333333333.jsonl")
	appendLines(t, pickup, claudeUser(0, "human", "go"), claudeText(1, ask), claudeTool(2, "ls"), turnEnd(2), claudeUser(11, "human", "yes"))
	scan(t, h)
	r, err := h.Report(t0.Add(time.Hour), 8)
	if err != nil {
		t.Fatal(err)
	}
	var reprints int
	var blocked time.Duration
	for _, d := range r.Days {
		reprints, blocked = reprints+d.Reprints, blocked+d.Blocked
	}
	if reprints != 2 || blocked != 10*time.Minute || r.Weeks[len(r.Weeks)-1].WaitP90 != 10*time.Minute {
		t.Fatalf("reprints %d blocked %s p90 wait %s, want 2, 10m and 10m", reprints, blocked, r.Weeks[len(r.Weeks)-1].WaitP90)
	}
	var files int
	h.db.QueryRow(`SELECT count(*) FROM ym_health_files`).Scan(&files)
	if files != 3 {
		t.Fatalf("files %d, want 3", files)
	}
}

func scan(t *testing.T, h *Health) {
	t.Helper()
	if _, err := h.Scan(); err != nil {
		t.Fatal(err)
	}
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if !strings.HasSuffix(l, "\n") {
			l += "\n"
		}
		f.WriteString(l)
	}
}
