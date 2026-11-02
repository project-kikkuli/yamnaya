package codex

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

const quotaEvent = `{"type":"event_msg","timestamp":"2026-11-02T16:20:58.760Z","payload":{"type":"error","codex_error_info":"UsageLimitExceeded"}}`

func TestCodexQuotaHandoffRequiresCurrentMainAndChildTerminalProof(t *testing.T) {
	for _, tc := range []struct {
		name, after, child string
		want               bool
	}{
		{name: "terminal quota", want: true},
		{name: "completed child", child: `{"type":"event_msg","payload":{"type":"task_complete"}}`, want: true},
		{name: "quota child", child: quotaEvent, want: true},
		{name: "active child", child: `{"type":"event_msg","payload":{"type":"task_started"}}`},
		{name: "unmatched child tool", child: `{"type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"a","arguments":"{}"}}` + "\n" + quotaEvent},
		{name: "child nonquota error after complete", child: `{"type":"event_msg","payload":{"type":"task_complete"}}` + "\n" + strings.ReplaceAll(quotaEvent, "UsageLimitExceeded", "ServerOverloaded")},
		{name: "later user", after: `{"type":"event_msg","payload":{"type":"user_message","message":"continue"}}`},
		{name: "later command completion", after: `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","exit_code":0}}}`},
		{name: "later native UserMessage", after: `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage"}}}`},
		{name: "later native AgentMessage", after: `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage"}}}`},
		{name: "later native Reasoning", after: `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"Reasoning"}}}`},
		{name: "new task", after: `{"type":"event_msg","payload":{"type":"task_started"}}`},
		{name: "main nonquota error", after: strings.ReplaceAll(quotaEvent, "UsageLimitExceeded", "ServerOverloaded")},
		{name: "wrong main identity"},
		{name: "wrong child identity", child: quotaEvent},
		{name: "outside main root"},
		{name: "missing child", child: "missing"},
		{name: "partial main", after: `{"type":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			db, err := sql.Open("sqlite", filepath.Join(root, "state_5.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			_, err = db.Exec(`CREATE TABLE threads(id TEXT PRIMARY KEY,rollout_path TEXT,cwd TEXT);CREATE TABLE thread_spawn_edges(parent_thread_id TEXT,child_thread_id TEXT,status TEXT)`)
			if err != nil {
				t.Fatal(err)
			}
			write := func(id, data string) string {
				path := filepath.Join(root, "sessions", id+".jsonl")
				if tc.name == "outside main root" && id == "main" {
					path = filepath.Join(t.TempDir(), id+".jsonl")
				}
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				metaID := id
				if tc.name == "wrong "+id+" identity" {
					metaID = "other"
				}
				body := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q}}`, metaID) + "\n" + data
				if tc.name != "partial main" {
					body = strings.TrimRight(body, "\n") + "\n"
				}
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				return path
			}
			path := write("main", quotaEvent+"\n"+tc.after)
			if _, err = db.Exec(`INSERT INTO threads VALUES('main',?,'/work')`, path); err != nil {
				t.Fatal(err)
			}
			if tc.child != "" {
				child := filepath.Join(root, "sessions", "missing.jsonl")
				if tc.child != "missing" {
					child = write("child", tc.child)
				}
				_, err = db.Exec(`INSERT INTO threads VALUES('child',?,'/work');INSERT INTO thread_spawn_edges VALUES('main','child','open')`, child)
				if err != nil {
					t.Fatal(err)
				}
			}
			a := &Adapter{Home: root}
			defer func() {
				if a.state != nil {
					a.state.Close()
				}
			}()
			_, err = a.InspectHandoff(core.Session{ID: "main", Cwd: "/work"})
			if (err == nil) != tc.want {
				t.Fatalf("handoff=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestCodexHandoffRejectsNativeWorkingComposer(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"quota reached\n› \n\n  context left", true},
		{"working · esc to interrupt\n› \n\n  context left", false},
		{"quota reached\n› \n\n tab to queue message", false},
		{"unknown dialog", false},
	} {
		if got := (&Adapter{}).QuotaDialog(tc.text); got != tc.want {
			t.Fatalf("screen %q got %v want %v", tc.text, got, tc.want)
		}
	}
}
