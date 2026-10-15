package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

type harness struct{ sessions map[int]core.Session }

func (h harness) Name() string                           { return "claude" }
func (h harness) Owns(p proc.Proc) bool                  { return p.Args[0] == "claude" }
func (h harness) SessionID(p proc.Proc) string           { return h.sessions[p.PID].ID }
func (h harness) Resume(id string) []string              { return []string{"claude", "--resume", id} }
func (h harness) ParsePrompt(string) (core.Prompt, bool) { return core.Prompt{}, false }
func (h harness) Discover(procs []proc.Proc) []core.Session {
	var out []core.Session
	for _, p := range procs {
		s := h.sessions[p.PID]
		s.Harness, s.PID, s.TTY = "claude", p.PID, p.TTY
		out = append(out, s)
	}
	return out
}

type host struct {
	typed   []string
	focused []string
}

func (h *host) Name() string { return "tmux" }
func (h *host) Locate(tty string) (core.Handle, bool) {
	return core.Handle{Host: "tmux", ID: tty, Label: tty}, true
}
func (h *host) Focus(hd core.Handle) (string, error) {
	h.focused = append(h.focused, hd.ID)
	return "", nil
}
func (h *host) Type(hd core.Handle, text string) error {
	h.typed = append(h.typed, hd.ID+" "+text)
	return nil
}
func (h *host) Screen(core.Handle) (string, error) { return "", nil }

func setup(t *testing.T) (Model, *core.Core, *host) {
	t.Helper()
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &host{}
	sessions := map[int]core.Session{
		1: {ID: "lead", Title: "Lead work", Status: core.Idle, Crew: []core.Member{{
			ID: "a1", Name: "Run the tests", Type: "general-purpose", Status: core.Done, Brief: "Run every test and report failures.",
			Facts: core.Facts{Result: "All 42 tests pass.", Trail: []core.Action{{Tool: "Bash", Detail: "go test ./..."}}},
		}}},
		2: {ID: "twin", Title: "Twin work", Status: core.Busy},
		3: {ID: "twin", Title: "Twin work", Status: core.Busy},
	}
	procs := []proc.Proc{
		{PID: 1, PPID: 0, TTY: "/dev/a", Args: []string{"claude"}},
		{PID: 2, PPID: 0, TTY: "/dev/b", Args: []string{"claude"}},
		{PID: 3, PPID: 0, TTY: "/dev/c", Args: []string{"claude"}},
	}
	c := &core.Core{Store: st, Harnesses: []core.Harness{harness{sessions}}, Hosts: []core.Host{h},
		Procs: func() ([]proc.Proc, error) { return procs, nil }}
	for _, q := range []string{"Ship it?", "Rename the flag?"} {
		if _, err := c.AddItem(core.Item{Kind: core.KindDecision, Question: q, Rec: "yes", Session: "lead"}); err != nil {
			t.Fatal(err)
		}
	}
	m := New(c)
	return refresh(t, m), c, h
}

func refresh(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.Update(m.refresh(false)())
	return next.(Model)
}

// press sends keys and runs any command they return, feeding results back like the Bubble Tea loop.
func press(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "ctrl+u":
			msg = tea.KeyMsg{Type: tea.KeyCtrlU}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		next, cmd := m.Update(msg)
		m = next.(Model)
		for cmd != nil {
			out := cmd()
			if _, ok := out.(tea.QuitMsg); ok {
				t.Fatalf("quit after %q", k)
			}
			next, cmd = m.Update(out)
			m = next.(Model)
			if _, ok := out.(doneMsg); !ok {
				cmd = nil
			}
		}
	}
	return m
}

func TestAnswerInlineTypesIntoTheIdleAgent(t *testing.T) {
	m, c, h := setup(t)
	if v := m.View(); !strings.Contains(v, "NEEDS YOU (2)") || !strings.Contains(v, "D1") || !strings.Contains(v, "enter expand") {
		t.Fatalf("view:\n%s", v)
	}
	m = press(t, m, "down", "down", "down", "a", "ctrl+u", "ship", " ", "it", "enter")
	it, _ := c.Store.Item("D1")
	if it.State != core.ItemAnswered || it.Answer != "ship it" {
		t.Fatalf("D1 = %+v", it)
	}
	if len(h.typed) != 1 || h.typed[0] != "/dev/a re D1: ship it" {
		t.Fatalf("typed %q", h.typed)
	}
	if !strings.Contains(m.message, "typed into the agent's terminal") || strings.Contains(m.View(), "NEEDS YOU (2)") {
		t.Fatalf("message %q\n%s", m.message, m.View())
	}
}

func TestAnswerDefaultsToTheRecAndTypingReplacesIt(t *testing.T) {
	m, c, _ := setup(t)
	m = press(t, m, "down", "down", "down", "a", "enter")
	if it, _ := c.Store.Item("D1"); it.Answer != "yes" {
		t.Fatalf("enter on the default answered %q, want the rec", it.Answer)
	}
	press(t, m, "a", "h", "i", "enter")
	if it, _ := c.Store.Item("D2"); it.Answer != "hi" {
		t.Fatalf("typed answer = %q, want it to replace the rec", it.Answer)
	}
}

