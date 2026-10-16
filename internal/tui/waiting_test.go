package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/health"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func waitingBoard(t *testing.T) (Model, *core.Core, *launches) {
	t.Helper()
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sessions := map[int]core.Session{
		1: {ID: "asker", Title: "Parser work", Status: core.Idle, Facts: core.Facts{Result: "Parser done.\n\nOpen for you\n1. Ship it now?\n\nNext: watch CI."}},
		2: {ID: "quiet", Title: "Quiet work", Status: core.Idle, Facts: core.Facts{Result: "All tests pass."}},
	}
	procs := []proc.Proc{{PID: 1, TTY: "/dev/a", Args: []string{"claude"}}, {PID: 2, TTY: "/dev/b", Args: []string{"claude"}}}
	l := &launches{}
	c := &core.Core{Store: st, Harnesses: []core.Harness{harness{sessions}}, Hosts: []core.Host{&host{}}, Launches: l,
		Procs: func() ([]proc.Proc, error) { return procs, nil },
		Ask:   health.Ask}
	return refresh(t, New(c)), c, l
}

func isWaiting(r row) bool { return r.need != nil && r.need.Waiting() }

func TestWaitingRowShowsTheRepliesFirstQuestionAndOnlyForTheAskingSession(t *testing.T) {
	m, _, _ := waitingBoard(t)
	view := m.View()
	if !strings.Contains(view, "NEEDS YOU (1)") || !strings.Contains(view, "Parser work") || !strings.Contains(view, "waiting: 1. Ship it now?") {
		t.Fatalf("board:\n%s", view)
	}
	m = to(t, m, isWaiting)
	m = press(t, m, "w")
	if m.mode != modeScreen || !strings.Contains(m.screen, "Parser done.") || !strings.Contains(m.screen, "Ship it now?") {
		t.Fatalf("w shows %q", m.screen)
	}
	m = press(t, m, "esc", "enter")
	if m.mode != modeScreen || !strings.Contains(m.screen, "Parser done.") {
		t.Fatalf("enter shows %q", m.screen)
	}
}

func TestWaitingRowKeysAnswerOrDismissIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
		sent string
	}{
		{name: "a sends the answer", keys: []string{"a", "y", "e", "s", "enter"}, sent: "asker yes"},
		{name: "x dismisses without sending", keys: []string{"x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, l := waitingBoard(t)
			m = to(t, m, isWaiting)
			m = press(t, m, tc.keys...)
			if strings.Join(l.sent, "|") != tc.sent {
				t.Fatalf("sent %v, want %q", l.sent, tc.sent)
			}
			m = refresh(t, m)
			if strings.Contains(m.View(), "waiting:") {
				t.Fatalf("row still shows:\n%s", m.View())
			}
		})
	}
}

func TestEscCancelsAnAnswerToAWaitingSession(t *testing.T) {
	m, _, l := waitingBoard(t)
	m = to(t, m, isWaiting)
	m = press(t, m, "a", "n", "o", "esc")
	m = refresh(t, m)
	if len(l.sent) != 0 || !strings.Contains(m.View(), "waiting:") {
		t.Fatalf("sent %v; board:\n%s", l.sent, m.View())
	}
}
