package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/progress"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// mountains is the range drawn behind the board. Each repository is a range: its goals stand as the main peaks
// and the sessions the owner started beside them, every agent a named climber and every pull request a camp.
// keyQuiet is how long the mountains hold still after a key or paste, so the board answers it first.
const keyQuiet = 300 * time.Millisecond

type mountains struct {
	// start is when the animation clock began; frame counts scene.Step since then.
	start        time.Time
	frame        int
	hidden, dark bool
	blurred      bool
	ticking      bool
	// quiet is when animation may resume after the last key.
	quiet time.Time
	// screen is the mountains as sent to the terminal, which follow the scene a frame's worth of bytes at a time.
	screen *onScreen
}

type onScreen struct {
	c *scene.Canvas
	// left is how many cells still differ from the scene; ver counts changes to c.
	left, ver int
}

// cellBytes is about what one changed cell of mountains costs on the wire, colors and cursor moves included.
const cellBytes = 40

type mountainFrame struct{}

func newMountains() mountains {
	return mountains{dark: lipgloss.HasDarkBackground(os.Stdin, os.Stdout), start: time.Now(), screen: &onScreen{}}
}

// settle moves the mountains on screen toward the scene: by what the terminal takes in a frame when step is
// set, so a key never waits long behind them, and not at all otherwise. Hidden mountains rise again from nothing.
// With no terminal to pace, the scene is drawn whole.
func (m *Model) settle(step bool) {
	sc := m.peaks.screen
	if sc == nil || m.out == nil {
		return
	}
	if !m.shown() {
		sc.c, sc.left = nil, 0
		return
	}
	page := m.page
	if page == nil {
		prepared := m.prepareView()
		page = &prepared
	}
	goal := scene.Paint(m.sceneOf(), m.width, m.height, m.peaks.frame, tableText(*page))
	if !sc.c.Fits(m.width, m.height) {
		sc.c = scene.Blank(m.width, m.height)
		sc.ver++
	}
	n := 0
	if step {
		n = max(1, m.out.budget()/cellBytes)
	}
	c, left := sc.c.Toward(goal, n)
	if left != sc.left || n > 0 {
		sc.c, sc.left = c, left
		sc.ver++
	}
}

// animate schedules the next frame at the scene's own pace, or none when nothing on it moves or nobody can see it.
func (m *Model) animate() tea.Cmd {
	if m.peaks.ticking || m.peaks.blurred || !m.shown() {
		return nil
	}
	if sc := m.peaks.screen; sc != nil && sc.left > 0 {
		m.peaks.ticking = true
		return tea.Tick(frameTime, func(time.Time) tea.Msg { return mountainFrame{} })
	}
	pace := scene.Pace(m.sceneOf())
	if pace == 0 {
		return nil
	}
	m.peaks.ticking = true
	return tea.Tick(pace, func(time.Time) tea.Msg { return mountainFrame{} })
}

// shown reports whether the board floats over the mountains: on a terminal big enough, on the board's own views.
func (m Model) shown() bool {
	if m.peaks.hidden || !pal.color || !m.loaded || m.width < 72 || m.height < 24 {
		return false
	}
	switch m.mode {
	case modeHelp, modeScreen, modeWhy, modePick, modeItems:
		return false
	case modeInput:
		return m.inKind != inNewRepo
	}
	return true
}

// tableWidth is how wide the board is drawn: the whole terminal, or narrower so the mountains show around it.
func (m Model) tableWidth() int {
	if !m.shown() {
		return m.width
	}
	return max(60, min(m.width-6, 132))
}

func weather(standing string) scene.Weather {
	switch standing {
	case core.StandDone:
		return scene.Summit
	case core.StandQuiet, core.StandAdrift:
		return scene.Clouds
	case core.StandDown:
		return scene.Eruption
	case core.StandStuck:
		return scene.Storm
	}
	return scene.Clear
}

