package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

const quotaLine = `{"type":"assistant","sessionId":"quota-session","timestamp":"2026-11-02T16:20:58.760Z","isApiErrorMessage":true,"error":"rate_limit","message":{"model":"<synthetic>","stop_reason":"stop_sequence","content":[]}}`
const quotaUser = `{"type":"user","sessionId":"quota-session","timestamp":"2026-11-02T16:20:58.372Z","message":{"content":"continue"}}`
const quotaTool = `{"type":"assistant","sessionId":"quota-session","timestamp":"2026-11-02T16:21:00Z","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"call","name":"Bash","input":{"command":"work"}}]}}`
const quotaDialog = "You've hit your weekly limit · resets Nov 5, 3am\n\nWhat do you want to do?\n❯ 1. Stop and wait for limit to reset\n  2. Wait here, then continue automatically at Nov 5, 3am\n  3. Switch to usage credits\nEnter to confirm · Esc to cancel\n"

func TestQuotaHandoffInspectsMainAndIndependentChildren(t *testing.T) {
	handback := `{"type":"assistant","sessionId":"quota-session","timestamp":"2026-11-02T16:20:59Z","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"return","name":"SubagentHandback","input":{}}]}}` + "\n" + `{"type":"user","sessionId":"quota-session","timestamp":"2026-11-02T16:21:00Z","message":{"content":[{"type":"tool_result","tool_use_id":"return","content":"ok"}]}}`
	for _, tc := range []struct {
		name, before, after, child, childPath, status string
		want                                          bool
	}{
		{name: "stopped main with turn duration", after: `{"type":"system","subtype":"turn_duration"}`, want: true},
		{name: "open main and later quota children", child: strings.ReplaceAll(quotaLine, `"isApiErrorMessage":true`, `"isSidechain":true,"isApiErrorMessage":true`), childPath: "workflows/run/agent-child.jsonl", want: true},
		{name: "teammate ended", child: `{"type":"assistant","sessionId":"quota-session","timestamp":"2026-11-02T16:20:59Z","message":{"stop_reason":"end_turn","content":[]}}`, want: true},
		{name: "handback result", child: handback, want: true},
		{name: "resumed after handback", child: handback + "\n" + quotaUser},
		{name: "later main user", after: quotaUser},
		{name: "later main tool", after: quotaTool},
		{name: "unmatched main tool", before: quotaTool},
		{name: "hidden active child", child: quotaTool},
		{name: "generic child stop sequence", child: strings.ReplaceAll(quotaLine, `"rate_limit"`, `"overloaded"`)},
		{name: "nonquota main", before: "nonquota"},
		{name: "unreadable native state", status: "missing"},
		{name: "native busy", status: "busy"},
		{name: "partial main", after: `{"type":`},
		{name: "malformed main", after: "garbage\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			main := quotaLine
			if tc.before == "nonquota" {
				main = strings.ReplaceAll(main, `"rate_limit"`, `"overloaded"`)
				tc.before = ""
			}
			data := quotaUser + "\n" + tc.before + "\n" + main + "\n" + tc.after
			data = strings.ReplaceAll(data, "\n\n", "\n")
			if tc.name != "partial main" {
				data = strings.TrimRight(data, "\n") + "\n"
			}
			path := filepath.Join(root, "projects", "-work", "quota-session.jsonl")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.child != "" {
				child := tc.childPath
				if child == "" {
					child = "agent-child.jsonl"
				}
				child = filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents", child)
				if err := os.MkdirAll(filepath.Dir(child), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(child, []byte(tc.child+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			status := tc.status
			if status == "" {
				status = "waiting"
			}
			if status != "missing" {
				writeState(t, root, 42, map[string]any{"pid": 42, "sessionId": "quota-session", "cwd": "/work", "status": status, "statusUpdatedAt": 1793636458947})
			}
			a := &Adapter{Dir: root, Now: time.Now, Procs: func() ([]proc.Proc, error) { return nil, nil }}
			at, err := a.InspectHandoff(core.Session{ID: "quota-session", PID: 42, Cwd: "/work"})
			if (err == nil) != tc.want {
				t.Fatalf("at=%s err=%v want=%v", at, err, tc.want)
			}
		})
	}
}

func TestQuotaDialogRequiresLiveBottomDialog(t *testing.T) {
	captured, err := os.ReadFile("testdata/weekly-limit-screen.txt")
	if err != nil {
		t.Fatal(err)
	}
	remote := string(captured)
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"observed", quotaDialog, true},
		{"captured weekly limit", remote, true},
		{"captured without decoration", strings.ReplaceAll(remote, "⎿", ""), true},
		{"decorated nonweekly limit", strings.ReplaceAll(remote, "weekly limit", "limit"), true},
		{"captured different selected option", strings.ReplaceAll(strings.ReplaceAll(remote, "❯ 1.", "1."), "3. Switch", "❯ 3. Switch"), true},
		{"different selected option", strings.ReplaceAll(strings.ReplaceAll(quotaDialog, "❯ 1.", "1."), "2. Wait", "❯ 2. Wait"), true},
		{"captured scrollback", remote + "❯ Continue working\n", false},
		{"captured permission", strings.ReplaceAll(remote, "1. Stop and wait for limit to reset", "1. Yes"), false},
		{"captured heading absent", strings.ReplaceAll(remote, "You've hit your weekly limit", "Different dialog"), false},
		{"quoted quota heading", strings.ReplaceAll(remote, "⎿", "> ⎿"), false},
		{"quota heading in prose", strings.ReplaceAll(remote, "⎿", "The tool said: ⎿"), false},
		{"stale quota heading", strings.ReplaceAll(remote, "What do you want to do?", "more output\nmore output\nWhat do you want to do?"), false},
		{"missing question", strings.ReplaceAll(remote, "What do you want to do?", ""), false},
		{"missing stop option", strings.ReplaceAll(remote, "❯ 1. Stop and wait for limit to reset", ""), false},
		{"missing wait option", strings.ReplaceAll(remote, "2. Wait here, then continue automatically at Nov 5, 3am", ""), false},
		{"missing credits option", strings.ReplaceAll(remote, "3. Switch to usage credits", ""), false},
		{"missing confirmation", strings.ReplaceAll(remote, "Enter to confirm · Esc to cancel", ""), false},
		{"scrollback", quotaDialog + "Now another dialog\n", false},
		{"permission", strings.ReplaceAll(quotaDialog, "1. Stop and wait for limit to reset", "1. Yes"), false},
		{"heading absent", strings.ReplaceAll(quotaDialog, "You've hit your weekly limit", "Different dialog"), false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := (&Adapter{}).QuotaDialog(tc.text); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestQuotaHandoffRejectsUnknownIdentityPathsAndNativeDescendants(t *testing.T) {
	const childID = "aaaaaaaa-1111-4111-8111-111111111111"
	const grandID = "bbbbbbbb-2222-4222-8222-222222222222"
	for _, name := range []string{"canonical root alias", "wrong main ID", "mixed main IDs", "mixed child IDs", "missing child transcript", "child file escape", "child symlink directory", "main file escape", "native child busy", "native child waiting", "native child unreadable", "reparented unreadable child", "related idle child unknown transcript", "related idle completed child", "related idle child active transcript", "native child root mismatch", "nested independently resumed active child"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			put := func(path, data string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(strings.TrimRight(data, "\n")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			main := filepath.Join(root, "projects", "-work", "quota-session.jsonl")
			put(main, quotaUser+"\n"+quotaLine)
			writeState(t, root, 42, map[string]any{"pid": 42, "sessionId": "quota-session", "cwd": "/work", "status": "waiting"})
			child := filepath.Join(strings.TrimSuffix(main, ".jsonl"), "subagents", "agent-"+childID+".jsonl")
			complete := strings.ReplaceAll(strings.ReplaceAll(quotaLine, "quota-session", childID), `"rate_limit"`, `"unused"`)
			complete = strings.ReplaceAll(complete, `"stop_sequence"`, `"end_turn"`)
			ps := []proc.Proc{{PID: 42, Args: []string{"claude"}}}
			adapterRoot := root
			want := name == "canonical root alias" || name == "related idle completed child"
			switch name {
			case "canonical root alias":
				adapterRoot = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(root, adapterRoot); err != nil {
					t.Fatal(err)
				}
			case "wrong main ID":
				put(main, strings.ReplaceAll(quotaLine, "quota-session", "other"))
			case "mixed main IDs":
				put(main, quotaLine+"\n"+strings.ReplaceAll(quotaLine, "quota-session", "other"))
			case "mixed child IDs":
				put(child, complete+"\n"+strings.ReplaceAll(complete, childID, "other"))
			case "missing child transcript":
				put(strings.TrimSuffix(child, ".jsonl")+".meta.json", `{}`)
			case "main file escape", "child file escape":
				outside := filepath.Join(t.TempDir(), "outside.jsonl")
				put(outside, quotaLine)
				link := main
				if name == "child file escape" {
					link = child
				}
				if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
					t.Fatal(err)
				}
				os.Remove(link)
				if err := os.Symlink(outside, link); err != nil {
					t.Fatal(err)
				}
			case "child symlink directory":
				dir := filepath.Dir(child)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(dir, "workflow")); err != nil {
					t.Fatal(err)
				}
			default:
				put(child, complete)
				own := filepath.Join(root, "projects", "-work", childID+".jsonl")
				if name != "related idle child unknown transcript" {
					put(own, complete)
				}
				if name == "related idle child active transcript" {
					put(own, strings.ReplaceAll(quotaTool, "quota-session", childID))
				}
				ppid := 42
				if name == "reparented unreadable child" {
					ppid = 1
				}
				ps = append(ps, proc.Proc{PID: 43, PPID: ppid, Args: []string{"claude", "--resume", childID}})
				status := "idle"
				if name == "native child busy" {
					status = "busy"
				}
				if name == "native child waiting" {
					status = "waiting"
				}
				if name != "native child unreadable" && name != "reparented unreadable child" {
					writeState(t, root, 43, map[string]any{"pid": 43, "sessionId": childID, "cwd": "/work", "status": status})
				}
				if name == "nested independently resumed active child" {
					nested := filepath.Join(strings.TrimSuffix(own, ".jsonl"), "subagents", "agent-"+grandID+".jsonl")
					put(nested, strings.ReplaceAll(complete, childID, grandID))
					ps = append(ps, proc.Proc{PID: 44, PPID: 1, Args: []string{"claude", "--resume", grandID}})
					writeState(t, root, 44, map[string]any{"pid": 44, "sessionId": grandID, "cwd": "/work", "status": "busy"})
				}
			}
			a := &Adapter{Dir: adapterRoot, Now: time.Now, Procs: func() ([]proc.Proc, error) { return ps, nil }, RootOf: func(pid int, _ string) string {
				if name == "native child root mismatch" && pid == 43 {
					return t.TempDir()
				}
				return root
			}}
			_, err := a.InspectHandoff(core.Session{ID: "quota-session", PID: 42, Cwd: "/work"})
			if (err == nil) != want {
				t.Fatalf("native proof=%v want=%v", err, want)
			}
		})
	}
}

func TestQuotaHandoffCountsASubagentKilledWithItsProcessAsOver(t *testing.T) {
	const (
		prompt = `{"type":"user","sessionId":"quota-session","agentId":"af89","isSidechain":true,"timestamp":"2026-11-03T22:40:35.719Z","message":{"role":"user","content":"Run this exact Bash command in the foreground"}}`
		tool   = `{"type":"assistant","sessionId":"quota-session","agentId":"af89","isSidechain":true,"timestamp":"2026-11-03T22:40:39.033Z","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"toolu_okAwKD","name":"Bash","input":{"command":"until [ -f x ]; do sleep 2; done"}}]}}`
		killed = `{"type":"user","sessionId":"quota-session","agentId":"af89","isSidechain":true,"timestamp":"2026-11-03T22:41:42.877Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_okAwKD","is_error":true,"content":"Exit code 137"}]},"toolUseResult":"Error: Exit code 137"}`
		attach = `{"type":"attachment","sessionId":"quota-session","agentId":"af89","isSidechain":true,"timestamp":"2026-11-03T22:41:42.930Z"}`
	)
	for _, c := range []struct {
		name  string
		lines []string
		// wrote is how long ago the transcript was last written, running how long ago the process started; zero has no process to read.
		wrote, running time.Duration
		want           bool
	}{
		{name: "the shape the drill left: tool result 137, no stop", lines: []string{prompt, tool, killed, attach}, wrote: time.Hour, running: time.Minute, want: true},
		{name: "killed before its tool result was written", lines: []string{prompt, tool}, wrote: time.Hour, running: time.Minute, want: true},
		{name: "written by this process, tool still running", lines: []string{prompt, tool}, wrote: time.Minute, running: time.Hour},
		{name: "written by this process, tool just returned", lines: []string{prompt, tool, killed, attach}, wrote: time.Minute, running: time.Hour},
		{name: "no process to tell by", lines: []string{prompt, tool, killed, attach}, wrote: time.Hour},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "projects", "-work", "quota-session.jsonl")
			child := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents", "agent-af89.jsonl")
			if err := os.MkdirAll(filepath.Dir(child), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(quotaUser+"\n"+quotaLine+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(child, []byte(strings.Join(c.lines, "\n")+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			at := time.Now().Add(-c.wrote)
			if err := os.Chtimes(child, at, at); err != nil {
				t.Fatal(err)
			}
			writeState(t, root, 42, map[string]any{"pid": 42, "sessionId": "quota-session", "cwd": "/work", "status": "waiting", "statusUpdatedAt": 1793000000000})
			var ps []proc.Proc
			if c.running > 0 {
				ps = []proc.Proc{{PID: 42, Elapsed: c.running, Args: []string{"claude"}}}
			}
			a := &Adapter{Dir: root, Now: time.Now, Procs: func() ([]proc.Proc, error) { return ps, nil }}
			if _, err := a.InspectHandoff(core.Session{ID: "quota-session", PID: 42, Cwd: "/work"}); (err == nil) != c.want {
				t.Fatalf("err=%v want ok=%v", err, c.want)
			}
		})
	}
}

func TestConversationTellsAGoneTranscriptFromOneItCannotRead(t *testing.T) {
	const id = "quota-session"
	for _, c := range []struct {
		name  string
		setup func(*testing.T, string)
		found bool
		fails bool
	}{
		{name: "never had a transcript", setup: func(*testing.T, string) {}},
		{name: "transcript holds a turn", found: true, setup: func(t *testing.T, root string) {
			putConversation(t, root, quotaUser+"\n")
		}},
		{name: "transcript holds no turn", setup: func(t *testing.T, root string) {
			putConversation(t, root, `{"type":"system"}`+"\n")
		}},
		{name: "transcript unreadable", fails: true, setup: func(t *testing.T, root string) {
			p := putConversation(t, root, quotaUser+"\n")
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(p, 0600) })
		}},
		{name: "an empty stub in an earlier project dir", found: true, setup: func(t *testing.T, root string) {
			stub := filepath.Join(root, "projects", "-a-earlier", "quota-session.jsonl")
			if err := os.MkdirAll(filepath.Dir(stub), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stub, []byte(`{"type":"system"}`+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			putConversation(t, root, quotaUser+"\n")
		}},
		{name: "config root missing", fails: true, setup: func(t *testing.T, root string) {
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "claude")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			c.setup(t, root)
			a := &Adapter{Dir: root}
			path, err := a.Conversation(id)
			if (err != nil) != c.fails || (path != "") != c.found {
				t.Fatalf("path=%q err=%v, want found=%v fails=%v", path, err, c.found, c.fails)
			}
			a.Unstart(id)
			if c.found {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("Unstart moved a conversation that holds a turn: %v", err)
				}
			}
			if c.fails {
				if matches, _ := filepath.Glob(filepath.Join(root, "projects", "*", "*.noturn")); len(matches) != 0 {
					t.Fatalf("Unstart renamed a conversation it could not read: %v", matches)
				}
			}
		})
	}
}

func putConversation(t *testing.T, root, data string) string {
	t.Helper()
	p := filepath.Join(root, "projects", "-work", "quota-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSilentQuotaStopReadsOnlyTheDialogOverAnEndedTurn(t *testing.T) {
	captured, err := os.ReadFile("testdata/silent-quota-dialog.txt")
	if err != nil {
		t.Fatal(err)
	}
	real := string(captured)
	ended := `{"type":"assistant","sessionId":"quota-session","timestamp":"2026-11-04T00:56:00Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"FINISHED"}]}}`
	for _, c := range []struct {
		name, screen, main string
		stopped            bool
		fails              bool
	}{
		{name: "the captured screen over an ended turn", screen: real, main: ended, stopped: true},
		{name: "the captured screen over a turn the limit stopped", screen: real, main: strings.ReplaceAll(quotaLine, "2026-11-02T16:20:58.760Z", "2026-11-04T01:03:52Z"), stopped: true},
		{name: "the captured screen over an open turn", screen: real, main: quotaTool, stopped: true, fails: true},
		{name: "the captured screen after a later prompt", screen: real, main: ended + "\n" + strings.ReplaceAll(quotaUser, "2026-11-02T16:20:58.372Z", "2026-11-04T01:00:00Z"), stopped: true, fails: true},
		{name: "the captured screen wrapped by a narrow pane", screen: rewrap(real, 30), main: ended, stopped: true},
		{name: "another dialog wrapped by a narrow pane", screen: rewrap(strings.ReplaceAll(real, "1. Stop and wait for limit to reset", "1. Yes"), 30), main: ended},
		{name: "wrapped options quoted, composer below", screen: rewrap(real, 30) + "\n❯ continue working\n", main: ended},
		{name: "a stop the transcript records is the strict path's", screen: quotaDialog, main: quotaLine},
		{name: "the options quoted, composer below", screen: real + "\n❯ continue working\n", main: ended},
		{name: "another dialog under a rule", screen: strings.ReplaceAll(real, "1. Stop and wait for limit to reset", "1. Yes"), main: ended},
		{name: "no rule above the options", screen: strings.ReplaceAll(real, "▔", ""), main: ended},
		{name: "a rule of something else", screen: strings.ReplaceAll(real, "▔", "x"), main: ended},
		{name: "an ordinary screen", screen: "❯ what next\n", main: ended},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "projects", "-work", "quota-session.jsonl")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(quotaUser+"\n"+c.main+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			writeState(t, root, 42, map[string]any{"pid": 42, "sessionId": "quota-session", "cwd": "/work", "status": "waiting", "statusUpdatedAt": 1793636458947})
			a := &Adapter{Dir: root, Now: time.Now, Procs: func() ([]proc.Proc, error) { return nil, nil }}
			_, stopped, err := a.SilentQuotaStop(core.Session{ID: "quota-session", PID: 42, Cwd: "/work"}, c.screen)
			if stopped != c.stopped || (err != nil) != c.fails {
				t.Fatalf("stopped=%v err=%v, want stopped=%v fails=%v", stopped, err, c.stopped, c.fails)
			}
		})
	}
}

