package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

const mountainInterval = 125 * time.Millisecond

// mountains is the range drawn behind the board: one peak per goal, one range per chief.
type mountains struct {
	frame        int
	hidden, dark bool
}

type mountainFrame struct{}

func mountainTick() tea.Cmd {
	return tea.Tick(mountainInterval, func(time.Time) tea.Msg { return mountainFrame{} })
}

func newMountains() mountains { return mountains{dark: lipgloss.HasDarkBackground()} }

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
	case core.StandQuiet:
		return scene.Clouds
	case core.StandDown:
		return scene.Eruption
	case core.StandStuck:
		return scene.Storm
	}
	return scene.Clear
}

// sceneOf is the board as mountains: a chief is a range, a goal a peak, its agents the climbers on it.
func (m Model) sceneOf() scene.Scene {
	s := scene.Scene{Dark: m.peaks.dark}
	if r, ok := m.selected(); ok && r.node != nil {
		s.Selected = r.node.Manager.ID
	}
	for _, n := range m.board.Tree {
		if n.Goal != nil {
			s.Peaks = append(s.Peaks, peakOf(n))
		} else {
			s.Ranges = append(s.Ranges, rangeOf(n))
		}
	}
	return s
}

func peakOf(n core.Node) scene.Peak {
	p := scene.Peak{ID: n.Manager.ID, Name: shortAsk(n.Goal.Ask, 24), Weather: weather(n.Standing), Climbers: len(n.Agents)}
	if n.Goal.State == core.GoalDone {
		p.Progress = 1
	} else if total := len(n.Agents); total > 0 {
		done := 0
		for _, a := range n.Agents {
			if a.Status == core.Done || a.Status == core.Idle {
				done++
			}
		}
		p.Progress = 0.15 + 0.7*float64(done)/float64(total)
	} else {
		p.Progress = 0.05
	}
	return p
}

func rangeOf(n core.Node) scene.Range {
	r := scene.Range{ID: n.Manager.ID, Name: n.Manager.Name, Weather: weather(n.Standing)}
	for _, c := range n.Children {
		if c.Goal != nil {
			r.Peaks = append(r.Peaks, peakOf(c))
		} else {
			r.Ranges = append(r.Ranges, rangeOf(c))
		}
	}
	return r
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
	text := page.String()
	if !m.shown() {
		return text
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r", ""), "\t", "    ")
	return scene.Frame(m.sceneOf(), m.width, m.height, m.peaks.frame, text)
}
