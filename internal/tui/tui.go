// Package tui is ym's full-screen board.
package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

type mode int

const (
	modeList mode = iota
	modeDetail
	modeInput
	modeWhy
	modePick
	modeMerge
	modeHelp
)

type inputKind int

const (
	inAnswer inputKind = iota
	inDismiss
	inRename
	inFilter
)

// row is one selectable line: a permission prompt, an item, a mission, or a session in detail.
type row struct {
	need    *core.Need
	project *core.Project
	session *core.Live
	item    *core.Item
}

type Model struct {
	core     *core.Core
	board    core.Board
	loaded   bool
	err      error
	mode     mode
	back     mode
	cursor   int
	detail   string
	input    []rune
	fresh    bool
	inKind   inputKind
	inItem   string
	inProj   string
	filter   string
	targets  []core.Target
	cursor2  int
	mergeSrc string
	why      *core.Item
	message  string
	width    int
	height   int
	interval time.Duration
}

func New(c *core.Core) Model {
	return Model{core: c, width: 100, height: 40, interval: time.Second}
}

func Run(c *core.Core) error {
	_, err := tea.NewProgram(New(c), tea.WithAltScreen()).Run()
	return err
}

type boardMsg struct {
	board core.Board
	err   error
	tick  bool
}

type tickMsg struct{}

type doneMsg struct {
	message string
	err     error
}

// refresh reloads the board; only the periodic reload schedules the next one, so actions never start a second loop.
func (m Model) refresh(tick bool) tea.Cmd {
	c := m.core
	return func() tea.Msg {
		b, err := c.Board()
		return boardMsg{b, err, tick}
	}
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(m.interval, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) Init() tea.Cmd { return m.refresh(true) }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case boardMsg:
		keep := ""
		if r, ok := m.selected(); ok {
			keep = rowKey(r)
		}
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.board, m.loaded, m.err = msg.board, true, nil
		}
		m.follow(keep)
		if msg.tick {
			return m, m.tick()
		}
		return m, nil
	case tickMsg:
		return m, m.refresh(true)
	case doneMsg:
		if msg.err != nil {
			m.message = "error: " + msg.err.Error()
		} else {
			m.message = msg.message
		}
		return m, m.refresh(false)
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// follow keeps the cursor on the row it was on when that row still exists.
func (m *Model) follow(key string) {
	if key != "" {
		for i, r := range m.rows() {
			if rowKey(r) == key {
				m.cursor = i
				return
			}
		}
	}
	m.clamp()
}

func rowKey(r row) string {
	switch {
	case r.item != nil:
		return "item:" + r.item.ID
	case r.session != nil:
		return "session:" + r.session.Key()
	case r.project != nil:
		return "project:" + r.project.Key
	}
	return ""
}

func (m *Model) clamp() {
	n := len(m.rows())
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m Model) rows() []row {
	var out []row
	switch m.listMode() {
	case modeDetail:
		p, ok := m.board.Project(m.detail)
		if !ok {
			return nil
		}
		for i := range p.Sessions {
			out = append(out, row{session: &p.Sessions[i], project: &p})
		}
		for i := range p.Items {
			out = append(out, row{item: &p.Items[i], project: &p})
		}
	default:
		for i := range m.board.Needs {
			n := &m.board.Needs[i]
			if m.matches(m.projectName(n.Project), needText(*n)) {
				out = append(out, row{need: n, item: n.Item, session: n.Session})
			}
		}
		for i := range m.board.Projects {
			p := &m.board.Projects[i]
			if len(p.Sessions) > 0 && m.matches(p.Name) {
				out = append(out, row{project: p})
			}
		}
	}
	return out
}

// listMode is the list the cursor moves over, looking through overlays.
func (m Model) listMode() mode {
	switch m.mode {
	case modeInput, modeWhy, modePick, modeHelp:
		return m.back
	}
	return m.mode
}

func (m Model) matches(texts ...string) bool {
	if m.filter == "" {
		return true
	}
	f := strings.ToLower(m.filter)
	for _, t := range texts {
		if strings.Contains(strings.ToLower(t), f) {
			return true
		}
	}
	return false
}

func needText(n core.Need) string {
	if n.Item != nil {
		return n.Item.ID + " " + n.Item.Question
	}
	if n.Session != nil && n.Session.Prompt != nil {
		return n.Session.Prompt.String()
	}
	return "permission prompt"
}

