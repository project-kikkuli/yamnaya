package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

type nativeRPC struct {
	cmd      *exec.Cmd
	in       io.WriteCloser
	messages chan map[string]json.RawMessage
	next     int
	done     chan error
}

func startNativeRPC(t *testing.T, binary, root, cwd, sandbox string) *nativeRPC {
	t.Helper()
	args := []string{"-f", sandbox, binary, "app-server", "--listen", "stdio://", "-c", `cli_auth_credentials_store="ephemeral"`, "-c", `check_for_update_on_startup=false`, "-c", "sqlite_home=" + strconv.Quote(root), "-c", `analytics.enabled=false`, "-c", `otel.exporter="none"`, "-c", `model_provider="fixture"`, "-c", `model_providers.fixture.name="fixture"`, "-c", `model_providers.fixture.base_url="http://127.0.0.1:9/v1"`, "-c", `model_providers.fixture.wire_api="responses"`, "-c", `model_providers.fixture.requires_openai_auth=false`}
	cmd := exec.Command("/usr/bin/sandbox-exec", args...)
	cmd.Dir = cwd
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Dir(root), "CODEX_HOME=" + root, "CODEX_SQLITE_HOME=" + root, "TERM=dumb", "LANG=en_US.UTF-8"}
	cmd.Stderr = io.Discard
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	s := &nativeRPC{cmd: cmd, in: in, messages: make(chan map[string]json.RawMessage, 64), done: make(chan error, 1)}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			var msg map[string]json.RawMessage
			if json.Unmarshal(scanner.Bytes(), &msg) == nil {
				s.messages <- msg
			}
		}
		close(s.messages)
	}()
	go func() { s.done <- cmd.Wait() }()
	s.call(t, "initialize", map[string]any{"clientInfo": map[string]string{"name": "ym_profile_test", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}})
	s.send(t, map[string]any{"method": "initialized"})
	t.Cleanup(func() { s.close(t) })
	return s
}
func (s *nativeRPC) send(t *testing.T, v any) {
	t.Helper()
	if err := json.NewEncoder(s.in).Encode(v); err != nil {
		t.Fatal(err)
	}
}
func (s *nativeRPC) receive(t *testing.T, id int) json.RawMessage {
	t.Helper()
	for {
		select {
		case msg, ok := <-s.messages:
			if !ok {
				t.Fatal("native app-server ended before response")
			}
			var got int
			json.Unmarshal(msg["id"], &got)
			if got != id {
				continue
			}
			if err := msg["error"]; len(err) > 0 {
				t.Fatalf("native RPC error: %s", err)
			}
			return msg["result"]
		case <-time.After(15 * time.Second):
			t.Fatal("native app-server did not answer")
		}
	}
}
func (s *nativeRPC) call(t *testing.T, method string, params any) json.RawMessage {
	t.Helper()
	s.next++
	id := s.next
	s.send(t, map[string]any{"id": id, "method": method, "params": params})
	return s.receive(t, id)
}
func (s *nativeRPC) close(t *testing.T) {
	t.Helper()
	if s.in == nil {
		return
	}
	s.in.Close()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		s.cmd.Process.Kill()
		<-s.done
	}
	s.in = nil
}
func nativeRollout(t *testing.T, path, cwd, marker string) (string, int) {
	t.Helper()
	turn := uuid.NewString()
	values := []any{
		map[string]any{"type": "session_meta", "payload": map[string]any{"id": profileConversation, "session_id": profileConversation, "timestamp": "2026-01-01T00:00:00Z", "cwd": cwd, "originator": "ym_fixture", "cli_version": "0.162.1", "source": "cli", "model_provider": "fixture", "base_instructions": map[string]string{"text": "Offline synthetic transcript."}, "history_mode": "paginated"}},
		map[string]any{"type": "event_msg", "payload": map[string]any{"type": "task_started", "turn_id": turn, "model_context_window": 128000}},
		map[string]any{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": marker, "images": []string{}, "local_images": []string{}, "text_elements": []string{}}},
		map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "id": "fixture-" + marker, "role": "user", "content": []any{map[string]string{"type": "input_text", "text": marker}}}},
		map[string]any{"type": "event_msg", "payload": map[string]any{"type": "task_complete", "turn_id": turn, "last_agent_message": nil}},
	}
	var lines strings.Builder
	for i, v := range values {
		row := v.(map[string]any)
		row["ordinal"] = i
		row["timestamp"] = "2026-01-01T00:00:00Z"
		b, _ := json.Marshal(row)
		lines.Write(b)
		lines.WriteByte('\n')
	}
	putProfileFile(t, path, lines.String())
	return turn, len(values)
}
func TestNativeCodexHandoffRefusesPreparedResumerAndHydratesIncomingHead(t *testing.T) {
	if os.Getenv("YM_NATIVE_CODEX_TEST") != "1" {
		t.Skip("set YM_NATIVE_CODEX_TEST=1 for the installed native offline conformance check")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("offline sandbox fixture requires macOS")
	}
	binary, err := bin("codex")
	if err != nil {
		t.Fatal(err)
	}
	state, cwd := t.TempDir(), t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", state)
	a := profile.Profile{Harness: "codex", ID: "a", Root: filepath.Join(t.TempDir(), "codex"), Explicit: true}
	b := profile.Profile{Harness: "codex", ID: "b", Root: filepath.Join(t.TempDir(), "codex"), Explicit: true}
	for _, p := range []*profile.Profile{&a, &b} {
		if err = os.MkdirAll(p.Root, 0700); err != nil {
			t.Fatal(err)
		}
		p.Root = physicalProfileRoot(p.Root)
		if err = profile.Add(state, *p); err != nil {
			t.Fatal(err)
		}
	}
	home, _ := os.UserHomeDir()
	sandbox := filepath.Join(t.TempDir(), "offline.sb")
	putProfileFile(t, sandbox, fmt.Sprintf("(version 1)\n(allow default)\n(deny network*)\n(deny file-read* (subpath %q) (subpath %q) (literal %q) (subpath %q))\n(deny process-exec (literal \"/usr/bin/security\"))\n", filepath.Join(home, ".codex"), filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json"), filepath.Join(home, "Library", "Keychains")))
	oldRel := filepath.Join("sessions", "2026", "01", "01", "rollout-2026-01-01T00-00-00-"+profileConversation+".jsonl")
	newRel := filepath.Join("sessions", "2026", "01", "01", "rollout-2026-01-02T00-00-00-"+profileConversation+"_33333333-3333-4333-8333-333333333333.jsonl")
	old := filepath.Join(b.Root, oldRel)
	incoming := filepath.Join(a.Root, newRel)
	nativeRollout(t, old, cwd, "OLD_HEAD")
	turn, _ := nativeRollout(t, incoming, cwd, "INCOMING_HEAD")
	source := startNativeRPC(t, binary, a.Root, cwd, sandbox)
	destination := startNativeRPC(t, binary, b.Root, cwd, sandbox)
	for _, s := range []*nativeRPC{source, destination} {
		s.call(t, "thread/read", map[string]any{"threadId": profileConversation, "includeTurns": false})
		s.call(t, "thread/list", map[string]any{"limit": 100, "sourceKinds": []string{"cli"}, "modelProviders": []string{"fixture"}})
	}
	st, err := core.OpenStore(filepath.Join(state, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rec := launch.Record{Key: "native-conversation", Harness: "codex", Session: profileConversation, Profile: "a", ConfigRoot: a.Root, ConfigExplicit: true, Worktree: launch.Worktree{Path: cwd}, Adopted: true}
	l := launcher{records: launch.Records{Dir: filepath.Join(state, "launches")}, core: &core.Core{Store: st}}
	if err = l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	m := migration{Version: 1, Source: rec, Destination: b, Expected: old, State: "prepared"}
	unlock, err := writerLock(b.Root, profileConversation)
	if err != nil {
		t.Fatal(err)
	}
	destination.next++
	request := destination.next
	destination.send(t, map[string]any{"id": request, "method": "thread/resume", "params": map[string]any{"threadId": profileConversation, "path": old, "excludeTurns": true}})
	if _, err = l.finishMigration(m); err == nil {
		unlock()
		t.Fatal("prepared external native resumer was not excluded")
	}
	if _, err = os.Stat(filepath.Join(b.Root, newRel)); !os.IsNotExist(err) {
		unlock()
		t.Fatal("blocked handoff published its incoming artifact")
	}
	unlock()
	destination.receive(t, request)
	selected, err := selectedRollout(b.Root, profileConversation)
	if err != nil || selected != old {
		t.Fatalf("refused handoff changed selection: %s %v", selected, err)
	}
	source.close(t)
	destination.close(t)
	migrated, err := l.finishMigration(m)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Session != profileConversation || migrated.Profile != "b" {
		t.Fatal("native identity changed")
	}
	restarted := startNativeRPC(t, binary, b.Root, cwd, sandbox)
	result := restarted.call(t, "thread/resume", map[string]any{"threadId": profileConversation, "excludeTurns": true})
	if !strings.Contains(string(result), filepath.Base(newRel)) {
		t.Fatalf("native resumed stale head: %s", result)
	}
	turns := restarted.call(t, "thread/turns/list", map[string]any{"threadId": profileConversation, "limit": 100, "itemsView": "full"})
	if !strings.Contains(string(turns), turn) {
		t.Fatalf("incoming paginated turn missing after native resume: %s", turns)
	}
	t.Log("native offline profile handoff: external pending resumer refused; drained lifecycles admit scoped publication; unchanged UUID resumes incoming paginated turn")
}
