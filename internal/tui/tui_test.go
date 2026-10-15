package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
func (h harness) Composer(screen string) (string, bool)  { return screen, true }
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
	box     map[string]string
	typed   []string
	focused []string
	// away are terminals no host reaches, like an iTerm window.
	away map[string]bool
}

func (h *host) Name() string { return "tmux" }
func (h *host) Locate(tty string) (core.Handle, bool) {
	if h.away[tty] {
		return core.Handle{}, false
	}
	return core.Handle{Host: "tmux", ID: tty, Label: tty}, true
}
func (h *host) Focus(hd core.Handle) (string, error) {
	h.focused = append(h.focused, hd.ID)
	return "", nil
}
func (h *host) Paste(hd core.Handle, text string) error {
	if h.box == nil {
		h.box = map[string]string{}
	}
	h.box[hd.ID] += text
	return nil
}
func (h *host) Enter(hd core.Handle) error {
	h.typed = append(h.typed, hd.ID+" "+h.box[hd.ID])
	delete(h.box, hd.ID)
	return nil
}
func (h *host) Screen(hd core.Handle) (string, error) { return h.box[hd.ID], nil }

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
			switch out.(type) {
			case doneMsg, adoptedMsg:
			default:
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
	m = press(t, m, "a", "ctrl+u", "ship", " ", "it", "enter")
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
	m = press(t, m, "a", "enter")
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
	m = press(t, m, "down", "enter")
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
	m = press(t, m, "down")
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
	m = press(t, m, "a", "esc", "l")
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
	m = press(t, m, "down", "down", "down")
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
	m = press(t, m, "down", "down")
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

type launches struct {
	dormant []core.Dormant
	revived []string
	adopted []string
	sent    []string
}

func (l *launches) Adopt(s core.Live, say func(string)) (core.Target, error) {
	l.adopted = append(l.adopted, s.Key())
	return core.Target{Session: s.Session, Handle: &core.Handle{Host: "tmux", ID: "/dev/ym", Label: "ym"}}, nil
}

func (l *launches) Send(s core.Live, text string) (string, error) {
	l.sent = append(l.sent, s.Key()+" "+text)
	return "sent to " + s.Key(), nil
}

func (l *launches) Dormant() []core.Dormant { return l.dormant }
func (l *launches) Revive(key string) (string, error) {
	l.revived = append(l.revived, key)
	return "revived " + key, nil
}

func TestExitedLaunchesShowInTheirMissionAndEnterRevives(t *testing.T) {
	m, c, _ := setup(t)
	l := &launches{dormant: []core.Dormant{
		{Key: "fix-login", Harness: "claude", Session: "kid", Parent: "lead", Title: "Fix login", State: "exited 1"},
		{Key: "docs-pass", Harness: "codex", Title: "Docs pass", State: "gone"},
	}}
	c.Launches = l
	m = refresh(t, m)
	if v := m.View(); !strings.Contains(v, "Docs pass") || !strings.Contains(v, "1 exited") {
		t.Fatalf("a mission with only an exited launch is missing from the board:\n%s", v)
	}
	m = press(t, m, "down", "down", "down", "enter")
	if !strings.Contains(m.View(), "exited 1") {
		t.Fatalf("lead mission lacks its exited launch:\n%s", m.View())
	}
	for range 10 {
		if r, _ := m.selected(); r.dormant != nil {
			break
		}
		m = press(t, m, "down")
	}
	m = press(t, m, "enter")
	if len(l.revived) != 1 || l.revived[0] != "fix-login" || m.message != "revived fix-login" {
		t.Fatalf("revived %v, message %q", l.revived, m.message)
	}
}

func TestItemsFromEndedSessionsCollapseIntoOneStaleLine(t *testing.T) {
	m, c, _ := setup(t)
	for _, q := range []string{"Old question?", "Older question?"} {
		if _, err := c.AddItem(core.Item{Kind: core.KindDecision, Question: q, Session: "ended", Blocks: 9}); err != nil {
			t.Fatal(err)
		}
	}
	m = refresh(t, m)
	v := m.View()
	if !strings.Contains(v, "NEEDS YOU (2)") || !strings.Contains(v, "stale (2)") || strings.Contains(v, "Old question?") {
		t.Fatalf("stale items should sit collapsed below the live ones:\n%s", v)
	}
	m = press(t, m, "down", "down", "enter")
	if !strings.Contains(m.View(), "Older question?") {
		t.Fatalf("enter on the stale line did not expand it:\n%s", m.View())
	}
	m = press(t, m, "x", "enter")
	for _, id := range []string{"D3", "D4"} {
		if it, _ := c.Store.Item(id); it.State != core.ItemDismissed {
			t.Errorf("%s = %s after dismissing every stale item", id, it.State)
		}
	}
	if it, _ := c.Store.Item("D1"); it.State != core.ItemOpen {
		t.Errorf("a live item was dismissed with the stale ones")
	}
}

type meter struct{ f core.Fleet }

func (m meter) Fleet(time.Time) core.Fleet { return m.f }

func TestSpendShowsAtEveryLevel(t *testing.T) {
	m, c, _ := setup(t)
	c.Meter = meter{core.Fleet{Ready: true,
		Sessions: map[string]core.SessionSpend{
			"lead": {ID: "lead", Total: core.Spend{USD: 3}},
			"twin": {ID: "twin", Total: core.Spend{USD: 40}},
		},
		Agents: map[string]core.Spend{"lead/a1": {USD: 1.25}},
	}}
	m = refresh(t, m)
	board := m.View()
	for _, want := range []string{"$43.00", "$3.00", "$40.00"} {
		if !strings.Contains(board, want) {
			t.Fatalf("board lacks %s (repo total, then each mission):\n%s", want, board)
		}
	}
	levels := []struct {
		keys []string
		want string
	}{
		{[]string{"down", "down", "enter", "enter"}, "$1.25"},
		{[]string{"enter"}, "$3.00"},
		{[]string{"enter"}, "$1.25"},
	}
	for _, l := range levels {
		m = press(t, m, l.keys...)
		if !strings.Contains(m.View(), l.want) {
			t.Fatalf("mode %v lacks %s:\n%s", m.mode, l.want, m.View())
		}
	}
}

func TestHealthOpensOnHAndEscReturns(t *testing.T) {
	m, c, _ := setup(t)
	c.Health = func() (string, error) { return "week of  answered\n2026-10-12  6", nil }
	m = press(t, m, "h")
	if m.mode != modeHealth || !strings.Contains(m.View(), "2026-10-12  6") {
		t.Fatalf("h did not show health: mode %v\n%s", m.mode, m.View())
	}
	m = press(t, m, "esc")
	if m.mode != modeList {
		t.Fatalf("esc left mode %v", m.mode)
	}
}

func TestALongCrewKeepsTheHeaderAndSessionTitleOnScreen(t *testing.T) {
	m, c, _ := setup(t)
	lead := c.Harnesses[0].(harness).sessions[1]
	for i := range 60 {
		lead.Crew = append(lead.Crew, core.Member{ID: fmt.Sprint("w", i), Name: fmt.Sprint("worker ", i), Status: core.Busy})
	}
	c.Harnesses[0].(harness).sessions[1] = lead
	m.height = 30
	m = refresh(t, m)
	m = press(t, m, "down", "down", "enter", "enter", "enter")
	v := m.View()
	if m.mode != modeSession || !strings.Contains(v, " ym ") || !strings.Contains(v, "Lead work") || !strings.Contains(v, "worker 0") {
		t.Fatalf("session view with 61 crew lost its header, title or first worker:\n%s", v)
	}
	m = press(t, m, strings.Split(strings.Repeat("j", 50), "")...)
	if v := m.View(); !strings.Contains(v, " ym ") || !strings.Contains(v, "worker 49") || strings.Count(v, "\n") >= m.height {
		t.Fatalf("scrolled to worker 49, want it on screen under the header in %d lines:\n%s", m.height, v)
	}
}

func TestMissionItemsFromTwoProjectsShowTheirProject(t *testing.T) {
	m, c, _ := setup(t)
	if _, err := c.AddItem(core.Item{Kind: core.KindDecision, Question: "Other repo?", Session: "lead", Scope: "/src/other/.git"}); err != nil {
		t.Fatal(err)
	}
	m = press(t, refresh(t, m), "down", "down", "down", "enter", "enter")
	v := m.View()
	if m.mode != modeDetail || !strings.Contains(v, "other:D1") || strings.Count(v, " D1 ") != 1 {
		t.Fatalf("mission items from two projects should name the project where IDs repeat:\n%s", v)
	}
}

func TestHeaderSaysSpendIsStillLoading(t *testing.T) {
	now := time.Now()
	f := core.Fleet{Limits: []core.Limit{{Harness: "claude", Name: "5h", Used: 30, Window: 5 * time.Hour, ResetsAt: now.Add(time.Hour), Seen: now}}}
	if l := fleetLine(f, "mac", now); !strings.Contains(l, "claude") || !strings.Contains(l, "reading transcripts") {
		t.Fatalf("header before the first usage scan = %q, want the claude limits and a note that spend is loading", l)
	}
}

func TestOGoesToASessionWithNoCrewOrItems(t *testing.T) {
	m, _, _ := setup(t)
	m = press(t, m, "down", "down", "down", "down", "enter", "enter")
	if m.mode != modeSession || !strings.Contains(m.View(), "Twin work") {
		t.Fatalf("want the twin session view, mode %v:\n%s", m.mode, m.View())
	}
	m = press(t, m, "o")
	if m.mode != modePick || len(m.targets) != 2 {
		t.Fatalf("o in a session view with nothing selected: mode %v, %d targets", m.mode, len(m.targets))
	}
}

func TestGoingToASessionInAnotherTerminalAdoptsItOnceConfirmed(t *testing.T) {
	m, c, h := setup(t)
	l := &launches{}
	c.Launches = l
	h.away = map[string]bool{"/dev/a": true}
	m = refresh(t, m)
	m = press(t, m, "down", "down", "down", "enter")
	if r, _ := m.selected(); r.session == nil || r.session.Key() != "lead" || !strings.Contains(m.View(), "o adopt") {
		t.Fatalf("lead session row should offer adopt:\n%s", m.View())
	}
	for _, c := range []struct {
		keys    []string
		adopted int
	}{
		{[]string{"o", "esc"}, 0},
		{[]string{"o", "y", "enter"}, 1},
	} {
		m = press(t, m, c.keys...)
		if len(l.adopted) != c.adopted {
			t.Fatalf("after %v adopted %v", c.keys, l.adopted)
		}
	}
	if !strings.Contains(strings.Join(h.focused, " "), "/dev/ym") {
		t.Fatalf("adopted session was not shown; focused %v", h.focused)
	}
	h.away = nil
	press(t, refresh(t, m), "o")
	if len(l.adopted) != 1 || !strings.Contains(strings.Join(h.focused, " "), "/dev/a") {
		t.Fatalf("a session tmux reaches must be focused, not adopted: adopted %v focused %v", l.adopted, h.focused)
	}
}

func TestSendComposesAFollowUpForTheSelectedSession(t *testing.T) {
	m, c, _ := setup(t)
	l := &launches{}
	c.Launches = l
	m = press(t, m, "down", "down", "down", "enter")
	m = press(t, m, "s", "also", " ", "rebase", "enter")
	if len(l.sent) != 1 || l.sent[0] != "lead also rebase" || m.message != "sent to lead" {
		t.Fatalf("sent %v, message %q", l.sent, m.message)
	}
	m = press(t, m, "enter", "s", "esc")
	if len(l.sent) != 1 {
		t.Fatalf("esc should cancel the compose line; sent %v", l.sent)
	}
}