func (m Model) projectName(key string) string {
	if p, ok := m.board.Project(key); ok {
		return p.Name
	}
	return core.Short(key)
}

func (m Model) selected() (row, bool) {
	rs := m.rows()
	if m.cursor < 0 || m.cursor >= len(rs) {
		return row{}, false
	}
	return rs[m.cursor], true
}

func (m Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch m.mode {
	case modeInput:
		return m.inputKey(k)
	case modeHelp:
		if k.Type == tea.KeyEsc || k.String() == "q" || k.Type == tea.KeyEnter || k.String() == "?" {
			m.mode = m.back
		}
		return m, nil
	case modeWhy:
		switch k.String() {
		case "a", "l", "x", "u", "o":
			m.mode = m.back
			m.follow("item:" + m.why.ID)
			if r, ok := m.selected(); !ok || r.item == nil || r.item.ID != m.why.ID {
				m.message = m.why.ID + " is no longer on this list"
				return m, nil
			}
		case "esc", "q", "enter", "w":
			m.mode = m.back
			return m, nil
		default:
			return m, nil
		}
	case modePick:
		return m.pickKey(k)
	}
	m.message = ""
	switch k.String() {
	case "q":
		if m.mode == modeList {
			return m, tea.Quit
		}
		m.mode, m.cursor = modeList, 0
		return m, nil
	case "esc":
		if m.mode == modeMerge {
			m.mode, m.message = modeList, "merge cancelled"
		} else if m.mode == modeDetail {
			m.mode, m.cursor = modeList, 0
		} else if m.filter != "" {
			m.filter = ""
		}
		return m, nil
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.cursor < len(m.rows())-1 {
			m.cursor++
		}
		return m, nil
	case "?":
		m.back, m.mode = m.mode, modeHelp
		return m, nil
	case "/":
		if m.mode == modeList {
			m.startInput(inFilter, "", "", m.filter, false)
		}
		return m, nil
	}
	r, ok := m.selected()
	if !ok {
		return m, nil
	}
	if m.mode == modeMerge {
		if k.Type == tea.KeyEnter && r.project != nil && r.need == nil {
			src, dst := m.mergeSrc, r.project.Key
			m.mode = modeList
			return m, m.do(func() (string, error) {
				return "merged into " + r.project.Name, m.core.Store.Merge(src, dst)
			})
		}
		return m, nil
	}
	switch k.String() {
	case "enter":
		if r.item != nil {
			return m.expand(r.item.ID)
		}
		if r.project != nil && r.need == nil && m.mode == modeList {
			m.mode, m.detail, m.cursor = modeDetail, r.project.Key, 0
			return m, nil
		}
		return m.open(r)
	case "o":
		return m.open(r)
	case "a":
		if r.item != nil && r.item.State == core.ItemOpen {
			m.startInput(inAnswer, r.item.ID, "", r.item.Rec, true)
		}
	case "x":
		if r.item != nil && (r.item.State == core.ItemOpen || r.item.State == core.ItemSnoozed) {
			m.startInput(inDismiss, r.item.ID, "", "", false)
		}
	case "l":
		if r.item != nil && r.item.State == core.ItemOpen {
			id := r.item.ID
			return m, m.do(func() (string, error) {
				_, err := m.core.Snooze(id)
				return id + " snoozed", err
			})
		}
	case "u":
		if r.item != nil && r.item.State == core.ItemSnoozed {
			id := r.item.ID
			return m, m.do(func() (string, error) {
				_, err := m.core.Reopen(id)
				return id + " reopened", err
			})
		}
	case "w":
		if r.item != nil {
			return m.expand(r.item.ID)
		}
	case "r":
		if p := m.projectOf(r); p != nil {
			m.startInput(inRename, "", p.Key, p.Name, true)
		}
	case "m":
		if p := m.projectOf(r); p != nil && m.mode == modeList {
			m.mergeSrc, m.mode = p.Key, modeMerge
			m.message = "pick the mission to merge " + p.Name + " into, then enter; esc cancels"
		}
	case "s":
		if p := m.projectOf(r); p != nil {
			key := p.Key
			return m, m.do(func() (string, error) {
				n, err := m.core.Store.Split(key)
				return fmt.Sprintf("split %d merged missions back out", n), err
			})
		}
	}
	return m, nil
}

