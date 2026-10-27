// Package tui is ym's full-screen board.
package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

type mode int

const (
	modeList mode = iota
	modeRepo
	modeDetail
	modeSession
	modeAgent
	modeInput
	modeWhy
	modePick
	modeMerge
	modeHelp
	// modeScreen shows a page of text, such as health or usage, until esc.
	modeScreen
	modeConfirm
	modeItems
	// modeGoal shows one goal: its manager, brief and agents; modeChief one chief and its reports.
	modeGoal
	modeChief
)

type inputKind int

const (
	inAnswer inputKind = iota
	inDismiss
	inRename
	inFilter
	inDismissStale
	inAdopt
	inSend
	inProfile
	inNew
	inNewRepo
	inPhases
	inVibe
	inGoal
	inChief
	inAttach
	inSteer
	// inOver picks a new chief's reports and inRemit asks its remit; inAttach picks the chief to report to.
	inOver
	inRemit
	// inChiefMenu picks what c does: put the row under a chief, detach it, or start a new chief.
	inChiefMenu
)

// row is one selectable line: a permission prompt, an item, a repo, a mission, a session or a crew member.
type row struct {
	// node is a goal's manager or a chief.
	node    *core.Node
	need    *core.Need
	repo    *core.Repo
	project *core.Project
	session *core.Live
	member  *core.Member
	dormant *core.Dormant
	item    *core.Item
	// stale is the collapsed line standing for every item whose mission has no live session.
	stale bool
	// depth is how far below a chief the node row sits in the tree.
	depth int
	// group is the goal row's group on the board: its repository, or the chiefs.
	group string
}

// frame is a drill level to return to on esc.
type frame struct {
	mode                      mode
	cursor                    int
	detail, sess, agent, repo string
	goal, chief               string
	scroll                    int
}

type Model struct {
	peaks mountains
	// goal and chief are the manager ids the goal and chief views show.
	goal, chief string
	// feedFrom is when the open goal view was entered: news read from then on stays marked new while it shows.
	feedFrom time.Time
	// picked are the managers and chiefs space selected; s sends to all of them.
	picked map[string]bool
	// unfolded are the queue rows of sessions whose folded asks are listed.
	unfolded map[string]bool
	// flat lists every goal under its own repository, leaving chiefs out.
	flat bool
	// menu is the c menu's choices for the manager or chief menuFor.
	menu    []menuOpt
	menuFor string
	// forGoal sends the repository picker on to a goal instead of a session.
	forGoal bool
	inPicks []string
	// opts are the C and c pickers' managers and chiefs, optOn the checked ones.
	opts      []pickOpt
	optCur    int
	optOn     map[string]bool
	chiefName string
	steer     []string
	subtree   bool
	openURL   func(string) error
	page      *viewPage
	core      *core.Core
	host      string
	board     core.Board
	loaded    bool
	err       error
	mode      mode
	back      mode
	cursor    int
	stack     []frame
	repo      string
	detail    string
	sess      string
	agent     string
	scrollY   int
	showStale bool
	input     []rune
	fresh     bool
	inKind    inputKind
	inItem    string
	inLabel   string
	inProj    string
	filter    string
	targets   []core.Target
	cursor2   int
	// beside opens the chosen target in a split next to ym instead of switching to it.
	beside    bool
	mergeSrc  string
	why       *core.Item
	screen    string
	screenKey string
	ask       string
	yes       tea.Cmd
	// marks are the keys a yes marks killing: sessions and goal or chief nodes whose teardown runs off the UI thread.
	marks   []string
	killing map[string]bool
	// dyingAt are the rows a running kill tears down, marked x while one view is drawn.
	dyingAt map[int]bool
	newL    core.NewLaunch
	// launchOn is the remembered host the launch input starts on, "" for this one.
	launchOn string
	// repoScan streams repositories found under the roots; scanned holds them for the whole TUI session.
	repoScan <-chan string
	scanned  []string
	scanDone bool
	// picks lists the N picker's repositories: recent first, then scanned.
	picks    []string
	repoCur  int
	pickErr  string
	items    []core.Item
	itemsOf  string
	message  string
	width    int
	height   int
	interval time.Duration
	// self watches ym's own executable; restart is set when the board quits to run the new one.
	self    *binaryWatch
	restart bool
	out     *output
	// slots are the board's project hues while one view is drawn.
	slots map[string]int
	// drawn keeps the last screen drawn, shared by every copy of the model.
	drawn *lastDraw
	// acts are the actions the board dispatched, by the row key they act on; queued are its commands waiting for a host.
	acts   map[string]act
	queued map[string]core.Relay
}

func New(c *core.Core) Model {
	m := Model{killing: map[string]bool{}, acts: map[string]act{}, drawn: &lastDraw{}, core: c, host: core.ThisHost(), width: 100, height: 40, interval: time.Second, peaks: newMountains(), openURL: openInBrowser}
	if path, err := os.Executable(); err == nil {
		m.self, _ = watchBinary(path)
	}
	if os.Getenv(RestartedEnv) != "" {
		os.Unsetenv(RestartedEnv)
		m.message = "restarted into new ym " + revision()
	}
	prepared := m.prepareView()
	m.page = &prepared
	return m
}

// RestartedEnv marks a ym that replaced itself with a newer executable, so the board can say so.
const RestartedEnv = "YAMNAYA_RESTARTED"

// ErrRestart is Run's result when ym's executable changed on disk and the board quit so the new one can run.
var ErrRestart = errors.New("ym executable changed")

func Run(c *core.Core) error {
	m := New(c)
	m.out = newOutput(os.Stdout)
	final, err := tea.NewProgram(m, tea.WithInput(input{os.Stdin}), tea.WithOutput(m.out), tea.WithColorProfile(scene.ColorProfile())).Run()
	m.out.drain()
	if m, ok := final.(Model); ok && err == nil && m.restart {
		return ErrRestart
	}
	return err
}

