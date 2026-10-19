package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func alpineBoard() core.Board {
	s := core.Live{Session: core.Session{ID: "ridge", Status: core.Busy, Crew: []core.Member{
		{ID: "rope", Status: core.Busy}, {ID: "camp", Status: core.Idle},
	}}}
	p := core.Project{Key: "ridge", Name: "North ridge", Sessions: []core.Live{s}}
	return core.Board{At: time.Unix(100, 0), Projects: []core.Project{p}, Sessions: []core.Live{s}}
}

func TestExpeditionTransitions(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*core.Board)
		want string
	}{
		{"idle rests without claiming a summit", func(b *core.Board) { b.Projects[0].Sessions[0].Crew[0].Status = core.Idle }, ""},
		{"completed crew reaches summit", func(b *core.Board) { b.Projects[0].Sessions[0].Crew[0].Status = core.Done }, "summit"},
		{"cut crew falls without a summit", func(b *core.Board) {
			b.Projects[0].Sessions[0].Crew[0].Status, b.Projects[0].Sessions[0].Crew[0].Cut = core.Done, true
		}, "fall"},
		{"work reshapes the terrain", func(b *core.Board) { b.Projects[0].Sessions[0].Crew[0].Facts.Tools = map[string]int{"Read": 2} }, "work"},
		{"new crew departs", func(b *core.Board) {
			b.Projects[0].Sessions[0].Crew = append(b.Projects[0].Sessions[0].Crew, core.Member{ID: "new", Status: core.Busy})
		}, "depart"},
		{"disappearance does not invent a kill", func(b *core.Board) { b.Projects, b.Sessions = nil, nil }, ""},
		{"confirmed external kill sweeps the expedition", func(b *core.Board) {
			b.Projects, b.Sessions = nil, nil
			b.Kills = map[string]time.Time{"ridge": b.At}
		}, "storm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a alpine
			b := alpineBoard()
			a.observe(b)
			tc.edit(&b)
			a.observe(b)
			if tc.want == "" {
				if len(a.events) != 0 {
					t.Fatalf("unexpected events: %+v", a.events)
				}
				return
			}
			if len(a.events) != 1 || a.events[0].kind != tc.want {
				t.Fatalf("events %+v; want %s", a.events, tc.want)
			}
			if tc.want == "storm" && a.events[0].people != 3 {
				t.Fatalf("storm lost the crew: %+v", a.events[0])
			}
			a.observe(b)
			if len(a.events) != 1 {
				t.Fatalf("refresh replayed an event: %+v", a.events)
			}
			for range 80 {
				a.advance()
			}
			if len(a.events) > 0 {
				t.Fatalf("effect did not end: %+v", a.events)
			}
		})
	}
}

func TestLandscapeStartsFromCurrentStateWithoutReplayingHistory(t *testing.T) {
	b := alpineBoard()
	b.Kills = map[string]time.Time{"ridge": b.At.Add(-time.Minute)}
	b.Projects[0].Sessions[0].Crew[0].Status = core.Done
	b.Projects[0].Sessions[0].Facts.Tools = map[string]int{"Bash": 200}
	var a alpine
	a.observe(b)
	a.observe(b)
	if len(a.events) > 0 || a.mountains[0].relief != 0 {
		t.Fatalf("startup replayed old work: %+v", a)
	}
	child := core.Live{Session: core.Session{ID: "rope", Status: core.Busy}}
	b.Projects[0].Sessions = append(b.Projects[0].Sessions, child, child)
	a.observe(b)
	if len(a.teams) != 3 {
		t.Fatalf("duplicate process or crew identity counted twice: %+v", a.teams)
	}
}

func TestWeatherMovesWithoutReadingTheBoardOrMovingInput(t *testing.T) {
	usePalette(t, false)
	m := Model{width: 120, height: 40, loaded: true, mode: modeInput, inKind: inFilter, input: []rune("ridge"), cursor: 2}
	m.alpine.observe(alpineBoard())
	before := m.alpineView(10)
	for range 20 {
		next, _ := m.Update(alpineFrame{})
		m = next.(Model)
	}
	if m.alpineView(10) == before {
		t.Fatal("resting landscape stopped moving")
	}
	if m.cursor != 2 || string(m.input) != "ridge" || m.mode != modeInput {
		t.Fatalf("animation changed the controls: %+v", m)
	}
}