// sceneOf is the board as mountains. Chiefs are ranges of the goals under them; every other goal and every
// session the owner started stands in the range of its repository.
func (m Model) sceneOf() scene.Scene {
	b := m.board
	s := scene.Scene{Dark: m.peaks.dark, Selected: m.peakOf(), Now: time.Now().Truncate(time.Minute)}
	waiting := needs(b)
	repos := map[string]*scene.Range{}
	var order []string
	repo := func(id string) *scene.Range {
		name := core.RepoLabel(id)
		if id == "" {
			id, name = "elsewhere", "elsewhere"
		}
		if repos[id] == nil {
			repos[id] = &scene.Range{ID: "repo:" + id, Name: name}
			order = append(order, id)
		}
		return repos[id]
	}
	for _, n := range b.Tree {
		if n.Goal != nil {
			r := repo(n.Repo)
			r.Peaks = append(r.Peaks, goalPeak(n, waiting))
		} else {
			s.Ranges = append(s.Ranges, chiefRange(n, waiting))
		}
	}
	for _, p := range b.Projects {
		if p.Manager != "" || !p.Shown() {
			continue
		}
		r := repo(p.Repo)
		r.Peaks = append(r.Peaks, missionPeak(p, waiting))
	}
	for _, name := range order {
		s.Ranges = append(s.Ranges, *repos[name])
	}
	return s
}

// needs is every mission, goal and manager with something waiting on the owner.
func needs(b core.Board) map[string]bool {
	out := map[string]bool{}
	for _, n := range b.Needs {
		out[n.Project] = true
		if n.Goal != nil {
			out[n.Goal.ManagerID] = true
		}
		if n.Manager != "" {
			out[n.Manager] = true
		}
	}
	return out
}

func chiefRange(n core.Node, waiting map[string]bool) scene.Range {
	r := scene.Range{ID: n.Manager.ID, Name: n.Manager.Name, Weather: weather(n.Standing)}
	for _, c := range n.Children {
		if c.Goal != nil {
			r.Peaks = append(r.Peaks, goalPeak(c, waiting))
		} else {
			r.Ranges = append(r.Ranges, chiefRange(c, waiting))
		}
	}
	return r
}

// goalPeak is a goal measured the way missions are: its agents and their crews finished, and the gates its pull
// requests passed. A done goal stands at the summit.
func goalPeak(n core.Node, waiting map[string]bool) scene.Peak {
	var lives []core.Live
	if n.Live != nil {
		lives = append(lives, *n.Live)
	}
	lives = append(lives, n.Agents...)
	key := ""
	if n.Live != nil {
		key = n.Live.Key()
	}
	p := expedition(core.Project{Key: key, Sessions: lives})
	p.ID, p.Name = n.Manager.ID, shortAsk(n.Goal.Ask, 32)
	p.Weather = weather(n.Standing)
	p.NeedsYou = waiting[n.Manager.ID]
	switch n.Goal.State {
	case core.GoalDone:
		p.Progress = 1
	case core.GoalVerifying:
		p.Progress = max(p.Progress, 0.9)
	}
	return p
}

// missionPeak is a session the owner started, its weather read from what its agents and pull requests do.
func missionPeak(pr core.Project, waiting map[string]bool) scene.Peak {
	p := expedition(pr)
	p.ID, p.Name = pr.Key, pr.Name
	p.NeedsYou = waiting[pr.Key]
	p.Weather = scene.Clouds
	merged := len(p.Camps) > 0
	for _, c := range p.Camps {
		switch {
		case c.State == scene.Failing:
			p.Weather = scene.Storm
		case p.Weather != scene.Storm && p.Weather != scene.Clear && len(p.Team) > 0:
			p.Weather = scene.Clear
		}
		merged = merged && c.State == scene.Merged
	}
	for _, c := range p.Team {
		if c.Stance != scene.Resting && p.Weather == scene.Clouds {
			p.Weather = scene.Clear
		}
	}
	if merged && p.Weather == scene.Clouds {
		p.Weather = scene.Summit
	}
	return p
}

