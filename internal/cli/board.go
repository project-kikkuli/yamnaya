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
	rowSession
	rowCrew
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

	// sidebar is true when the board runs as the ym home sidebar and stages windows instead of switching to them.
	sidebar   bool
	height    int
	selLine   int
	kurgan    []store.Project
	inKurgan  bool
	kurganSel int
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
	return p.Open > 0 || p.NeedPermission() > 0 || ((m.sel.kind == rowProject || m.sel.kind == rowWindow) && m.sel.project == p.Name)
}

// rows lists selectable rows in the order the view draws them: the popup puts sessions above projects, the sidebar below.
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
	if m.sidebar {
		return append(rows, m.sessionRows()...)
	}
	return append(m.sessionRows(), rows...)
}

func (m *boardModel) selIndex(rows []row) int {
	for i, r := range rows {
		if r.kind == m.sel.kind && r.project == m.sel.project && r.window == m.sel.window && (r.kind != rowSession && r.kind != rowCrew || r.target == m.sel.target) {
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
			if (r.kind == rowProject && r.project == m.sel.project) || (r.kind == rowSession && r.target == m.sel.target) {
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
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		if m.sidebar {
			if err := m.a.healStage(); err != nil {
				m.status = err.Error()
			}
		}
		return m, tea.Batch(m.load, tick())
	case snapMsg:
		m.apply(msg)
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, m.leave()
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
		if m.inKurgan {
			return m, m.kurganKey(msg)
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
	case tea.KeyCtrlU:
		f.value = ""
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
	if m.sidebar {
		return nil
	}
	return tea.Quit
}

// leave closes the popup; the sidebar stays and detaches the client instead.
func (m *boardModel) leave() tea.Cmd {
	if !m.sidebar {
		return tea.Quit
	}
	if err := m.a.tmux.Detach(); err != nil {
		m.status = err.Error()
	}
	return nil
}

// open brings a window forward: onto the stage in the sidebar, else by switching the client to it.
func (m *boardModel) open(w signals.Window) tea.Cmd {
	if !m.sidebar {
		return m.jump(w.Pane)
	}
	if err := m.a.stage(w.ID, true); err != nil {
		m.status = err.Error()
	}
	m.refresh()
	return nil
}

func (m *boardModel) openByName(project, name string) tea.Cmd {
	m.refresh()
	p, ok := m.snap.Project(project)
	if !ok {
		return nil
	}
	if w, ok := p.Window(name); ok {
		m.sel = row{kind: rowWindow, project: project, window: w.Name}
		return m.open(w)
	}
	return nil
}

func (m *boardModel) focusStage() {
	slot, err := m.a.stageSlot()
	if err == nil {
		err = m.a.tmux.SelectPane(slot)
	}
	if err != nil {
		m.status = err.Error()
	}
}

func (m *boardModel) projectsKey(msg tea.KeyMsg) tea.Cmd {
	m.status = ""
	switch msg.String() {
	case "q", "esc":
		return m.leave()
	case "up":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "tab":
		if m.sidebar {
			m.focusStage()
		}
	case "enter":
		if m.sel.kind == rowSession || m.sel.kind == rowCrew {
			if !m.sidebar {
				return m.jump(m.sel.target)
			}
			ses, _ := m.session(m.sel.target)
			if err := m.a.stagePane(m.sel.target, ses.Harness, ses.ID, true); err != nil {
				m.status = err.Error()
			}
			m.refresh()
			return nil
		}
		if m.sel.project != "" {
			name := m.sel.window
			if m.sel.kind == rowProject {
				name = ""
			}
			return m.openByName(m.sel.project, name)
		}
	case "k":
		projects, err := m.a.store.Projects(store.ProjectLanded)
		if err != nil {
			m.status = err.Error()
			return nil
		}
		m.kurgan, m.kurganSel, m.inKurgan = projects, 0, true
	case "n":
		m.ask(func(v []string) error {
			if err := m.a.newProject(v[0], v[1], v[2], v[3], v[4]); err != nil {
				return err
			}
			m.sel = row{kind: rowProject, project: v[0]}
			if m.sidebar {
				m.openByName(v[0], "")
			}
			return nil
		}, promptField{label: "project"}, promptField{label: "cwd", value: homeCwd()}, promptField{label: "harness", value: harness.Claude},
			promptField{label: "phases"}, promptField{label: "first prompt"})
	case "o":
		if m.sel.project != "" {
			m.itemsOf, m.itemSel, m.expanded = m.sel.project, 0, ""
			m.refresh()
		}
	case "t":
		if m.sel.project != "" {
			project := m.sel.project
			m.ask(func(v []string) error {
				if err := m.a.spawn(project, v[0], v[1], ""); err != nil {
					return err
				}
				m.sel = row{kind: rowWindow, project: project, window: v[0]}
				if m.sidebar {
					m.openByName(project, v[0])
				}
				return nil
			}, promptField{label: "thread name"}, promptField{label: "harness", value: harness.Claude})
		}
	case "a":
		if sess, ok := m.session(m.sel.target); ok && m.sel.project == "" {
			m.ask(func(v []string) error {
				if err := m.a.promote(sess, v[0]); err != nil {
					return err
				}
				m.sel = row{kind: rowProject, project: v[0]}
				return nil
			}, promptField{label: "promote to project", value: promoteName(sess)})
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
	case "up":
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
			if err := m.a.tmux.TypeText(w.Pane, fmt.Sprintf("re %s: ", it.ID)); err != nil {
				m.status = err.Error()
				return nil
			}
		}
		return m.open(w)
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

func (m *boardModel) kurganKey(msg tea.KeyMsg) tea.Cmd {
	m.status = ""
	switch msg.String() {
	case "q", "esc", "k":
		m.inKurgan = false
	case "up":
		m.kurganSel = clamp(m.kurganSel-1, len(m.kurgan))
	case "down", "j":
		m.kurganSel = clamp(m.kurganSel+1, len(m.kurgan))
	case "enter":
		if m.kurganSel >= len(m.kurgan) {
			return nil
		}
		p, _, err := m.a.revive(m.kurgan[m.kurganSel].Name)
		if err != nil {
			m.status = err.Error()
			return nil
		}
		m.inKurgan = false
		return m.openByName(p.Name, "")
	}
	return nil
}

// newSidebar is the board as the ym home sidebar; it first returns any pane a previous sidebar left on stage.
func newSidebar(a *app) *boardModel {
	m := newBoard(a)
	m.sidebar = true
	if err := a.unstage(); err != nil {
		m.status = err.Error()
	}
	if err := a.ensureHome(homeCwd()); err != nil {
		m.status = err.Error()
	}
	return m
}

func boardCmd(withApp runner) *cobra.Command {
	var sidebar bool
	cmd := &cobra.Command{
		Use:   "board",
		Short: "Show every project and window; run it in tmux display-popup -E",
		Args:  cobra.NoArgs,
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			m := newBoard(a)
			if sidebar {
				m = newSidebar(a)
			}
			m.refresh()
			_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
			return err
		}),
	}
	cmd.Flags().BoolVar(&sidebar, "sidebar", false, "run as the ym home sidebar")
	cmd.Flags().MarkHidden("sidebar")
	return cmd
}
