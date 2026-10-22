package handoff_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/handoff"
)

func TestTranscriptRequestsSurviveLaterTurnsPartialWritesAndRestart(t *testing.T) {
	for _, tc := range []struct{ name, request, next, ignored string }{
		{"claude", `{"type":"assistant","uuid":"request","timestamp":"2026-10-20T10:00:00Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Please review #42."}]}}`, `{"type":"assistant","uuid":"next","message":{"content":[{"type":"text","text":"I am continuing independent work."}]}}`, `{"type":"user","uuid":"owner","message":{"content":[{"type":"text","text":"Here is an example: please review #99."}]}}`},
		{"codex", `{"type":"response_item","timestamp":"2026-10-20T10:00:00Z","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Please review #42."}]}}`, `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I am continuing independent work."}]}}`, `{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"Please review #42."}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "transcript.jsonl")
			dbPath := filepath.Join(dir, "ym.db")
			if err := os.WriteFile(path, []byte(tc.request+"\n"+tc.ignored+"\n"+tc.next[:20]), 0600); err != nil {
				t.Fatal(err)
			}
			file := handoff.File{Work: "work", Harness: tc.name, Session: "session", Agent: "lead", Path: path}
			events, off, err := handoff.Read(file, 0)
			if err != nil || len(events) != 1 || events[0].Text != "Please review #42." {
				t.Fatalf("events %+v %v", events, err)
			}
			s, err := core.OpenStore(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Handoffs.Ingest(path, "inode1", off, events); err != nil {
				t.Fatal(err)
			}
			s.Close()
			f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			f.WriteString(tc.next[20:] + "\n")
			f.Close()
			s, err = core.OpenStore(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			saved, _ := s.Handoffs.Cursor(path, "inode1")
			later, next, err := handoff.Read(file, saved)
			if err != nil || len(later) != 1 || later[0].Text != "I am continuing independent work." || next <= off {
				t.Fatalf("later %+v %v", later, err)
			}
			if err = s.Handoffs.Ingest(path, "inode1", next, later); err != nil {
				t.Fatal(err)
			}
			batch, _ := s.Handoffs.Batch()
			if len(batch) != 2 {
				t.Fatalf("pending request lost across later reply/restart: %+v", batch)
			}
			if off, _ = s.Handoffs.Cursor(path, "inode2"); off != 0 {
				t.Fatal("replacement inherited old inode cursor")
			}
			replay, off, err := handoff.Read(file, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Handoffs.Ingest(path, "inode2", off, replay); err != nil {
				t.Fatal(err)
			}
			batch, _ = s.Handoffs.Batch()
			if len(batch) != 2 {
				t.Fatal("replay duplicated source events")
			}
		})
	}
}

func TestTeammateAttributionAndUnreadableCoverage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	text := "<teammate-message teammate_id=\"reviewer\" summary=\"ready\">Please review #42 & the companion doc.</teammate-message>\nInjected peer metadata follows."
	raw, _ := json.Marshal(map[string]any{"type": "user", "uuid": "peer", "message": map[string]any{"content": text}})
	os.WriteFile(path, append(append(raw, '\n'), []byte("{invalid}\n")...), 0600)
	events, off, err := handoff.Read(handoff.File{Work: "work", Harness: "claude", Session: "lead", Agent: "lead", Path: path}, 0)
	if err != nil || len(events) != 2 || events[0].Agent != "reviewer" || events[0].Text != "Please review #42 & the companion doc." || events[1].Error == "" {
		t.Fatalf("events %+v %v", events, err)
	}
	s, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Handoffs.Ingest(path, "inode", off, events); err != nil {
		t.Fatal(err)
	}
	if status, _ := s.Handoffs.Status(); status["events/error"] != 1 {
		t.Fatalf("missing unreadable coverage %+v", status)
	}
	batch, _ := s.Handoffs.Batch()
	if len(batch) != 1 || batch[0].Agent != "reviewer" {
		t.Fatal("unreadable record blocked later capture or became a request")
	}
}