type boardMsg struct {
	board  core.Board
	relays []core.Relay
	err    error
	tick   bool
}

type tickMsg struct{}

type screenMsg struct {
	text string
	err  error
}

// reposMsg carries repositories the scan found since the last one; done marks the scan's end.
type reposMsg struct {
	repos []string
	done  bool
}

type doneMsg struct {
	message string
	err     error
}

type adoptedMsg struct {
	target core.Target
	err    error
}

// refresh reloads the board; only the periodic reload schedules the next one, so actions never start a second loop.
func (m Model) refresh(tick bool) tea.Cmd {
	c := m.core
	return func() tea.Msg {
		if c.KeepAlive != nil {
			if err := c.KeepAlive(); err != nil {
				return boardMsg{err: err, tick: tick}
			}
		}
		b, err := c.Board()
		var relays []core.Relay
		if err == nil && c.Store != nil {
			relays, err = c.Store.PendingRelays()
		}
		return boardMsg{b, relays, err, tick}
	}
}

// blurredInterval is how often the board reloads while its terminal is not focused.
const blurredInterval = 10 * time.Second

func (m Model) tick() tea.Cmd {
	interval := m.interval
	if m.peaks.blurred {
		interval = max(interval, blurredInterval)
	}
	return tea.Tick(interval, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) Init() tea.Cmd { return m.refresh(true) }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch b := msg.(type) {
	case mountainFrame:
		if wait := time.Until(m.peaks.quiet); wait > 0 {
			return m, tea.Tick(wait, func(time.Time) tea.Msg { return mountainFrame{} })
		}
		if m.out.behind() {
			return m, tea.Tick(frameTime, func(time.Time) tea.Msg { return mountainFrame{} })
		}
		m.peaks.ticking = false
		m.peaks.frame = int(time.Since(m.peaks.start) / scene.Step)
		m.settle(true)
		return m, m.animate()
	case tea.BlurMsg:
		m.peaks.blurred = true
		return m, nil
	case tea.FocusMsg:
		m.peaks.blurred = false
		return m, tea.Batch(m.refresh(false), m.animate())
	case boardMsg:
		if b.err == nil && m.loaded && m.err == nil && m.page != nil && !m.noting() && len(queued(b.relays)) == 0 && b.board.Gen != 0 && b.board.Gen == m.board.Gen && m.listMode() != modeItems && b.board.Machine.Slots.Pressure.Line() == m.board.Machine.Slots.Pressure.Line() {
			m.board = b.board
			if b.tick {
				return m, m.tick()
			}
			return m, nil
		}
	}
	switch msg.(type) {
	case tea.KeyPressMsg, tea.PasteMsg:
		m.peaks.quiet = time.Now().Add(keyQuiet)
	}
	m.page = nil
	next, cmd := m.update(msg)
	if model, ok := next.(Model); ok {
		prepared := model.prepareView()
		model.page = &prepared
		model.settle(false)
		return model, tea.Batch(cmd, model.animate())
	}
	return next, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case reposMsg:
		m.scanned = append(m.scanned, msg.repos...)
		m.scanDone = msg.done
		if m.mode == modeInput && m.inKind == inNewRepo {
			m.picks = m.orderPicks()
		}
		if msg.done {
			return m, nil
		}
		return m, m.waitRepos()
	case boardMsg:
		keep, rows := "", m.rows()
		var after, before []string
		if m.cursor >= 0 && m.cursor < len(rows) {
			keep = rowKey(rows[m.cursor])
			for _, r := range rows[m.cursor+1:] {
				after = append(after, rowKey(r))
			}
			for i := m.cursor - 1; i >= 0; i-- {
				before = append(before, rowKey(rows[i]))
			}
		}
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.board, m.loaded, m.err = msg.board, true, nil
			m.queued = queued(msg.relays)
		}
		if m.listMode() == modeItems {
			m.loadItems()
		}
		m.follow(keep, after, before)
		var cmds []tea.Cmd
		if msg.tick {
			cmds = append(cmds, m.tick())
		}
		return m, tea.Batch(cmds...)
	case tickMsg:
		if m.self != nil && m.mode == m.listMode() && m.self.changed() {
			m.restart = true
			return m, tea.Quit
		}
		return m, m.refresh(true)
	case weighedMsg:
		if msg.err != nil {
			m.message = "error: " + msg.err.Error()
			return m, nil
		}
		run, keys := msg.run, msg.keys
		m.confirm(msg.ask, func() tea.Msg {
			said, err := run()
			return killedMsg{keys, said, err}
		})
		m.marks = keys
		return m, nil
	case killedMsg:
		for _, k := range msg.keys {
			delete(m.killing, k)
			m.acts[k] = act{what: "kill", at: time.Now(), done: true, message: msg.message, err: msg.err}
		}
		m.message = msg.message
		if msg.err != nil {
			m.message = "error: " + msg.err.Error()
		}
		return m, m.refresh(false)
	case actMsg:
		return m.ended(msg)
	case reachedMsg:
		delete(m.acts, msg.key)
		t := msg.t
		return m, tea.Exec(attached{msg.open, m.out}, func(err error) tea.Msg { return doneMsg{"back from " + t.String(), err} })
	case doneMsg:
		if msg.err != nil {
			m.message = "error: " + msg.err.Error()
		} else {
			m.message = msg.message
		}
		return m, m.refresh(false)
	case revivedMsg:
		m.message = msg.message
		if msg.open == nil {
			return m, m.refresh(false)
		}
		key := msg.key
		return m, tea.Exec(attached{msg.open, m.out}, func(err error) tea.Msg { return doneMsg{"back from " + key, err} })
	case adoptedMsg:
		if msg.err != nil {
			m.message = "error: " + msg.err.Error()
			return m, m.refresh(false)
		}
		return m, m.focus(msg.target)
	case screenMsg:
		m.screen = msg.text
		if msg.err != nil {
			m.screen = "error: " + msg.err.Error()
		}
		return m, nil
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			return m.key(tea.KeyPressMsg{Code: tea.KeyUp})
		case tea.MouseWheelDown:
			return m.key(tea.KeyPressMsg{Code: tea.KeyDown})
		}
		return m, nil
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft && m.shown() && m.mode == m.listMode() {
			m.pick(msg.X, msg.Y)
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.key(msg)
	case tea.PasteMsg:
		if m.mode == modeInput {
			return m.typed(msg.Content), nil
		}
		return m, nil
	}
	return m, nil
}

