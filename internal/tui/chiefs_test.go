package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestChiefCreationAttachmentConversationAndBriefNavigation(t *testing.T) {
	s, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := &core.Core{Store: s, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	w, err := s.Work.Ensure("query", "query", "Compatibility")
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Board()
	if err != nil {
		t.Fatal(err)
	}
	m := Model{core: c, loaded: true, board: b, width: 120, height: 45, cursor: 1}
	m.workID = w.ID
	m.mode = modeWork
	key := func(k string) {
		t.Helper()
		next, cmd := m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		m = next.(Model)
		if cmd != nil {
			msg := cmd()
			if d, ok := msg.(doneMsg); ok && d.err != nil {
				t.Fatal(d.err)
			}
		}
	}
	enter := func() {
		t.Helper()
		next, cmd := m.inputKey(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(Model)
		if cmd != nil {
			msg := cmd()
			if d, ok := msg.(doneMsg); ok && d.err != nil {
				t.Fatal(d.err)
			}
		}
	}
	key("M")
	key("n")
	m.input = []rune("General")
	enter()
	m.input = []rune("Keep compatibility work coherent")
	enter()
	m.input = []rune("Investigate and coordinate; reserve publication for me")
	enter()
	b, err = c.Board()
	if err != nil {
		t.Fatal(err)
	}
	m.board = b
	if m.mode != modeChiefs || len(m.rows()) != 1 {
		t.Fatalf("chief wizard did not return to management: %+v", m.rows())
	}
	a := *m.rows()[0].chief
	links, _ := s.Work.Attachments()
	if len(links) != 1 || links[0].Work != w.ID {
		t.Fatal("selected project was not attached")
	}
	key("D")
	rules, _ := s.Work.Rules()
	if len(rules) != 1 || rules[0].Repo != "" {
		t.Fatal("global default did not persist")
	}
	key("s")
	m.input = []rune("Prioritize callers before cleanup")
	enter()
	events, err := s.Work.History(a.Workspace, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Type == "message" && e.Text == "Prioritize callers before cleanup" && e.Actor == "owner" {
			found = true
		}
	}
	if !found {
		t.Fatal("chief conversation lost direction provenance")
	}
	x, err := s.Work.AgentSnapshot(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Work.Checkpoint(a.ID, "fixture", work.Checkpoint{Brief: work.Brief{Assessment: "Caller compatibility takes priority", Context: x.Workspace.Context.Revision}, Inputs: x.Pending, WaitFor: "Changed compatibility evidence", Next: "Investigate the attached work"})
	if err != nil {
		t.Fatal(err)
	}
	m.board, err = c.Board()
	if err != nil {
		t.Fatal(err)
	}
	key("enter")
	page := ansi.Strip(m.workView().String())
	if !strings.Contains(page, "Caller compatibility takes priority") || !strings.Contains(page, "Compatibility") {
		t.Fatalf("chief brief lost assessment or attachments: %s", page)
	}
	key("C")
	if m.workPanel != 2 {
		t.Fatal("context shortcut changed")
	}
	key("B")
	if m.workPanel != 0 {
		t.Fatal("brief shortcut changed")
	}
}
