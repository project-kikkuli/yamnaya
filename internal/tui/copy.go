package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/clipboard"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

// clipWay is how y reaches the clipboard: pbcopy on this Mac, else OSC 52 through the terminal the board is drawn on.
var clipWay = func() clipboard.Way { return clipboard.Pick(clipboard.Here()) }

// copyOut puts what text yields on the clipboard as clean text.
func (m Model) copyOut(what string, text func() (string, error)) tea.Cmd {
	return func() tea.Msg {
		t, err := text()
		if err != nil {
			return doneMsg{"", err}
		}
		t = clipboard.Clean(t)
		done := fmt.Sprintf("copied %s, %d lines", what, strings.Count(t, "\n"))
		if clipWay() == clipboard.Pbcopy {
			_, err := clipboard.Copy(t)
			return doneMsg{done, err}
		}
		return tea.BatchMsg{tea.SetClipboard(t), func() tea.Msg { return doneMsg{done + " through the terminal", nil} }}
	}
}

// copyKey is y: on a text page it copies what the page shows, unwrapped; on an item, the item; on a goal or chief, it asks
// whether to copy the brief or the context.
func (m Model) copyKey(r row) (tea.Model, tea.Cmd) {
	if m.mode == modeScreen {
		text := ansi.Strip(m.screen)
		if m.screenCopy != "" {
			text = m.screenCopy
		}
		return m, m.copyOut("this page", func() (string, error) { return text, nil })
	}
	it := r.item
	if m.mode == modeWhy {
		it = m.why
	}
	if it != nil {
		item, c := *it, m.core
		return m, m.copyOut("item "+item.Qualified(), func() (string, error) { return c.Store.Nicked(c.ItemText(item)), nil })
	}
	n, ok := m.nodeOf(r)
	if !ok {
		m.message = "nothing to copy here; y copies a goal's or chief's brief or context, an item, or a text page"
		return m, nil
	}
	m.menu, m.menuFor, m.optCur = []menuOpt{
		{"b", "brief: " + n.Fresh.String() + ", the page b shows"},
		{"c", "context: what " + n.Manager.Label() + " is about now, its newest revision"},
	}, n.Manager.ID, 0
	m.startInput(inCopyMenu, "", "", "", false)
	return m, nil
}

func (m Model) copyMenu(key string) (tea.Model, tea.Cmd) {
	m.mode = m.back
	n, ok := m.board.Node(m.menuFor)
	if !ok {
		return m, nil
	}
	c, id := m.core, n.Manager.ID
	switch key {
	case "b":
		if _, _, remote := core.SplitRemote(id); remote && n.Manager.Host != "" && n.Fresh.Brief != nil {
			page := n.Fresh.Brief.Markdown(n.Manager.Label())
			return m, m.copyOut(id+"'s brief", func() (string, error) { return page, nil })
		}
		return m, m.copyOut(id+"'s brief", func() (string, error) { return c.BriefPage(id) })
	case "c":
		return m, m.copyOut(id+"'s context", func() (string, error) {
			_, text, _, err := c.Context(id)
			return text, err
		})
	}
	return m, nil
}