// rewrap hard-wraps each line of text at width, as a narrow pane shows it.
func rewrap(text string, width int) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		r := []rune(line)
		for len(r) > width {
			out = append(out, string(r[:width]))
			r = r[width:]
		}
		out = append(out, string(r))
	}
	return strings.Join(out, "\n")
}

func TestTheQuotaFooterIsAStopOnlyUnderTheInputBox(t *testing.T) {
	captured, err := os.ReadFile("testdata/quota-footer.txt")
	if err != nil {
		t.Fatal(err)
	}
	real := string(captured)
	now := time.Date(2026, 11, 4, 4, 31, 0, 0, time.UTC)
	rule := strings.Repeat("─", 40)
	footer := "⚠ Usage limit reached · limit resets 3am\nContinuing automatically at 3am · esc to cancel"
	for _, c := range []struct {
		name, screen string
		want         bool
	}{
		{"the captured screen", real, true},
		{"the captured screen wrapped narrow", rewrap(real, 30), true},
		{"a reply reciting it above the box", footer + "\n✻ Done\n" + rule + "\n❯ \n" + rule + "\n⏵⏵ bypass permissions on\n", false},
		{"typed into the box", "✻ Done\n" + rule + "\n❯ " + footer + "\n" + rule + "\n⏵⏵ bypass\n", false},
		{"no input box", footer + "\n", false},
		{"another warning under the box", "✻ Done\n" + rule + "\n❯ \n" + rule + "\n⚠ Auto-update failed\n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, got := footerStop(c.screen, now); got != c.want {
				t.Fatalf("footer=%v, want %v", got, c.want)
			}
		})
	}
}