// follow keeps the cursor on the row it was on when that row still exists. Once it is gone the cursor stays put on a new row
// that took its place, else moves to the nearest row that followed it, one of its kind first, else the nearest that preceded it.
func (m *Model) follow(key string, after, before []string) {
	rows := m.rows()
	at := map[string]int{}
	for i, r := range rows {
		at[rowKey(r)] = i
	}
	if i, ok := at[key]; ok && key != "" {
		m.cursor = i
		return
	}
	old := map[string]bool{key: true}
	for _, k := range append(after, before...) {
		old[k] = true
	}
	if m.cursor >= 0 && m.cursor < len(rows) && !old[rowKey(rows[m.cursor])] {
		return
	}
	kind, _, _ := strings.Cut(key, ":")
	for _, near := range [][]string{after, before} {
		for _, k := range near {
			if i, ok := at[k]; ok && strings.HasPrefix(k, kind+":") {
				m.cursor = i
				return
			}
		}
	}
	for _, near := range [][]string{after, before} {
		for _, k := range near {
			if i, ok := at[k]; ok && k != "" {
				m.cursor = i
				return
			}
		}
	}
	m.clamp()
}

func rowKey(r row) string {
	switch {
	case r.node != nil && r.session != nil:
		return "node:" + r.node.Manager.ID + "/" + r.session.Key()
	case r.node != nil:
		return "node:" + r.node.Manager.ID
	case r.item != nil:
		return "item:" + r.item.Key()
	case r.member != nil:
		return "member:" + r.session.Key() + "/" + r.member.ID
	case r.dormant != nil:
		return "dormant:" + r.dormant.Key
	case r.stale:
		return "stale"
	case r.session != nil:
		return "session:" + r.session.Key()
	case r.project != nil:
		return "project:" + r.project.Key
	case r.repo != nil:
		return "repo:" + r.repo.Key
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
	case modeAgent:
		return nil
	case modeGoal:
		return m.goalRows()
	case modeChief:
		return m.chiefRows()
	case modeItems:
		for i := range m.items {
			out = append(out, row{item: &m.items[i]})
		}
	case modeSession:
		p, _ := m.board.Project(m.detail)
		s, ok := m.session()
		if !ok {
			return nil
		}
		for i := range s.Crew {
			out = append(out, row{member: &s.Crew[i], session: s, project: &p})
		}
		for i := range p.Items {
			if p.Items[i].Session == s.Key() {
				out = append(out, row{item: &p.Items[i], project: &p})
			}
		}
	case modeDetail:
		p, ok := m.board.Project(m.detail)
		if !ok {
			return nil
		}
		for i := range p.Sessions {
			s := &p.Sessions[i]
			out = append(out, row{session: s, project: &p})
			for j := range s.Crew {
				out = append(out, row{member: &s.Crew[j], session: s, project: &p})
			}
		}
		for i := range p.Dormant {
			out = append(out, row{dormant: &p.Dormant[i], project: &p})
		}
		for i := range p.Items {
			out = append(out, row{item: &p.Items[i], project: &p})
		}
	case modeRepo:
		r, ok := m.board.Repo(m.repo)
		if !ok {
			return nil
		}
		for i := range r.Missions {
			out = append(out, row{project: &r.Missions[i], repo: &r})
		}
	default:
		out = append(out, m.queueRows()...)
		if len(m.board.Stale) > 0 {
			out = append(out, row{stale: true})
			for i := range m.board.Stale {
				n := &m.board.Stale[i]
				if m.showStale && m.matches(m.projectName(n.Project), needText(*n)) {
					out = append(out, row{need: n, item: n.Item})
				}
			}
		}
		out = append(out, m.treeRows()...)
		for i := range m.board.Repos {
			r := &m.board.Repos[i]
			all := m.matches(r.Name)
			var missions []row
			for j := range r.Missions {
				if r.Missions[j].Manager != "" {
					continue
				}
				if all || m.matches(r.Missions[j].Name) {
					missions = append(missions, row{project: &r.Missions[j], repo: r})
				}
			}
			if len(missions) > 0 {
				out = append(out, row{repo: r})
				out = append(out, missions...)
			}
		}
	}
	return out
}

// session is the session the session or agent view shows.
func (m Model) session() (*core.Live, bool) {
	if p, ok := m.board.Project(m.detail); ok {
		for i := range p.Sessions {
			if p.Sessions[i].Key() == m.sess {
				return &p.Sessions[i], true
			}
		}
	}
	if s, ok := m.board.Session(m.sess); ok {
		return &s, true
	}
	return nil, false
}

// member is the crew member the agent view shows.
func (m Model) member() (*core.Live, *core.Member, bool) {
	s, ok := m.session()
	if !ok {
		return nil, nil, false
	}
	for i := range s.Crew {
		if s.Crew[i].ID == m.agent {
			return s, &s.Crew[i], true
		}
	}
	return s, nil, false
}

