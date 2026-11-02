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
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"observed", quotaDialog, true},
		{"different selected option", strings.ReplaceAll(strings.ReplaceAll(quotaDialog, "❯ 1.", "1."), "2. Wait", "❯ 2. Wait"), true},
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
