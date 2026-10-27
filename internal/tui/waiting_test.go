package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/health"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func waitingBoard(t *testing.T) Model {
	t.Helper()
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	said := time.Now().Add(-time.Minute)
	sessions := map[int]core.Session{
		1: {ID: "asker", Title: "Parser work", Status: core.Idle, Facts: core.Facts{Result: "Parser done.\n\nOpen for you\n1. Ship it now?", ResultAt: said}},
		2: {ID: "quiet", Title: "Quiet work", Status: core.Busy, Facts: core.Facts{Result: "All tests pass.", ResultAt: said}},
	}
	procs := []proc.Proc{{PID: 1, TTY: "/dev/a", Args: []string{"claude"}}, {PID: 2, TTY: "/dev/b", Args: []string{"claude"}}}
	c := &core.Core{Store: st, Harnesses: []core.Harness{harness{sessions}}, Hosts: []core.Host{&host{}}, Launches: &launches{},
		Procs: func() ([]proc.Proc, error) { return procs, nil },
		Ask:   health.Addressed}
	return refresh(t, New(c))
}

func lineOf(view, text string) string {
	for _, l := range strings.Split(ansi.Strip(view), "\n") {
		if strings.Contains(l, text) {
			return l
		}
	}
	return ""
}

func TestAnIdleSessionYouStartedShowsItsLastLineOnItsRowNotInNeedsYou(t *testing.T) {
	m := waitingBoard(t)
	view := m.View().Content
	if !strings.Contains(view, "NEEDS YOU (0)") {
		t.Fatalf("an idle session's last message reached Needs you:\n%s", view)
	}
	if head := lineOf(view, " ym "); !strings.Contains(head, "2 sessions") || !strings.Contains(head, "0 goals") || !strings.Contains(head, "nothing needs you") {
		t.Errorf("header %q, want sessions, goals and the Needs you count", head)
	}
	if row := lineOf(view, "1. Ship it now?"); !strings.Contains(row, "● idle · 1. Ship it now?") {
		t.Fatalf("idle row %q, want an unread dot and its last line", row)
	}
	if row := lineOf(view, "Quiet work"); strings.Contains(row, "idle ·") {
		t.Errorf("busy session row %q shows an idle line", row)
	}
	m = to(t, m, mission("asker"))
	m = refresh(t, press(t, m, "o"))
	if row := lineOf(m.View().Content, "1. Ship it now?"); strings.Contains(row, "●") || !strings.Contains(row, "idle · 1. Ship it now?") {
		t.Fatalf("after opening, row %q, want its last line without the unread dot", row)
	}
}