func TestAlpineBoardKeepsControlsVisibleAcrossTerminalSizes(t *testing.T) {
	usePalette(t, false)
	for _, size := range [][2]int{{40, 12}, {80, 24}, {80, 28}, {100, 40}, {160, 50}, {240, 80}} {
		for _, effect := range []string{"", "summit", "storm"} {
			t.Run(fmt.Sprint(size, effect), func(t *testing.T) {
				m, _, _ := setup(t)
				m.width, m.height = size[0], size[1]
				if effect != "" {
					m.alpine.events = []alpineEvent{{kind: effect, mountain: m.alpine.mountains[0].key, frame: 0, people: 8}}
				}
				for _, frame := range []uint64{0, 15, 40, 70} {
					m.alpine.frame = frame
					view := m.View()
					lines := strings.Split(view, "\n")
					if len(lines) != m.height {
						t.Fatalf("%d lines on %d-row terminal", len(lines), m.height)
					}
					for _, line := range lines {
						if ansi.StringWidth(line) > m.width {
							t.Fatalf("line exceeds %d: %s", m.width, line)
						}
					}
					if !strings.Contains(ansi.Strip(view), "NEEDS YOU") {
						t.Fatalf("landscape displaced the task board:\n%s", view)
					}
				}
			})
		}
	}
}

func TestAnimationPreservesSelectionAndScrollAcrossFrames(t *testing.T) {
	m, _, _ := setup(t)
	m = press(t, m, "down")
	r, _ := m.selected()
	key := rowKey(r)
	for range 90 {
		next, _ := m.Update(alpineFrame{})
		m = next.(Model)
	}
	r, _ = m.selected()
	if rowKey(r) != key {
		t.Fatalf("selection moved from %s to %s", key, rowKey(r))
	}
	m.mode, m.scrollY = modeAgent, 5
	next, _ := m.Update(alpineFrame{})
	if next.(Model).scrollY != 5 {
		t.Fatal("animation reset the detail scroll")
	}
}

func TestRangeToggleLeavesTypedCapitalAInTheInput(t *testing.T) {
	m, _, _ := setup(t)
	m = press(t, m, "A")
	if m.alpineHeight() != 0 {
		t.Fatal("A did not hide the landscape")
	}
	m = press(t, m, "A", "/", "A")
	if m.alpineHeight() == 0 || string(m.input) != "A" {
		t.Fatal("typing A toggled the landscape instead of entering text")
	}
}

func TestMountainLabelsRespectWideCharactersAndNoColor(t *testing.T) {
	for _, color := range []bool{false, true} {
		t.Run(fmt.Sprint(color), func(t *testing.T) {
			usePalette(t, color)
			b := alpineBoard()
			b.Projects[0].Name = "雪山 🏔 expedition"
			m := Model{width: 80, height: 32, board: b}
			m.alpine.observe(b)
			v := m.alpineView(m.alpineHeight())
			if colored(v) != color || strings.Contains(v, "\x00") {
				t.Fatalf("invalid landscape output: %q", v)
			}
			for _, line := range strings.Split(v, "\n") {
				if ansi.StringWidth(line) > m.width {
					t.Fatalf("wide label overflowed: %q", line)
				}
			}
		})
	}
}

func TestTornadoRequiresSuccessfulKillAndFreshObservation(t *testing.T) {
	for _, stubborn := range []bool{false, true} {
		t.Run(fmt.Sprint("stubborn=", stubborn), func(t *testing.T) {
			m, c, _ := setup(t)
			signals(c, stubborn)
			m = press(t, to(t, m, mission("lead")), "K", "y")
			storms := 0
			for _, e := range m.alpine.events {
				if e.kind == "storm" {
					storms++
				}
			}
			want := 1
			if stubborn {
				want = 0
			}
			if storms != want {
				t.Fatalf("%d tornadoes; want %d", storms, want)
			}
		})
	}
}

func TestFailedBoardRefreshPreservesExpeditions(t *testing.T) {
	m, _, _ := setup(t)
	before := len(m.alpine.teams)
	next, _ := m.Update(boardMsg{err: errors.New("unavailable")})
	m = next.(Model)
	if len(m.alpine.teams) != before || len(m.alpine.events) > 0 {
		t.Fatal("failed refresh changed the landscape")
	}
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if cmd != nil {
		t.Fatal("resize unexpectedly reread the board")
	}
}

func BenchmarkAlpineFrame(b *testing.B) {
	m := Model{width: 160, height: 50, loaded: true}
	board := alpineBoard()
	for i := range 100 {
		board.Projects[0].Sessions[0].Crew = append(board.Projects[0].Sessions[0].Crew, core.Member{ID: fmt.Sprint("climber", i), Status: core.Busy})
	}
	m.alpine.observe(board)
	for b.Loop() {
		m.alpine.advance()
		_ = m.alpineView(12)
	}
}
