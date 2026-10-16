package claude

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// A background job's process renames itself "claude bg-spare" and runs on a pty its "claude bg-pty-host" parent holds.
func bgProcs() []proc.Proc {
	return []proc.Proc{
		{PID: 7, PPID: 1, TTY: "/dev/ttys003", Args: []string{"claude", "--resume", oldID}},
		{PID: 8, PPID: 7, Args: []string{"claude.exe", "daemon", "run"}},
		{PID: 9, PPID: 8, Args: []string{"claude bg-pty-host", "--bg-pty-host", "/tmp/p.pty.sock", "--", "claude.exe", "--bg-spare", "/tmp/p.claim.sock"}},
		{PID: 10, PPID: 9, TTY: "/dev/ttys007", Args: []string{"claude bg-spare", "--bg-spare", "/tmp/p.claim.sock"}},
		{PID: 11, PPID: 9, Args: []string{"claude bg-spare", "--bg-spare", "/tmp/q.claim.sock"}},
	}
}

func TestABackgroundJobIsTheLiveSessionItsPaneContinuedIn(t *testing.T) {
	for _, status := range []string{"busy", "idle"} {
		t.Run(status, func(t *testing.T) {
			dir := t.TempDir()
			srv := serveInbox(t, "close")
			writeTranscript(t, dir, oldID, userLine, doneLine, continued(newID))
			writeTranscript(t, dir, newID, `{"type":"permission-mode","permissionMode":"bypassPermissions"}`, userLine)
			writeState(t, dir, 7, map[string]any{"pid": 7, "sessionId": oldID, "cwd": "/w", "kind": "interactive", "status": "idle", "statusUpdatedAt": 1})
			writeState(t, dir, 10, map[string]any{"pid": 10, "sessionId": newID, "cwd": "/w", "kind": "bg", "status": status, "statusUpdatedAt": 1,
				"peerProtocol": 1, "messagingSocketPath": srv.sock})
			st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
			must(t, err)
			t.Cleanup(func() { st.Close() })
			a := &Adapter{Dir: dir, Now: time.Now, SendTimeout: 300 * time.Millisecond, ReceiptWait: 100 * time.Millisecond}
			k := &core.Core{Store: st, Harnesses: []core.Harness{a}, Procs: func() ([]proc.Proc, error) { return bgProcs(), nil }}

			b, err := k.Board()
			must(t, err)
			if len(b.Sessions) != 1 || b.Sessions[0].Key() != newID {
				t.Fatalf("sessions = %+v, want only the background job", b.Sessions)
			}
			s, ok := b.Session(oldID)
			if !ok || s.Key() != newID || !s.Background || s.Location() != "bg job" || s.Status.String() != status {
				t.Fatalf("Session(old) = %+v, %v; want the %s background job", s, ok, status)
			}
			if b.ProjectOf(newID) != oldID {
				t.Errorf("project = %q, want the pane's", b.ProjectOf(newID))
			}
			if s.Adoptable() {
				t.Errorf("a background job reads as adoptable")
			}
			ts := k.Targets(b, newID, Name)
			if len(ts) != 1 || ts[0].Handle != nil || ts[0].Resume != nil || !strings.Contains(ts[0].Advice(), "background job") {
				t.Errorf("targets = %+v; want the job with advice and no resume command", ts)
			}

			it, err := k.AddItem(core.Item{Kind: core.KindDecision, Question: "q", Session: oldID})
			must(t, err)
			if _, d, err := k.Answer(b, it.ID, "go"); err != nil || d != core.Sent {
				t.Fatalf("answer = %v, %v; want sent to the job's inbox", d, err)
			}
			lines := <-srv.lines
			if len(lines) != 1 || !strings.Contains(lines[0], `re `+it.ID+`: go`) || !strings.Contains(lines[0], `from-mode=\"bypass\"`) {
				t.Errorf("socket got %q", lines)
			}
		})
	}
}
