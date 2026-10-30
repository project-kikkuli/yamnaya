package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func TestSceneResumesWithTheCurrentBoardAfterEvents(t *testing.T) {
	old, colors, motion := pal, scene.Colors, scene.ReducedMotion
	pal, scene.Colors = newPalette(true), scene.TrueColor
	t.Cleanup(func() { pal, scene.Colors, scene.ReducedMotion = old, colors, motion })
	updated := workBoard()
	updated.Tree[0].Standing = core.StandStuck
	updated.Tree[0].Agents[0].Status = core.Waiting
	for _, reduced := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			messages []tea.Msg
		}{
			{"selection", []tea.Msg{keyOf("j")}},
			{"several selections", []tea.Msg{keyOf("j"), keyOf("k"), keyOf("j")}},
			{"filter overlay", []tea.Msg{keyOf("/"), tea.PasteMsg{Content: "importer"}, keyOf("esc")}},
			{"help overlay", []tea.Msg{keyOf("?"), keyOf("esc")}},
			{"confirmation overlay", []tea.Msg{keyOf("G"), keyOf("esc")}},
			{"hidden then shown", []tea.Msg{keyOf("A"), keyOf("A")}},
			{"theme change", []tea.Msg{keyOf("L")}},
			{"resize", []tea.Msg{tea.WindowSizeMsg{Width: 100, Height: 32}}},
			{"small terminal then restored", []tea.Msg{tea.WindowSizeMsg{Width: 48, Height: 16}, tea.WindowSizeMsg{Width: 174, Height: 72}}},
			{"board refresh", []tea.Msg{boardMsg{board: updated, theme: "kaiju"}}},
		} {
			t.Run(tc.name+map[bool]string{false: "/animated", true: "/reduced"}[reduced], func(t *testing.T) {
				scene.ReducedMotion = reduced
				m := New(&core.Core{})
				m.board, m.loaded, m.width, m.height = workBoard(), true, 174, 72
				m.board.Tree[0].Standing = core.StandMoving
				m.peaks.hidden, m.peaks.theme, m.peaks.dark = false, "kaiju", true
				m.out = &output{rate: 1e9}
				page := m.prepareView()
				m.page = &page
				m.settle(true)
				held := m.peaks.screen.c
				for _, msg := range tc.messages {
					next, _ := m.Update(msg)
					m = next.(Model)
					if !m.shown() {
						continue
					}
					text := tableText(*m.page)
					for _, line := range strings.Split(ansi.Strip(text), "\n") {
						if !strings.Contains(ansi.Strip(m.View().Content), strings.TrimSpace(line)) {
							t.Fatal("event did not update the table immediately")
						}
					}
					if reduced {
						if got, want := m.View().Content, scene.Frame(m.sceneOf(), m.width, m.height, 0, text); got != want {
							t.Fatal("reduced motion did not update the scene immediately")
						}
					} else if tc.name == "selection" || tc.name == "several selections" {
						if m.peaks.frame != 0 || m.View().Content != held.Draw(text) {
							t.Fatal("the scene moved before the quiet period ended")
						}
					}
				}
				if !reduced {
					m.peaks.quiet = time.Time{}
					m.peaks.start = time.Now().Add(-9 * scene.Step)
					next, _ := m.Update(mountainFrame{})
					m = next.(Model)
					if m.peaks.frame < 9 {
						t.Fatal("animation did not advance")
					}
				}
				text := tableText(*m.page)
				if got, want := m.View().Content, scene.Frame(m.sceneOf(), m.width, m.height, m.peaks.frame, text); got != want {
					t.Fatal("resumed scene differs from the current snapshot, selection or table")
				}
				if got := m.sceneOf().Selected; got != m.peakOf() || got == "" {
					t.Fatal("selection no longer identifies a board row")
				}
			})
		}
	}
}

func TestPendingSceneWaitsForFocusAndTerminalOutput(t *testing.T) {
	old, colors, motion := pal, scene.Colors, scene.ReducedMotion
	pal, scene.Colors, scene.ReducedMotion = newPalette(true), scene.TrueColor, false
	t.Cleanup(func() { pal, scene.Colors, scene.ReducedMotion = old, colors, motion })
	m := New(&core.Core{})
	m.board, m.loaded, m.width, m.height = workBoard(), true, 160, 48
	m.board.Tree[0].Standing = core.StandMoving
	m.peaks.hidden, m.peaks.theme = false, "kaiju"
	m.out = &output{rate: 1e9}
	page := m.prepareView()
	m.page = &page
	m.settle(true)
	next, _ := m.Update(keyOf("j"))
	m = next.(Model)
	held := m.View().Content
	clicked := false
	for y, line := range strings.Split(ansi.Strip(held), "\n") {
		if x := strings.Index(line, "hchb-readback"); x >= 0 {
			next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: ansi.StringWidth(line[:x]), Y: y})
			m = next.(Model)
			if m.peakOf() != "lead" {
				t.Fatal("clicking a defender lost its unit while the scene was pending")
			}
			clicked = true
			break
		}
	}
	if !clicked {
		t.Fatal("no defender hit target in the held scene")
	}
	held = m.View().Content
	m.peaks.quiet = time.Time{}
	m.peaks.start = time.Now().Add(-11 * scene.Step)
	m.out.writing = true
	next, cmd := m.Update(mountainFrame{})
	m = next.(Model)
	if cmd == nil || m.peaks.frame != 0 || m.View().Content != held {
		t.Fatal("pending scene moved while the terminal was behind")
	}
	m.out.writing = false
	next, _ = m.Update(tea.BlurMsg{})
	m = next.(Model)
	m.peaks.ticking = false
	if m.animate() != nil {
		t.Fatal("blurred scene scheduled animation")
	}
	next, _ = m.Update(tea.FocusMsg{})
	m = next.(Model)
	next, _ = m.Update(mountainFrame{})
	m = next.(Model)
	if m.peaks.frame < 11 {
		t.Fatal("pending scene did not resume after focus and output recovered")
	}
	text := tableText(*m.page)
	if got, want := m.View().Content, scene.Frame(m.sceneOf(), m.width, m.height, m.peaks.frame, text); got != want {
		t.Fatal("resumed scene lost the pending selection")
	}
}
