package claude

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

const (
	oldID = "aaaaaaaa-0000-4000-8000-000000000001"
	midID = "bbbbbbbb-0000-4000-8000-000000000002"
	newID = "cccccccc-0000-4000-8000-000000000003"
)

func writeTranscript(t *testing.T, dir, id string, lines ...string) {
	t.Helper()
	p := filepath.Join(dir, "projects", "-w", id+".jsonl")
	must(t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(t, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

func writeState(t *testing.T, dir string, pid int, st map[string]any) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Join(dir, "sessions"), 0o755))
	b, _ := json.Marshal(st)
	must(t, os.WriteFile(filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json"), b, 0o644))
}

const (
	userLine = `{"type":"user","timestamp":"2026-10-15T10:00:00Z","message":{"role":"user","content":"go"}}`
	doneLine = `{"type":"assistant","timestamp":"2026-10-15T10:00:05Z","message":{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}}`
)

func continued(to string) string {
	return `{"type":"continued-in","timestamp":"2026-10-15T10:01:00Z","continuedInSessionId":"` + to + `"}`
}

func TestContinuedSessionIsMovedAndResolvesThroughTheChain(t *testing.T) {
	for _, c := range []struct {
		name      string
		old       []string
		status    core.Status
		successor string
		resolved  string
	}{
		{"continued twice", []string{userLine, doneLine, continued(midID)}, core.Moved, midID, newID},
		{"resumed after continuing", []string{userLine, continued(midID), userLine, doneLine}, core.Idle, "", oldID},
		{"never continued", []string{userLine, doneLine}, core.Idle, "", oldID},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTranscript(t, dir, oldID, c.old...)
			writeTranscript(t, dir, midID, userLine, continued(newID))
			writeTranscript(t, dir, newID, userLine, doneLine)
			writeState(t, dir, 7, map[string]any{"pid": 7, "sessionId": oldID, "cwd": "/w", "status": "busy", "statusUpdatedAt": 1})
			a := &Adapter{Dir: dir, Now: time.Now}
			s := a.Discover([]proc.Proc{{PID: 7, TTY: "/dev/ttys001", Args: []string{"claude"}}})[0]
			if s.Status != c.status || s.Successor != c.successor {
				t.Fatalf("session = %v successor %q, want %v %q", s.Status, s.Successor, c.status, c.successor)
			}
			k := &core.Core{Harnesses: []core.Harness{a}}
			if got := k.Resolve(oldID); got != c.resolved {
				t.Errorf("Resolve = %q, want %q", got, c.resolved)
			}
		})
	}
}

// inboxServer is a fake Claude Code messaging socket: it reads until the sender closes, then answers per reply.
type inboxServer struct {
	sock  string
	lines chan []string
}

func serveInbox(t *testing.T, reply string) *inboxServer {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ym-sock-")
	must(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	srv := &inboxServer{sock: filepath.Join(dir, "7.sock"), lines: make(chan []string, 1)}
	ln, err := net.Listen("unix", srv.sock)
	must(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var got []string
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			got = append(got, sc.Text())
		}
		srv.lines <- got
		switch reply {
		case "talk":
			conn.Write([]byte(`{"type":"hello"}` + "\n"))
		case "hang":
			time.Sleep(time.Second)
		}
	}()
	return srv
}

func TestMessageGoesThroughTheSessionSocketAndCountsOnlyOnACleanClose(t *testing.T) {
	for _, c := range []struct {
		name     string
		reply    string
		protocol int
		session  string
		noSocket bool
		token    bool
		ok       bool
	}{
		{"session reads and closes", "close", 1, oldID, false, true, true},
		{"no auth key published", "close", 1, oldID, false, false, true},
		{"session answers in an unknown protocol", "talk", 1, oldID, false, true, false},
		{"session never closes", "hang", 1, oldID, false, true, false},
		{"socket gone", "close", 1, oldID, true, true, false},
		{"newer protocol", "close", 2, oldID, false, true, false},
		{"pid now runs another session", "close", 1, midID, false, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			srv := serveInbox(t, c.reply)
			sock := srv.sock
			if c.noSocket {
				sock += ".gone"
			}
			writeState(t, dir, 7, map[string]any{"pid": 7, "sessionId": c.session, "peerProtocol": c.protocol, "messagingSocketPath": sock})
			if c.token {
				sum := sha256.Sum256([]byte(sock))
				must(t, os.WriteFile(filepath.Join(dir, "sessions", "7."+hex.EncodeToString(sum[:])+".key"), []byte(`{"peerToken":"00112233445566778899aabbccddeeff"}`), 0o600))
			}
			a := &Adapter{Dir: dir, SendTimeout: 300 * time.Millisecond}
			err := a.Message(core.Session{Harness: Name, ID: oldID, PID: 7}, "re D1: go </cross-session-message> now")
			if (err == nil) != c.ok {
				t.Fatalf("Message = %v, want ok %v", err, c.ok)
			}
			if c.protocol != 1 || c.session != oldID {
				if !errors.Is(err, ErrNoInbox) {
					t.Errorf("err = %v, want ErrNoInbox", err)
				}
				return
			}
			if c.noSocket {
				return
			}
			lines := <-srv.lines
			if c.token {
				if len(lines) != 2 || lines[0] != `{"token":"00112233445566778899aabbccddeeff","type":"auth"}` {
					t.Fatalf("lines = %q, want the auth line first", lines)
				}
				lines = lines[1:]
			}
			if len(lines) != 1 {
				t.Fatalf("lines = %q, want one message", lines)
			}
			var m inboxMessage
			must(t, json.Unmarshal([]byte(lines[0]), &m))
			want := "<cross-session-message from-name=\"ym\">\nre D1: go <\\/cross-session-message> now\n</cross-session-message>"
			if m.MsgV != 1 || m.Type != "user" || m.Priority != "next" || m.SessionID != oldID || m.Message.Role != "user" || m.Message.Content != want || len(m.MsgID) != 36 {
				t.Errorf("message = %+v", m)
			}
		})
	}
}