// expedition is the team, camps and measured progress of a set of sessions.
func expedition(pr core.Project) scene.Peak {
	var p scene.Peak
	seen := map[string]bool{}
	for _, s := range pr.Sessions {
		if s.Key() != pr.Key || s.Status != core.Idle {
			p.Team = appendClimber(p.Team, s.Title, s.Status, s.Key() != pr.Key)
		}
		for _, c := range s.Crew {
			if !c.Cut {
				p.Team = appendClimber(p.Team, c.Name, c.Status, c.Teammate)
			}
		}
		for _, x := range s.AllPRs() {
			u := progress.PRUnit(x)
			if u.Closed || seen[x.URL] {
				continue
			}
			seen[x.URL] = true
			c := scene.Camp{Label: fmt.Sprintf("#%d", x.Number), Stage: u.Passed()}
			switch {
			case u.Met[5]:
				c.State = scene.Merged
			case u.Failing:
				c.State = scene.Failing
			case u.Met[3]:
				c.State = scene.Green
			}
			p.Camps = append(p.Camps, c)
		}
	}
	if f, ok := progress.Of(pr).Fraction(); ok {
		p.Progress = f
	}
	return p
}

// appendClimber adds an agent by what it is doing: busy ones climb, waiting ones signal, idle teammates rest.
// Agents that finished have left the mountain.
func appendClimber(team []scene.Climber, name string, st core.Status, stays bool) []scene.Climber {
	switch {
	case st == core.Busy:
		return append(team, scene.Climber{Name: name, Stance: scene.Climbing})
	case st == core.Waiting:
		return append(team, scene.Climber{Name: name, Stance: scene.Waiting})
	case st == core.Idle && stays:
		return append(team, scene.Climber{Name: name, Stance: scene.Resting})
	}
	return team
}

// peakOf is the peak or range the selected row stands for.
func (m Model) peakOf() string {
	r, ok := m.selected()
	if !ok {
		return ""
	}
	return m.peakOfRow(r)
}

// pick selects the board row of the peak or range drawn at cell x, y.
func (m *Model) pick(x, y int) {
	id := scene.Hit(m.sceneOf(), m.width, m.height, m.prepareView().String(), x, y)
	if id == "" {
		return
	}
	for i, r := range m.rows() {
		if m.peakOfRow(r) == id {
			m.cursor = i
			return
		}
	}
}

func (m Model) peakOfRow(r row) string {
	b := m.board
	switch {
	case r.node != nil:
		return r.node.Manager.ID
	case r.need != nil && r.need.Goal != nil:
		return r.need.Goal.ManagerID
	case r.need != nil:
		return r.need.Project
	case r.session != nil:
		return b.ProjectOf(r.session.Key())
	case r.project != nil:
		return r.project.Key
	case r.dormant != nil:
		return b.ProjectOf(r.dormant.Key)
	case r.item != nil:
		return b.ProjectOf(r.item.Session)
	case r.repo != nil:
		if r.repo.Key == "" {
			return "repo:elsewhere"
		}
		return "repo:" + r.repo.Key
	}
	return ""
}

func shortAsk(ask string, n int) string {
	words := strings.Fields(ask)
	out := ""
	for _, w := range words {
		if len(out)+len(w)+1 > n {
			break
		}
		if out != "" {
			out += " "
		}
		out += w
	}
	if out == "" && len(words) > 0 {
		out = clip(words[0], n)
	}
	return out
}

// paint draws the page: alone, or floating over the mountains.
func (m Model) paint(page viewPage) string {
	if !m.shown() {
		return page.String()
	}
	text := tableText(page)
	if sc := m.peaks.screen; sc != nil && m.out != nil {
		if !sc.c.Fits(m.width, m.height) {
			sc.c = scene.Blank(m.width, m.height)
		}
		return sc.c.Draw(text)
	}
	return scene.Frame(m.sceneOf(), m.width, m.height, m.peaks.frame, text)
}

func tableText(page viewPage) string {
	return strings.ReplaceAll(strings.ReplaceAll(page.String(), "\r", ""), "\t", "    ")
}