func TestAQuotaResetReadsAsItsNextOccurrenceInTheRightZone(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no zoneinfo")
	}
	utc := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, time.UTC) }
	t.Setenv("TZ", "UTC")
	saved := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = saved })
	for _, c := range []struct {
		name, text string
		now, want  time.Time
	}{
		{"later today", "3pm", utc(2026, 11, 4, 4, 31), utc(2026, 11, 4, 15, 0)},
		{"already passed today means tomorrow", "3am", utc(2026, 11, 4, 4, 31), utc(2026, 11, 5, 3, 0)},
		{"exactly now means tomorrow", "3am", utc(2026, 11, 4, 3, 0), utc(2026, 11, 5, 3, 0)},
		{"with minutes", "3:30am", utc(2026, 11, 4, 3, 0), utc(2026, 11, 4, 3, 30)},
		{"noon", "12pm", utc(2026, 11, 4, 4, 0), utc(2026, 11, 4, 12, 0)},
		{"midnight", "12am", utc(2026, 11, 4, 4, 0), utc(2026, 11, 5, 0, 0)},
		{"a named zone", "3am(America/New_York)", utc(2026, 11, 4, 4, 31), time.Date(2026, 11, 4, 3, 0, 0, 0, ny)},
		{"a date", "Nov5,3am(UTC)", utc(2026, 11, 4, 4, 31), utc(2026, 11, 5, 3, 0)},
		{"a date just past means next year", "Nov3,3am(UTC)", utc(2026, 11, 4, 4, 31), utc(2027, 11, 3, 3, 0)},
		{"unreadable", "soon", utc(2026, 11, 4, 4, 31), time.Time{}},
		{"an unknown zone", "3am(Mars/Olympus)", utc(2026, 11, 4, 4, 31), time.Time{}},
		{"not a clock", "13am", utc(2026, 11, 4, 4, 31), time.Time{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := parseQuotaReset(c.text, c.now); !got.Equal(c.want) {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}
}

func TestRealScreensThatOnlyMentionAQuotaStopAreNotOne(t *testing.T) {
	for _, name := range []string{"quota-fp-recited.txt", "quota-fp-askuser.txt"} {
		t.Run(name, func(t *testing.T) {
			captured, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			a := &Adapter{}
			if a.QuotaDialog(string(captured)) || silentQuotaDialog(string(captured)) {
				t.Fatal("read as the quota dialog")
			}
			if _, ok := footerStop(string(captured), time.Now()); ok {
				t.Fatal("read as the quota footer")
			}
		})
	}
}
