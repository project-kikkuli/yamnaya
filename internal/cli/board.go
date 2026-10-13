package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/project-kikkuli/yamnaya/internal/harness"
	"github.com/project-kikkuli/yamnaya/internal/signals"
	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/spf13/cobra"
)

const boardRefresh = 1500 * time.Millisecond

type rowKind int

const (
	rowProject rowKind = iota
	rowWindow
	rowUnmanaged
)

type row struct {
	kind    rowKind
	project string
	window  string
	target  string
}

type promptField struct {
	label string
	value string
}

type prompt struct {
	fields []promptField
	at     int
	submit func(values []string) error
}

type snapMsg struct {
	snap signals.Snapshot
	err  error
}

type tickMsg struct{}

type boardModel struct {
	a      *app
	reader *signals.Reader
	now    func() time.Time

	snap   signals.Snapshot
	sel    row
	width  int
	status string

	itemsOf  string
	items    []store.Item
	itemSel  int
	expanded string

	prompt *prompt
}

func newBoard(a *app) *boardModel {
	return &boardModel{a: a, reader: signals.NewReader(a.store, a.tmux), now: time.Now}
}

func (m *boardModel) Init() tea.Cmd {
	return tea.Batch(m.load, tick())
}

func tick() tea.Cmd {
	return tea.Tick(boardRefresh, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *boardModel) load() tea.Msg {
	snap, err := m.reader.Snapshot()
	return snapMsg{snap, err}
}

// refresh reloads the snapshot and the open item list synchronously after an action.
func (m *boardModel) refresh() {
	m.apply(m.load().(snapMsg))
}

func (m *boardModel) apply(msg snapMsg) {
	if msg.err != nil {
		m.status = msg.err.Error()
		return
	}
	m.snap = msg.snap
	if m.itemsOf != "" {
		items, err := m.a.store.Items(m.itemsOf, false)
		if err != nil {
			m.status = err.Error()
			return
		}
		m.items = items
		m.itemSel = clamp(m.itemSel, len(items))
	}
	m.fixSelection()
}

func clamp(i, n int) int {
	if i >= n {
		i = n - 1
	}
	if i < 0 {
		i = 0
	}
	return i
}

func (m *boardModel) expandedProject(p signals.Project) bool {
	return p.Open > 0 || p.NeedPermission() > 0 || (m.sel.kind != rowUnmanaged && m.sel.project == p.Name)
}

func (m *boardModel) rows() []row {
	var rows []row
	for _, p := range m.snap.Projects {
		rows = append(rows, row{kind: rowProject, project: p.Name})
		if m.expandedProject(p) {
			for _, w := range p.Windows {
				rows = append(rows, row{kind: rowWindow, project: p.Name, window: w.Name, target: w.TmuxTarget})
			}
		}
	}
	for _, u := range m.snap.Unmanaged {
		rows = append(rows, row{kind: rowUnmanaged, target: u.ID, window: u.Name})
	}
	return rows
}

func (m *boardModel) selIndex(rows []row) int {
	for i, r := range rows {
		if r.kind == m.sel.kind && r.project == m.sel.project && r.window == m.sel.window && (r.kind != rowUnmanaged || r.target == m.sel.target) {
			return i
		}
	}
	return -1
}

func (m *boardModel) fixSelection() {
	rows := m.rows()
	if len(rows) == 0 {
		m.sel = row{}
		return
	}
	if m.selIndex(rows) < 0 {
		for _, r := range rows {
			if r.kind == rowProject && r.project == m.sel.project {
				m.sel = r
				return
			}
		}
		m.sel = rows[0]
	}
}

func (m *boardModel) move(delta int) {
	rows := m.rows()
	if len(rows) == 0 {
		return
	}
	m.sel = rows[clamp(m.selIndex(rows)+delta, len(rows))]
}

func (m *boardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tickMsg:
		return m, tea.Batch(m.load, tick())
	case snapMsg:
		m.apply(msg)
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		if m.prompt != nil {
			m.promptKey(msg)
			return m, nil
		}
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
			var cmds []tea.Cmd
			for _, r := range msg.Runes {
				_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				cmds = append(cmds, cmd)
				if cmd != nil {
					break
				}
			}
			return m, tea.Batch(cmds...)
		}
		if m.itemsOf != "" {
			return m, m.itemsKey(msg)
		}
		return m, m.projectsKey(msg)
	}
	return m, nil
}

func (m *boardModel) promptKey(msg tea.KeyMsg) {
	p := m.prompt
	f := &p.fields[p.at]
	switch msg.Type {
	case tea.KeyEsc:
		m.prompt = nil
	case tea.KeyBackspace:
		if r := []rune(f.value); len(r) > 0 {
			f.value = string(r[:len(r)-1])
		}
	case tea.KeySpace:
		f.value += " "
	case tea.KeyRunes:
		f.value += string(msg.Runes)
	case tea.KeyEnter:
		if p.at < len(p.fields)-1 {
			p.at++
			return
		}
		values := make([]string, len(p.fields))
		for i, f := range p.fields {
			values[i] = f.value
		}
		m.prompt = nil
		if err := p.submit(values); err != nil {
			m.status = err.Error()
		}
		m.refresh()
	}
}