// expand shows an item in full; its keys act on it there.
func (m Model) expand(id string) (tea.Model, tea.Cmd) {
	it, err := m.core.Store.Item(id)
	if err != nil {
		m.message = "error: " + err.Error()
		return m, nil
	}
	m.why, m.back, m.mode = &it, m.mode, modeWhy
	return m, nil
}

func (m Model) projectOf(r row) *core.Project {
	if r.need != nil {
		return nil
	}
	if r.project != nil && r.session == nil && r.item == nil {
		return r.project
	}
	if m.mode == modeDetail {
		if p, ok := m.board.Project(m.detail); ok {
			return &p
		}
	}
	return nil
}

// startInput opens the input line. A fresh initial text is a default: enter accepts it, typing replaces it.
func (m *Model) startInput(kind inputKind, item, project, initial string, fresh bool) {
	if kind != inFilter {
		m.back = m.mode
	} else {
		m.back = modeList
	}
	m.mode, m.inKind, m.inItem, m.inProj, m.input = modeInput, kind, item, project, []rune(initial)
	m.fresh = fresh && initial != ""
}

func (m Model) inputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		m.mode = m.back
		return m, nil
	case tea.KeyBackspace:
		if m.fresh {
			m.input, m.fresh = nil, false
		}
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
		if m.inKind == inFilter {
			m.filter = string(m.input)
			m.cursor = 0
		}
		return m, nil
	case tea.KeyCtrlU:
		m.input, m.fresh = nil, false
		return m, nil
	case tea.KeyEnter:
		text := strings.TrimSpace(string(m.input))
		m.mode = m.back
		switch m.inKind {
		case inFilter:
			m.filter, m.cursor = text, 0
		case inAnswer:
			if text == "" {
				return m, nil
			}
			id, board := m.inItem, m.board
			return m, m.do(func() (string, error) {
				_, d, err := m.core.Answer(board, id, text)
				return id + " answered, " + d.String(), err
			})
		case inDismiss:
			id, board := m.inItem, m.board
			return m, m.do(func() (string, error) {
				_, d, err := m.core.Dismiss(board, id, text)
				return id + " dismissed, " + d.String(), err
			})
		case inRename:
			key := m.inProj
			return m, m.do(func() (string, error) { return "renamed", m.core.Store.Rename(key, text) })
		}
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		if m.fresh {
			m.input, m.fresh = nil, false
		}
		m.input = append(m.input, k.Runes...)
		if k.Type == tea.KeySpace && len(k.Runes) == 0 {
			m.input = append(m.input, ' ')
		}
		if m.inKind == inFilter {
			m.filter = string(m.input)
			m.cursor = 0
		}
	}
	return m, nil
}

func (m Model) do(f func() (string, error)) tea.Cmd {
	return func() tea.Msg {
		msg, err := f()
		return doneMsg{msg, err}
	}
}

// open focuses the session behind the row; a session in several processes asks which one.
func (m Model) open(r row) (tea.Model, tea.Cmd) {
	key, harness := "", ""
	switch {
	case r.session != nil:
		key, harness = r.session.Key(), r.session.Harness
	case r.item != nil:
		key, harness = r.item.Session, r.item.Harness
	case r.project != nil && len(r.project.Sessions) > 0:
		key, harness = r.project.Sessions[0].Key(), r.project.Sessions[0].Harness
	}
	if key == "" {
		return m, nil
	}
	ts := m.core.Targets(m.board, key, harness)
	switch len(ts) {
	case 0:
		m.message = "no live process and no resume command for " + core.Short(key)
		return m, nil
	case 1:
		return m, m.focus(ts[0])
	}
	m.targets, m.back, m.mode, m.cursor2 = ts, m.mode, modePick, 0
	return m, nil
}

func (m Model) focus(t core.Target) tea.Cmd {
	return m.do(func() (string, error) {
		err := m.core.Focus(t)
		if errors.Is(err, core.ErrNoHost) {
			return t.Advice(), nil
		}
		return "opened " + t.String(), err
	})
}

func (m Model) pickKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q":
		m.mode = m.back
	case "up", "k":
		if m.cursor2 > 0 {
			m.cursor2--
		}
	case "down", "j":
		if m.cursor2 < len(m.targets)-1 {
			m.cursor2++
		}
	case "enter":
		m.mode = m.back
		return m, m.focus(m.targets[m.cursor2])
	}
	return m, nil
}
