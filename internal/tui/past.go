package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// pastList is every conversation the harnesses' history lists, read when E opens, and the filter typed over it.
type pastList struct {
	loaded bool
	rows   []core.Past
	err    error
	query  string
}

type pastMsg pastList

// pastPreview is how many of the selected conversation's latest prompts the footer shows.
const pastPreview = 6

const pastRowKey = "panel:past:"

func (m Model) loadPast() tea.Cmd {
	history := m.core.History
	return func() tea.Msg {
		if history == nil {
			return pastMsg{loaded: true}
		}
		ps, err := history()
		return pastMsg{loaded: true, rows: ps, err: err}
	}
}

// shownPast are the conversations the board does not already show that match the filter.
func (m Model) shownPast() []core.Past {
	return core.FilterPast(core.Unlaunched(m.pn.past.rows, m.board), m.pn.past.query)
}

func (m Model) pastRows() []prow {
	d := m.pn.past
	rows := []prow{{text: ""}, {text: pal.bold.Render("other conversations") + pal.dim.Render("  Claude and Codex history ym did not launch; enter resumes one on ym's server")}}
	switch {
	case !d.loaded:
		return append(rows, prow{text: pal.dim.Render("   reading…")})
	case d.err != nil:
		rows = append(rows, prow{text: pal.red.Render("   error: " + d.err.Error())})
	}
	ps := m.shownPast()
	if len(ps) == 0 && d.query != "" {
		rows = append(rows, prow{text: pal.dim.Render("   none match " + d.query)})
	} else if len(ps) == 0 {
		rows = append(rows, prow{text: pal.dim.Render("   none")})
	}
	for _, p := range ps {
		first := p.Title
		if len(p.Prompts) > 0 {
			first = p.Prompts[0]
		}
		text := p.Last.Local().Format("2006-01-02 15:04") + " " + pad(p.Harness, 6) + " " + pad(filepath.Base(p.Cwd), 20) + " " + first
		if p.Transcript == "" {
			text = pal.dim.Render(text + "  (transcript gone)")
		}
		key := pastRowKey + p.Session
		rows = append(rows, prow{text: text, key: key, does: []pdo{{"enter", "resume", func(m Model) (tea.Model, tea.Cmd) {
			c := m.core
			return m, m.act(key, "resuming "+core.Short(p.Session), "resume", func() (string, error) { return c.ResumePast(p) })
		}}}})
	}
	return rows
}

// pastKey types k into the filter over other conversations, reporting whether it took k; E and ? keep their meaning until
// something is typed.
func (m Model) pastKey(k tea.KeyPressMsg) (Model, bool) {
	q := []rune(m.pn.past.query)
	switch key := k.String(); {
	case key == "backspace" && len(q) > 0:
		q = q[:len(q)-1]
	case key == "ctrl+u" || key == "esc" && len(q) > 0:
		q = nil
	case k.Text != "" && !k.Mod.Contains(tea.ModCtrl) && !k.Mod.Contains(tea.ModAlt) && (len(q) > 0 || k.Text != "E" && k.Text != "?"):
		q = append(q, []rune(k.Text)...)
	default:
		return m, false
	}
	m.pn.past.query = string(q)
	rows, _ := m.panelRows()
	m.pn.cur = m.firstPanelRow(0, 1)
	for i, r := range rows {
		if strings.HasPrefix(r.key, pastRowKey) {
			m.pn.cur = i
			break
		}
	}
	return m, true
}

// selectedPast is the conversation under the panel's cursor.
func (m Model) selectedPast() (core.Past, bool) {
	rows, _ := m.panelRows()
	if m.pn.cur < 0 || m.pn.cur >= len(rows) {
		return core.Past{}, false
	}
	id, ok := strings.CutPrefix(rows[m.pn.cur].key, pastRowKey)
	if !ok {
		return core.Past{}, false
	}
	for _, p := range m.shownPast() {
		if p.Session == id {
			return p, true
		}
	}
	return core.Past{}, false
}

// pastFooter previews the conversation under E's cursor and shows the filter typed over them.
func (m Model) pastFooter() []string {
	if m.mode != modePanel || m.pn.kind != panelLaunched {
		return nil
	}
	var lines []string
	if p, ok := m.selectedPast(); ok {
		about := fmt.Sprintf("  %s, %s, last %s", p.Cwd, plural(len(p.Prompts), "prompt"), p.Last.Local().Format("2006-01-02 15:04"))
		if p.Title != "" {
			about = "  " + p.Title + about
		}
		if p.Transcript == "" {
			about += ", transcript gone"
		}
		lines = append(lines, " "+pal.bold.Render(p.Harness+" "+p.Session)+pal.dim.Render(about))
		from := max(0, len(p.Prompts)-pastPreview)
		if from > 0 {
			lines = append(lines, pal.dim.Render(fmt.Sprintf("   … %d earlier", from)))
		}
		for i, text := range p.Prompts[from:] {
			lines = append(lines, fmt.Sprintf("   %d. %s", from+i+1, text))
		}
	}
	if q := m.pn.past.query; q != "" {
		return append(lines, " "+pal.bold.Render("filter:")+" "+q+"█"+pal.dim.Render("   exact, space = AND · esc clears"))
	}
	return append(lines, pal.dim.Render(" type to filter other conversations (exact, space = AND)"))
}