func (m *boardModel) ask(submit func([]string) error, fields ...promptField) {
	m.status = ""
	m.prompt = &prompt{fields: fields, submit: submit}
}

func (m *boardModel) jump(target string) tea.Cmd {
	if err := m.a.tmux.Jump(target); err != nil {
		m.status = err.Error()
		return nil
	}
	return tea.Quit
}

func (m *boardModel) projectsKey(msg tea.KeyMsg) tea.Cmd {
	m.status = ""
	switch msg.String() {
	case "q", "esc":
		return tea.Quit
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "enter":
		target := m.sel.target
		if m.sel.kind == rowProject {
			if p, ok := m.snap.Project(m.sel.project); ok {
				if w, ok := p.Window(""); ok {
					target = w.TmuxTarget
				}
			}
		}
		if target != "" {
			return m.jump(target)
		}
	case "o":
		if m.sel.kind != rowUnmanaged && m.sel.project != "" {
			m.itemsOf, m.itemSel, m.expanded = m.sel.project, 0, ""
			m.refresh()
		}
	case "t":
		if m.sel.kind != rowUnmanaged && m.sel.project != "" {
			project := m.sel.project
			m.ask(func(v []string) error {
				if err := m.a.spawn(project, v[0], v[1], ""); err != nil {
					return err
				}
				m.sel = row{kind: rowWindow, project: project, window: v[0]}
				return nil
			}, promptField{label: "thread name"}, promptField{label: "harness", value: harness.Claude})
		}
	case "a":
		if m.sel.kind == rowUnmanaged {
			target := m.sel.target
			project := ""
			if len(m.snap.Projects) == 1 {
				project = m.snap.Projects[0].Name
			}
			m.ask(func(v []string) error {
				w, err := m.a.adopt(target, v[0], v[1], "")
				if err != nil {
					return err
				}
				m.sel = row{kind: rowWindow, project: w.Project, window: w.Name}
				return nil
			}, promptField{label: "adopt into project", value: project}, promptField{label: "name", value: adoptName(m.sel.window)})
		}
	}
	return nil
}

func (m *boardModel) currentItem() (store.Item, bool) {
	if m.itemSel < len(m.items) {
		return m.items[m.itemSel], true
	}
	return store.Item{}, false
}

func (m *boardModel) itemSource(it store.Item) (signals.Window, bool) {
	p, ok := m.snap.Project(it.Project)
	if !ok {
		return signals.Window{}, false
	}
	return sourceWindow(p, it)
}

func (m *boardModel) itemsKey(msg tea.KeyMsg) tea.Cmd {
	m.status = ""
	key := msg.String()
	switch key {
	case "q", "esc":
		m.itemsOf, m.items = "", nil
		return nil
	case "up", "k":
		m.itemSel = clamp(m.itemSel-1, len(m.items))
		return nil
	case "down", "j":
		m.itemSel = clamp(m.itemSel+1, len(m.items))
		return nil
	}
	it, ok := m.currentItem()
	if !ok {
		return nil
	}
	switch key {
	case "enter":
		if m.expanded == it.ID {
			m.expanded = ""
		} else {
			m.expanded = it.ID
		}
	case "g", "f":
		w, ok := m.itemSource(it)
		if !ok || !w.Alive {
			m.status = fmt.Sprintf("%s: source window is not alive", it.ID)
			return nil
		}
		if key == "f" {
			if err := m.a.tmux.TypeText(w.TmuxTarget, fmt.Sprintf("re %s: ", it.ID)); err != nil {
				m.status = err.Error()
				return nil
			}
		}
		return m.jump(w.TmuxTarget)
	case "y", "n":
		if it.Kind != store.KindDefault {
			m.status = "y/n answer veto-able defaults only"
			return nil
		}
		answer := map[string]string{"y": "yes", "n": "no"}[key]
		if _, err := m.a.store.Answer(it.Project, it.ID, answer); err != nil {
			m.status = err.Error()
		}
		m.refresh()
	case "s":
		if _, err := m.a.store.Snooze(it.Project, it.ID); err != nil {
			m.status = err.Error()
		}
		m.refresh()
	case "d":
		m.ask(func(v []string) error {
			if strings.TrimSpace(v[0]) == "" {
				return errors.New("dismiss needs a reason")
			}
			_, err := m.a.store.Dismiss(it.Project, it.ID, v[0])
			return err
		}, promptField{label: "dismiss " + it.ID + " because"})
	}
	return nil
}

func boardCmd(withApp runner) *cobra.Command {
	return &cobra.Command{
		Use:   "board",
		Short: "Show every project and window; run it in tmux display-popup -E",
		Args:  cobra.NoArgs,
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			m := newBoard(a)
			m.refresh()
			_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
			return err
		}),
	}
}
