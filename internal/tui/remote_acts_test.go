package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// hostRemotes is a host that answers, went silent before the action, or goes silent during it.
type hostRemotes struct {
	*remotes
	silent, runErr error
	reached        int
}

func (h *hostRemotes) Silent(string) error { return h.silent }
func (h *hostRemotes) Reach(string) error {
	h.reached++
	return h.runErr
}
func (h *hostRemotes) Run(host string, args ...string) (string, error) {
	h.ran = append(h.ran, append([]string{host}, args...))
	if h.runErr != nil {
		return "", h.runErr
	}
	return "killed claude far-lead", nil
}

type hostState struct {
	name           string
	silent, runErr error
}

var hostStates = []hostState{
	{"answering", nil, nil},
	{"not answering", fmt.Errorf("%w: no answer for 2m", core.ErrUnreachable), nil},
	{"goes silent during the action", nil, fmt.Errorf("no answer in 16s: %w", core.ErrUnreachable)},
}

func onHost(t *testing.T, st hostState) (Model, *core.Core, *hostRemotes) {
	t.Helper()
	m, c, _ := setup(t)
	m, r := withBox(t, m, c)
	h := &hostRemotes{remotes: r, silent: st.silent, runErr: st.runErr}
	if st.silent != nil {
		r.boards[0].Err, r.boards[0].FailingSince = errors.New("no answer in 8s"), time.Now().Add(-2*time.Minute)
	}
	c.Remotes = h
	return refresh(t, m), c, h
}

// step feeds msg to the board and returns the command it dispatched, without running it.
func step(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func farRow(t *testing.T, m Model) string {
	t.Helper()
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	for i, l := range lines {
		if strings.Contains(l, "Far work") && i+1 < len(lines) {
			for _, n := range lines[i+1 : min(len(lines), i+3)] {
				if strings.HasPrefix(n, "         ") {
					return strings.TrimSpace(n)
				}
			}
		}
	}
	return ""
}

func TestKillOnAHostNeverWaitsOnItAndTheRowSaysHowItStands(t *testing.T) {
	for _, st := range hostStates {
		t.Run(st.name, func(t *testing.T) {
			m, c, h := onHost(t, st)
			m = press(t, to(t, m, mission("far-lead-0001")), "K")
			m, cmd := step(t, m, keyOf("y"))
			if st.silent == nil {
				if note := farRow(t, m); !strings.HasPrefix(note, "killing…") {
					t.Fatalf("while the kill runs the row says %q", note)
				}
			}
			start := time.Now()
			m, _ = step(t, m, cmd())
			m = refresh(t, m)
			if took := time.Since(start); took > time.Second {
				t.Errorf("kill took %s", took)
			}
			ran := len(h.ran) > 0
			if ran == (st.silent != nil) {
				t.Errorf("ssh ran %v on a host whose state is %s", h.ran, st.name)
			}
			pending, _ := c.Store.PendingRelays()
			note := farRow(t, m)
			switch {
			case st.silent == nil && st.runErr == nil:
				if len(pending) != 0 || note != "killed claude far-lead" {
					t.Errorf("pending %v, row %q", pending, note)
				}
			default:
				if len(pending) != 1 || !strings.Contains(pending[0].Body, `"kill","--yes","far-lead-0001"`) || !strings.HasPrefix(note, "kill pending since") {
					t.Errorf("pending %v, row %q", pending, note)
				}
				if st.silent != nil && !strings.Contains(note, "@boxmachine not answering: no answer 2m") {
					t.Errorf("row %q does not say the host is not answering", note)
				}
			}
		})
	}
}

func TestGoingToASessionOnAHostChecksItAnswersBeforeTakingTheTerminal(t *testing.T) {
	for _, st := range hostStates {
		t.Run(st.name, func(t *testing.T) {
			m, _, h := onHost(t, st)
			m = to(t, m, mission("far-lead-0001"))
			m, cmd := step(t, m, keyOf("o"))
			if cmd == nil {
				t.Fatalf("o dispatched nothing; message %q", m.message)
			}
			if st.silent == nil {
				if note := farRow(t, m); !strings.HasPrefix(note, "reaching @boxmachine…") {
					t.Fatalf("while it reaches the host the row says %q", note)
				}
			}
			out := cmd()
			reached, ok := out.(reachedMsg)
			if ok != (st.silent == nil && st.runErr == nil) {
				t.Fatalf("o on a host %s ended with %T", st.name, out)
			}
			if st.silent != nil && h.reached != 0 {
				t.Errorf("reached a host the board already knows is silent")
			}
			m, _ = step(t, m, out)
			note := farRow(t, m)
			switch {
			case ok:
				if reached.open == nil || note != "" {
					t.Errorf("attach %v, row %q", reached.open, note)
				}
			default:
				if !strings.HasPrefix(note, "open failed:") {
					t.Errorf("row %q", note)
				}
			}
		})
	}
}

func TestAKillQueuedByAnyNameForTheSessionShowsPendingOnItsRow(t *testing.T) {
	for _, ref := range []string{"far-lead-0001", "far-lead", "far-wt-lead"} {
		t.Run(ref, func(t *testing.T) {
			m, c, h := onHost(t, hostStates[1])
			h.boards[0].Launched = []core.Launched{{Key: core.RemoteKey(box, "far-wt-lead"), Harness: "claude", Session: "far-lead-0001", State: "running"}}
			if err := c.Relay(box, core.RelayYM, map[string][]string{"args": {"kill", "--yes", ref}}); err != nil {
				t.Fatal(err)
			}
			if note := farRow(t, to(t, refresh(t, m), mission("far-lead-0001"))); !strings.HasPrefix(note, "kill pending since") {
				t.Errorf("row %q", note)
			}
		})
	}
}