// listMode is the list the cursor moves over, looking through overlays.
func (m Model) listMode() mode {
	switch m.mode {
	case modeInput, modeWhy, modePick, modeHelp, modeMerge, modeConfirm:
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
	switch {
	case n.Goal != nil:
		return n.Goal.ID + " " + n.Goal.State + " " + n.Goal.Ask
	case n.PR != nil:
		return "merge " + n.PR.URL
	case n.Review != nil:
		return "review " + n.Review.URL
	case n.Doc != nil:
		text := "doc " + n.Doc.Title
		for _, d := range n.Docs {
			text += " " + d.Asker + " " + d.Text
		}
		return text
	case n.Steer != nil:
		return "steer " + n.Manager + " " + n.Steer.Text
	case n.Relay != nil:
		return "refused " + n.Relay.Dest + " " + relayWhat(*n.Relay) + " " + n.Relay.Error
	}
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

func (m Model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.mode {
	case modeInput:
		return m.inputKey(k)
	case modeHelp:
		last := max(0, strings.Count(ansi.Wrap(helpText(m.width), m.width, " "), "\n")-m.height+4)
		switch k.String() {
		case "up", "k":
			m.scrollY = max(0, m.scrollY-1)
			return m, nil
		case "down", "j":
			m.scrollY = min(last, m.scrollY+1)
			return m, nil
		case "pgdown", "space", "f":
			m.scrollY = min(last, m.scrollY+m.height/2)
			return m, nil
		case "pgup", "b":
			m.scrollY = max(0, m.scrollY-m.height/2)
			return m, nil
		}
		if k.Code == tea.KeyEscape || k.String() == "q" || k.Code == tea.KeyEnter || k.String() == "?" {
			m.mode = m.back
		}
		return m, nil
	case modeScreen:
		switch k.String() {
		case "up", "k":
			m.scrollY = max(0, m.scrollY-1)
			return m, nil
		case "down", "j":
			m.scrollY++
			return m, nil
		case "pgdown", "space":
			m.scrollY += m.height / 2
			return m, nil
		case "pgup":
			m.scrollY = max(0, m.scrollY-m.height/2)
			return m, nil
		}
		if k.Code == tea.KeyEscape || k.String() == "q" || k.Code == tea.KeyEnter || k.String() == m.screenKey {
			m.mode = m.back
		}
		return m, nil
	case modeConfirm:
		switch k.String() {
		case "y":
			m.mode, m.message = m.back, ""
			for _, k := range m.marks {
				m.killing[k] = true
				m.acts[k] = act{doing: "killing", what: "kill", at: time.Now()}
			}
			if len(m.marks) > 0 {
				m.message = "killing; teardown runs in the background"
			}
			m.marks = nil
			return m, m.yes
		case "n", "esc", "q":
			m.mode, m.message, m.marks = m.back, "cancelled", nil
		}
		return m, nil
	case modeWhy:
		switch k.String() {
		case "r", "g", "l", "x", "u", "o", "O":
			m.mode = m.back
			m.follow("item:"+m.why.Key(), nil, nil)
			if r, ok := m.selected(); !ok || r.item == nil || r.item.Key() != m.why.Key() {
				m.message = m.why.ID + " is no longer on this list"
				return m, nil
			}
		case "esc", "q", "enter":
			m.mode = m.back
			return m, nil
		default:
			return m, nil
		}
	case modePick:
		return m.pickKey(k)
	}
	if m.mode == modeMerge && !slices.Contains([]string{"q", "esc", "up", "k", "down", "j", "enter"}, k.String()) {
		return m, nil
	}
	m.message = ""
	if next, cmd, handled := m.goalKey(k); handled {
		return next, cmd
	}
	switch k.String() {
	case "A":
		m.peaks.hidden = !m.peaks.hidden
		return m, nil
	case "q":
		if m.mode == modeList {
			return m, tea.Quit
		}
		if m.mode == modeMerge {
			m.mode = m.back
		}
		for len(m.stack) > 0 {
			m.pop()
		}
		return m, nil
	case "esc":
		switch {
		case m.mode == modeMerge:
			m.mode, m.message = m.back, "merge cancelled"
		case len(m.stack) > 0:
			m.pop()
		case m.filter != "":
			m.filter = ""
		}
		return m, nil
	case "up", "k":
		if m.mode == modeAgent {
			m.scrollY = max(0, m.scrollY-1)
		} else if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.mode == modeAgent {
			m.scrollBy(1)
		} else if m.cursor < len(m.rows())-1 {
			m.cursor++
		}
		return m, nil
	case "tab", "shift+tab", "[", "]", "<", ">", "home", "end":
		m.move(k.String())
		return m, nil
	case "pgup", "b":
		if m.mode == modeAgent {
			m.scrollY = max(0, m.scrollY-m.height/2)
		}
		return m, nil
	case "pgdown", "space", "f":
		if m.mode == modeAgent {
			m.scrollBy(m.height / 2)
		}
		return m, nil
	case "?":
		m.back, m.mode, m.scrollY = m.mode, modeHelp, 0
		return m, nil
	case "h":
		if m.core.Health == nil {
			return m, nil
		}
		adrift := adriftText(m.board)
		if lines := machineLines(m.board.Machine, m.board.At, m.width); lines != "" {
			adrift = "machine" + lines + "\n\n" + adrift
		}
		m.show("h", adrift+"reading transcripts…")
		load := m.core.Health
		return m, func() tea.Msg {
			text, err := load()
			return screenMsg{adrift + text, err}
		}
	case "U":
		m.show("U", m.usageText())
		return m, nil
	case "E":
		m.show("E", m.launchedText())
		return m, nil
	case "I":
		m.itemsOf = ""
		if m.mode == modeDetail || m.mode == modeSession || m.mode == modeAgent {
			m.itemsOf = m.detail
		}
		m.push(modeItems)
		m.loadItems()
		return m, nil
	case "/":
		if m.mode == modeList {
			m.startInput(inFilter, "", "", m.filter, false)
		}
		return m, nil
	}
	r, ok := m.selected()
	if !ok {
		if (m.mode == modeAgent || m.mode == modeSession) && (k.String() == "o" || k.String() == "O") {
			m.beside = k.String() == "O"
			return m.open(row{})
		}
		if strings.Contains("sKTL", k.String()) && (m.mode == modeAgent || m.mode == modeSession) {
			r = row{}
		} else {
			return m, nil
		}
	}
	if m.mode == modeMerge {
		if k.Code == tea.KeyEnter && r.project != nil && r.need == nil && r.session == nil && r.member == nil {
			src, dst := m.mergeSrc, r.project.Key
			m.mode = m.back
			return m, m.do(func() (string, error) {
				return "merged into " + r.project.Name, m.core.Store.Merge(src, dst)
			})
		}
		return m, nil
	}
	if r.stale {
		switch k.String() {
		case "enter":
			m.showStale = !m.showStale
		case "x":
			m.startInput(inDismissStale, "", "", "stale: its session ended", true)
		}
		return m, nil
	}
	switch k.String() {
	case "enter":
		switch {
		case r.item != nil:
			return m.expand(r.item.Key())
		case r.need != nil:
			m.beside = false
			return m.open(r)
		case r.dormant != nil:
			return m.revive(*r.dormant)
		case r.member != nil:
			m.push(modeAgent)
			m.sess, m.agent, m.scrollY = r.session.Key(), r.member.ID, 0
			if r.project != nil {
				m.detail = r.project.Key
			}
		case r.session != nil:
			m.push(modeSession)
			m.sess = r.session.Key()
		case r.project != nil:
			m.push(modeDetail)
			m.detail = r.project.Key
		case r.repo != nil:
			m.push(modeRepo)
			m.repo = r.repo.Key
		}
		return m, nil
	case "o", "O":
		if r.dormant != nil {
			return m.revive(*r.dormant)
		}
		m.beside = k.String() == "O"
		return m.open(r)
	case "r":
		return m.reply(r)
	case "g":
		if r.item != nil && r.item.State == core.ItemOpen && r.item.Kind == core.KindGo {
			key, board := r.item.Key(), m.board
			return m, m.do(func() (string, error) {
				it, d, err := m.core.Go(board, key)
				return it.ID + " answered go ahead, " + d.String(), err
			})
		}
	case "x":
		if r.item != nil && (r.item.State == core.ItemOpen || r.item.State == core.ItemSnoozed) {
			m.startInput(inDismiss, r.item.Key(), "", "", false)
			m.inLabel = r.item.ID
		}
	case "l":
		if r.item != nil && r.item.State == core.ItemOpen {
			key, id := r.item.Key(), r.item.ID
			return m, m.do(func() (string, error) {
				_, err := m.core.Snooze(key)
				return id + " snoozed", err
			})
		}
	case "u":
		if r.item != nil && r.item.State == core.ItemSnoozed {
			key, id := r.item.Key(), r.item.ID
			return m, m.do(func() (string, error) {
				_, err := m.core.Reopen(key)
				return id + " reopened", err
			})
		}
	case "e":
		if p := m.projectOf(r); p != nil {
			m.startInput(inRename, "", p.Key, p.Name, true)
		}
	case "m":
		if p := m.projectOf(r); p != nil && (m.mode == modeList || m.mode == modeRepo) {
			m.mergeSrc, m.back, m.mode = p.Key, m.mode, modeMerge
			m.message = "pick the mission to merge " + p.Name + " into, then enter; esc cancels"
		}
	case "s":
		if s := m.sessionOf(r); s != nil && s.ID != "" && m.core.Launches != nil {
			m.startInput(inSend, s.Key(), "", "", false)
			m.inLabel = titleOf(*s)
		}
	case "S":
		if p := m.projectOf(r); p != nil {
			key, name := p.Key, p.Name
			return m, m.do(func() (string, error) {
				n, err := m.core.Store.Split(key)
				if err == nil && n == 0 {
					err = fmt.Errorf("%s has no merges", name)
				}
				return fmt.Sprintf("split %d merged missions back out", n), err
			})
		}
	case "K":
		if ss, label := m.killTargets(r); len(ss) > 0 && !m.dying(ss) {
			m.message = "counting what killing " + label + " ends"
			return m, m.weighKill(ss, label)
		}
	case "G":
		if m.core.Launches != nil {
			l := m.core.Launches
			m.confirm("gc? removes worktrees idle 3 days that are clean, on a remote and unused, then branches merged into origin",
				m.do(func() (string, error) {
					msg, err := l.GC()
					return msg[strings.LastIndex(msg, "\n")+1:], err
				}))
		}
	case "B":
		if switcher, ok := m.core.Launches.(interface {
			SwitchProfile(string, string) (string, error)
		}); ok && switcher != nil {
			if s := m.sessionOf(r); s != nil {
				ref := s.Ref()
				if s.Remote != "" {
					ref = core.RemoteKey(s.Remote, ref)
				}
				m.startInput(inProfile, ref, "", "", false)
				m.inLabel = s.Label()
			} else if r.dormant != nil {
				m.startInput(inProfile, r.dormant.Key, "", "", false)
				m.inLabel = r.dormant.Title
			}
		}
	case "z":
		return m.toggleOff(r)
	case "T":
		if s := m.sessionOf(r); s != nil {
			live, to := *s, core.Other(s.Harness)
			m.confirm("end "+live.Label()+" and relaunch it in "+to+"?", m.do(func() (string, error) { return m.core.Relaunch(live, to) }))
		}
	case "p":
		if p := m.projectOf(r); p != nil {
			key, ph := p.Key, p.Phases
			if ph.Current+1 >= len(ph.Names) {
				m.message = p.Name + " has no next phase; P sets its phases"
				return m, nil
			}
			m.confirm("advance "+p.Name+" to "+ph.Names[ph.Current+1]+"?", m.do(func() (string, error) {
				ph, err := m.core.Store.NextPhase(key)
				return phaseText(ph), err
			}))
		}
	case "P":
		if p := m.projectOf(r); p != nil {
			m.startInput(inPhases, "", p.Key, strings.Join(p.Phases.Names, ", "), false)
			m.inLabel = p.Name
		}
	case "v":
		if p := m.projectOf(r); p != nil {
			m.startInput(inVibe, "", p.Key, "", false)
			m.inLabel = p.Name
		}
	}
	return m, nil
}

// toggleOff turns ym off, or back on, for the row's goal or chief, else its session, or a mission's root session.
func (m Model) toggleOff(r row) (tea.Model, tea.Cmd) {
	var t core.OffTarget
	var off bool
	switch s := m.sessionOf(r); {
	case r.node != nil && r.node.Manager.Host == "":
		mg := r.node.Manager
		t, off = core.OffTarget{Manager: &mg}, !r.node.Off
	case s != nil && s.Remote == "":
		t, off = core.OffTarget{Session: s.Key()}, !s.Off
	case r.project != nil && len(r.project.Sessions) > 0 && r.project.Sessions[0].Remote == "":
		t, off = core.OffTarget{Session: r.project.Sessions[0].Key()}, !r.project.Sessions[0].Off
	default:
		return m, nil
	}
	board := m.board
	return m, m.do(func() (string, error) { return m.core.SetOff(board, t, off) })
}

// confirm asks ask on the footer; y runs yes, n or esc cancels.
func (m *Model) confirm(ask string, yes tea.Cmd) {
	m.ask, m.yes, m.back, m.mode, m.marks = ask, yes, m.mode, modeConfirm, nil
}

func (m *Model) show(key, text string) {
	m.back, m.mode, m.screenKey, m.screen = m.mode, modeScreen, key, text
	m.scrollY = 0
}

func (m *Model) loadItems() {
	items, err := m.core.ItemsIn(m.board, m.itemsOf, true)
	if err != nil {
		m.message = "error: " + err.Error()
	}
	m.items = items
	m.clamp()
}

// killable is the live session K ends: the selected session or permission prompt, the session an agent runs in, or a mission's only session.
func (m Model) killable(r row) *core.Live {
	switch {
	case r.need != nil && r.item == nil:
		return r.session
	case r.need != nil || r.dormant != nil || r.stale:
		return nil
	case r.session != nil:
		return r.session
	case r.project != nil && r.item == nil:
		if len(r.project.Sessions) == 1 {
			return &r.project.Sessions[0]
		}
		return nil
	case m.mode == modeSession || m.mode == modeAgent:
		if s, ok := m.session(); ok {
			return s
		}
	}
	return nil
}

// repoOf is the directory a new session started from row r branches from: the row's repository, else ym's own directory.
func (m Model) repoOf(r row) string {
	key := ""
	switch {
	case r.node != nil:
		key = r.node.Manager.Repo
	case r.need != nil && r.need.Goal != nil:
		key = r.need.Goal.Repo
	case r.dormant != nil:
		key = r.dormant.RepoKey
	case r.session != nil:
		key = r.session.RepoKey
	case r.project != nil:
		key = r.project.RepoDir
	case r.repo != nil:
		key = r.repo.Dir
	}
	if key == "" && m.mode == modeRepo {
		if rp, ok := m.board.Repo(m.repo); ok {
			key = rp.Dir
		}
	}
	if key == "" {
		if p, ok := m.board.Project(m.detail); ok && m.mode != modeList {
			key = p.RepoDir
		}
	}
	if key != "" {
		return strings.TrimSuffix(key, string(filepath.Separator)+".git")
	}
	dir, _ := os.Getwd()
	return dir
}

func phaseText(p core.Phases) string {
	if len(p.Names) == 0 {
		return "phases cleared"
	}
	parts := make([]string, len(p.Names))
	for i, n := range p.Names {
		parts[i] = n
		if i == p.Current {
			parts[i] = "> " + n
		}
	}
	return "phases: " + strings.Join(parts, ", ")
}

// scrollBy moves the agent view by n lines, stopping where its last line reaches the bottom.
func (m *Model) scrollBy(n int) {
	total := strings.Count(m.agentView().String(), "\n")
	m.scrollY = max(0, min(m.scrollY+n, total-m.height/2))
}

// revive resumes a launched session whose agent exited.
// revive resumes an exited launched session, here or on its host, and goes to it.
func (m Model) revive(d core.Dormant) (tea.Model, tea.Cmd) {
	if m.core.Launches == nil {
		m.message = "no launcher to revive " + d.Key
		return m, nil
	}
	l, c := m.core.Launches, m.core
	if host, _ := m.splitHost(d.Key); host != "" {
		if err := m.silent(host); err != nil {
			m.refuse(d.Key, "revive", err)
			return m, nil
		}
	}
	m.message = "reviving " + d.Key + "…"
	return m, func() tea.Msg {
		out, err := l.Revive(d.Key)
		if err != nil {
			return doneMsg{"", err}
		}
		return revivedMsg{key: d.Key, message: out, open: openLaunch(c, d.Key)}
	}
}

// revivedMsg is a revived session to go to.
type revivedMsg struct {
	key, message string
	open         *exec.Cmd
}

// openLaunch is the command that shows the launched session key in this terminal: ym open on its host, or here.
func openLaunch(c *core.Core, key string) *exec.Cmd {
	if host, rest, ok := c.RemoteRef(key); ok && c.Remotes != nil {
		return c.Remotes.Attach(host, "open", "--no-board", rest)
	}
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	cmd := exec.Command(self, "open", key)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd
}

// lastExited is the mission's most recently started exited session, for o on a mission nothing runs in.
func lastExited(p *core.Project) (core.Dormant, bool) {
	if p == nil || len(p.Dormant) == 0 {
		return core.Dormant{}, false
	}
	last := p.Dormant[0]
	for _, d := range p.Dormant[1:] {
		if d.Started.After(last.Started) {
			last = d
		}
	}
	return last, true
}

// push enters a deeper level, remembering where esc returns to.
func (m *Model) push(to mode) {
	m.stack = append(m.stack, frame{mode: m.mode, cursor: m.cursor, detail: m.detail, sess: m.sess, agent: m.agent, repo: m.repo, goal: m.goal, chief: m.chief, scroll: m.scrollY})
	m.mode, m.cursor = to, 0
}

func (m *Model) pop() {
	f := m.stack[len(m.stack)-1]
	m.stack = m.stack[:len(m.stack)-1]
	m.mode, m.cursor = f.mode, f.cursor
	m.detail, m.sess, m.agent, m.repo, m.goal, m.chief = f.detail, f.sess, f.agent, f.repo, f.goal, f.chief
	m.scrollY = f.scroll
	m.clamp()
}

// expand shows an item in full; its keys act on it there.
func (m Model) expand(id string) (tea.Model, tea.Cmd) {
	if _, _, remote := m.core.RemoteRef(id); remote {
		for _, it := range m.remoteItems() {
			if it.Key() == id {
				m.why, m.back, m.mode = &it, m.mode, modeWhy
				return m, nil
			}
		}
	}
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
	if r.project != nil && r.session == nil && r.item == nil && r.member == nil && r.dormant == nil {
		return r.project
	}
	if m.mode == modeDetail || m.mode == modeSession {
		if p, ok := m.board.Project(m.detail); ok {
			return &p
		}
	}
	return nil
}

// startInput opens the input line. A fresh initial text is a default: enter accepts it, typing replaces it.
func (m *Model) startInput(kind inputKind, item, project, initial string, fresh bool) {
	m.back = m.mode
	m.mode, m.inKind, m.inItem, m.inProj, m.input = modeInput, kind, item, project, []rune(initial)
	m.fresh = fresh && initial != ""
}

func (m Model) inputKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.picking() {
		return m.optKey(k)
	}
	switch k.String() {
	case "esc":
		m.mode = m.back
		return m, nil
	case "backspace":
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
		m.repoCur, m.pickErr = 0, ""
		return m, nil
	case "ctrl+u":
		m.input, m.fresh, m.repoCur, m.pickErr = nil, false, 0, ""
		return m, nil
	case "up", "down":
		if m.inKind == inNewRepo {
			if k.Code == tea.KeyUp {
				m.repoCur = max(0, m.repoCur-1)
			} else {
				m.repoCur = max(0, min(len(m.repoMatches())-1, m.repoCur+1))
			}
		}
		return m, nil
	case "ctrl+t":
		if m.launching() {
			m.nextTarget()
		}
		return m, nil
	case "shift+tab":
		if m.launching() {
			m.nextLaunchProfile()
		}
		return m, nil
	case "tab":
		if m.launching() {
			m.newL.Harness = core.Other(m.newL.Harness)
			m.newL.Profile = ""
		}
		if m.inKind == inSteer && m.steerHasChief() {
			m.subtree = !m.subtree
		}
		return m, nil
	case "enter":
		text := strings.TrimSpace(string(m.input))
		m.mode = m.back
		switch m.inKind {
		case inAdopt:
			return m.adopt(m.inItem)
		case inGoal, inChief, inRemit, inSteer:
			return m.goalInput(text)
		case inProfile:
			if text == "" {
				return m, nil
			}
			ref := m.inItem
			switcher, ok := m.core.Launches.(interface {
				SwitchProfile(string, string) (string, error)
			})
			if !ok {
				return m, nil
			}
			return m, m.do(func() (string, error) { return switcher.SwitchProfile(ref, text) })
		case inSend:
			if text == "" {
				return m, nil
			}
			s, ok := m.board.Session(m.inItem)
			if !ok {
				m.message = m.inLabel + " is gone"
				return m, nil
			}
			l := m.core.Launches
			return m, m.act(sessionKey(s), "sending", "send", func() (string, error) { return l.Send(s, text) })
		case inFilter:
			m.filter, m.cursor = text, 0
		case inAnswer:
			if text == "" {
				return m, nil
			}
			id, board := m.inItem, m.board
			return m, m.do(func() (string, error) {
				it, d, err := m.core.Answer(board, id, text)
				return it.ID + " answered, " + d.String(), err
			})
		case inDismiss:
			id, board := m.inItem, m.board
			return m, m.do(func() (string, error) {
				it, d, err := m.core.Dismiss(board, id, text)
				return it.ID + " dismissed, " + d.String(), err
			})
		case inDismissStale:
			if text == "" {
				return m, nil
			}
			var ids []string
			for _, n := range m.board.Stale {
				ids = append(ids, n.Item.Key())
			}
			board := m.board
			return m, m.do(func() (string, error) {
				for _, id := range ids {
					if _, _, err := m.core.Dismiss(board, id, text); err != nil {
						return "", err
					}
				}
				return fmt.Sprintf("dismissed %d stale items", len(ids)), nil
			})
		case inRename:
			key := m.inProj
			return m, m.do(func() (string, error) { return "renamed", m.core.Store.Rename(key, text) })
		case inNew:
			if text == "" {
				return m, nil
			}
			m.newL.Prompt = text
			if m.launchOn != "" {
				return m.launchRemote(text)
			}
			return m.startNew()
		case inNewRepo:
			repo, err := m.pickedRepo(text)
			if err != nil {
				m.mode, m.pickErr = modeInput, err.Error()
				return m, nil
			}
			m.newL.Repo = repo
			if m.forGoal {
				m.startInput(inGoal, "", "", "", false)
			} else {
				m.startInput(inNew, "", "", "", false)
			}
		case inPhases:
			var names []string
			for _, n := range strings.Split(text, ",") {
				if n = strings.TrimSpace(n); n != "" {
					names = append(names, n)
				}
			}
			key := m.inProj
			return m, m.do(func() (string, error) {
				ph, err := m.core.Store.SetPhases(key, names)
				return phaseText(ph), err
			})
		case inVibe:
			if text == "" {
				return m, nil
			}
			place, why, _ := strings.Cut(text, ":")
			key, board := m.inProj, m.board
			return m, m.do(func() (string, error) {
				target, err := board.VibeTarget(key, "")
				if err != nil {
					return "", err
				}
				v, err := m.core.Store.AddVibe(core.Vibe{Place: strings.TrimSpace(place), Why: strings.TrimSpace(why), Target: target})
				return "vibe: " + v.Place, err
			})
		}
		return m, nil
	default:
		if k.Text == "" || k.Mod.Contains(tea.ModCtrl) || k.Mod.Contains(tea.ModAlt) {
			return m, nil
		}
		return m.typed(k.Text), nil
	}
}

// typed adds text typed or pasted into the input.
func (m Model) typed(text string) Model {
	if m.inKind == inAdopt || m.picking() {
		return m
	}
	if m.fresh {
		m.input, m.fresh = nil, false
	}
	m.input = append(m.input, []rune(text)...)
	if m.inKind == inFilter {
		m.filter = string(m.input)
		m.cursor = 0
	}
	m.repoCur, m.pickErr = 0, ""
	return m
}

func (m Model) startNew() (tea.Model, tea.Cmd) {
	n, c := m.newL, m.core
	m.message = "starting " + n.Harness + "…"
	return m, m.do(func() (string, error) {
		msg, err := c.StartNew(n)
		if err == nil {
			err = m.launchedHere("session", n.Harness)
		}
		first, _, _ := strings.Cut(msg, "\n")
		return "started " + first, err
	})
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
	case m.mode == modeAgent || m.mode == modeSession:
		if s, ok := m.session(); ok {
			key, harness = s.Key(), s.Harness
		}
	}
	if key == "" {
		if d, ok := lastExited(r.project); ok && r.session == nil && r.item == nil {
			return m.revive(d)
		}
		if r.project != nil {
			m.message = "nothing runs in " + r.project.Name + " and ym launched none of its sessions, so there is nothing to resume"
		}
		return m, nil
	}
	if err := m.core.Store.MarkOpened(key, time.Now()); err != nil {
		m.message = "error: " + err.Error()
	}
	if s, ok := m.board.Session(key); ok && s.Adoptable() && len(s.Copies) <= 1 && m.core.Launches != nil {
		m.startInput(inAdopt, key, "", "", false)
		m.inLabel = titleOf(s)
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
	if m.beside {
		return m.do(func() (string, error) {
			err := m.core.OpenBeside(t)
			switch {
			case errors.Is(err, core.ErrNoHost) && t.Handle != nil:
				return "runs in " + t.String() + "; O opens it beside ym only when ym runs inside tmux", nil
			case errors.Is(err, core.ErrNoHost):
				return t.Advice(), nil
			}
			return "opened " + t.String() + " beside", err
		})
	}
	if cmd := m.core.AttachCmd(t); cmd != nil {
		if t.Remote != "" {
			return m.attachRemote(t, cmd)
		}
		return tea.Exec(attached{cmd, m.out}, func(err error) tea.Msg { return doneMsg{"back from " + t.String(), err} })
	}
	return m.do(func() (string, error) {
		err := m.core.Focus(t)
		if errors.Is(err, core.ErrNoHost) {
			return t.Advice(), nil
		}
		return "opened " + t.String(), err
	})
}

func (m Model) pickKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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

// adopt moves the session onto ym's server in the background, then shows it.
func (m Model) adopt(key string) (tea.Model, tea.Cmd) {
	s, ok := m.board.Session(key)
	if !ok {
		m.message = m.inLabel + " is gone"
		return m, nil
	}
	m.message = "adopting " + m.inLabel + ": waits until it is idle, then ends it in its terminal"
	l := m.core.Launches
	return m, func() tea.Msg {
		t, err := l.Adopt(s, nil)
		return adoptedMsg{t, err}
	}
}

// each runs f on every ref, going on past failures, and joins what it said.
// sessionOf is the session a row or the open view is about.
func (m Model) sessionOf(r row) *core.Live {
	switch {
	case r.session != nil && r.member == nil:
		return r.session
	case r.item != nil:
		if s, ok := m.board.Session(r.item.Session); ok {
			return &s
		}
	case m.mode == modeSession || m.mode == modeAgent:
		if s, ok := m.session(); ok {
			return s
		}
	}
	return nil
}

func titleOf(s core.Live) string {
	if s.Title != "" {
		return s.Title
	}
	return core.Short(s.Key())
}

// remoteItems are the items other hosts last reported, open and lately closed, with their history.
func (m Model) remoteItems() []core.Item {
	var out []core.Item
	for _, r := range m.board.Remotes {
		out = append(append(out, r.Items...), r.Closed...)
	}
	return out
}

// revision is the commit this ym was built from, short, or its build time when it carries no commit.
func revision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "build"
	}
	var rev, at string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			at = s.Value
		}
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if rev == "" {
		return "build " + at
	}
	return rev
}

func (m *Model) nextLaunchProfile() {
	ps := m.board.Profiles
	if m.launchOn != "" {
		ps = nil
		for _, r := range m.board.Remotes {
			if r.Host == m.launchOn {
				ps = r.Profiles
			}
		}
	}
	ids := []string{""}
	for _, p := range ps {
		if p.Harness == m.newL.Harness {
			ids = append(ids, p.ID)
		}
	}
	for i, id := range ids {
		if id == m.newL.Profile {
			m.newL.Profile = ids[(i+1)%len(ids)]
			return
		}
	}
	m.newL.Profile = ""
}