func TestEnterExpandsAnItemWhoseKeysActOnIt(t *testing.T) {
	m, c, h := setup(t)
	m = press(t, m, "down", "down", "down", "down", "enter")
	if m.mode != modeWhy || m.why.ID != "D2" || !strings.Contains(m.View(), "Rename the flag?") {
		t.Fatalf("enter did not expand D2: mode %v\n%s", m.mode, m.View())
	}
	if len(h.focused) != 0 {
		t.Fatalf("enter on an item jumped to %v", h.focused)
	}
	m = press(t, m, "l")
	if it, _ := c.Store.Item("D2"); it.State != core.ItemSnoozed {
		t.Fatalf("l in the expanded view left D2 %s", it.State)
	}
	m = press(t, m, "enter", "o")
	if len(h.focused) != 1 || h.focused[0] != "/dev/a" {
		t.Fatalf("o from the expanded view focused %v", h.focused)
	}
}

func TestCursorStaysOnItsRowAcrossRefreshes(t *testing.T) {
	m, c, _ := setup(t)
	m = press(t, m, "down", "down", "down", "down")
	if _, err := c.AddItem(core.Item{Kind: core.KindChore, Question: "Urgent", Session: "lead", Blocks: 5}); err != nil {
		t.Fatal(err)
	}
	m = refresh(t, m)
	if r, _ := m.selected(); r.item == nil || r.item.ID != "D2" {
		t.Fatalf("cursor moved to %+v after a new item sorted above it", r)
	}
}

func TestLaterDismissAndEscapeLeaveItemsAlone(t *testing.T) {
	m, c, _ := setup(t)
	m = press(t, m, "down", "down", "down", "a", "esc", "l")
	if it, _ := c.Store.Item("D1"); it.State != core.ItemSnoozed {
		t.Fatalf("D1 = %s", it.State)
	}
	m = press(t, m, "x", "not", " ", "needed", "enter")
	if it, _ := c.Store.Item("D2"); it.State != core.ItemDismissed || it.Reason != "not needed" {
		t.Fatalf("D2 = %+v", it)
	}
	if !strings.Contains(m.View(), "NEEDS YOU (0)") {
		t.Fatal(m.View())
	}
}

func TestMissionDetailRenameAndDuplicatePick(t *testing.T) {
	m, c, h := setup(t)
	m = press(t, m, "down")
	r, _ := m.selected()
	if r.project == nil || r.need != nil {
		t.Fatalf("cursor on %+v, want the first mission", r)
	}
	m = press(t, m, "r", "ctrl+u", "Launch", "enter")
	b, _ := c.Board()
	if p, _ := b.Project("lead"); p.Name != "Launch" {
		t.Fatalf("name = %q", p.Name)
	}
	m = press(t, m, "down")
	if !strings.Contains(m.View(), "2 COPIES") {
		t.Fatalf("duplicate not flagged:\n%s", m.View())
	}
	m = press(t, m, "o")
	if m.mode != modePick || len(m.targets) != 2 {
		t.Fatalf("mode %v targets %d", m.mode, len(m.targets))
	}
	m = press(t, m, "down", "enter")
	if len(h.focused) != 1 || h.focused[0] != "/dev/c" {
		t.Fatalf("focused %v", h.focused)
	}
	m = press(t, m, "enter")
	if m.mode != modeDetail || !strings.Contains(m.View(), "SESSIONS") {
		t.Fatalf("enter on a mission should open its detail:\n%s", m.View())
	}
	m = press(t, m, "esc")
	if m.mode != modeList {
		t.Fatalf("esc left mode %v", m.mode)
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); cmd == nil {
		t.Fatal("q did not quit")
	}
}

func TestEnterDrillsFromRepoToAgentAndEscClimbsBack(t *testing.T) {
	m, _, _ := setup(t)
	steps := []struct {
		mode mode
		want string
	}{
		{modeRepo, "MISSIONS"},
		{modeDetail, "SESSIONS AND CREW"},
		{modeSession, "CREW (1)"},
		{modeAgent, "Run every test and report failures."},
	}
	for _, st := range steps {
		m = press(t, m, "enter")
		if m.mode != st.mode || !strings.Contains(m.View(), st.want) {
			t.Fatalf("after enter: mode %v, want %v showing %q:\n%s", m.mode, st.mode, st.want, m.View())
		}
	}
	if v := m.View(); !strings.Contains(v, "All 42 tests pass.") || !strings.Contains(v, "Bash: go test ./...") {
		t.Fatalf("agent view lacks result or trail:\n%s", v)
	}
	for _, want := range []mode{modeSession, modeDetail, modeRepo, modeList} {
		m = press(t, m, "esc")
		if m.mode != want {
			t.Fatalf("esc left mode %v, want %v", m.mode, want)
		}
	}
	if r, _ := m.selected(); r.repo == nil || r.project != nil {
		t.Fatalf("esc back to the board put the cursor on %+v, want the repo it left from", r)
	}
}
