package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// sketch is a second theme so switching has somewhere to go.
type sketch struct{}

func init() { scene.Register(sketch{}) }

func (sketch) Name() string { return "sketch" }

func (sketch) Paint(s scene.Scene, width, height, frame int, box scene.Rect) *scene.Canvas {
	return scene.NewCanvas(width, height, box)
}

func (sketch) Hit(scene.Scene, int, int, scene.Rect, int, int) string { return "" }

func (sketch) Legend(scene.Scene) scene.Legend {
	var l scene.Legend
	for _, g := range scene.Signals() {
		l.Entries = append(l.Entries, scene.Entry{Signal: g, Glyph: "*", Look: "a pencil mark for " + g.String()})
	}
	l.Decoration = []string{"paper grain"}
	return l
}

func TestLSwitchesTheThemeKeepsItInStateAndHelpFollows(t *testing.T) {
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := New(&core.Core{Store: st})
	m.loaded, m.peaks.hidden = true, true
	names := []string{}
	for _, th := range scene.Themes() {
		names = append(names, th.Name())
	}
	for i := range names {
		want := names[(i+1)%len(names)]
		m = switchTheme(t, m)
		if got, _ := st.Theme(); got != want || m.sceneOf().Theme != want {
			t.Fatalf("after %d presses (%s): stored %q, drawn %q, want %q", i+1, m.message, got, m.sceneOf().Theme, want)
		}
	}
	m = switchTheme(t, press(t, m, "?"))
	if m.mode != modeHelp || !strings.Contains(ansi.Strip(helpText(m.width, m.legendScene())), "Theme: "+names[1]) {
		t.Fatalf("L in help did not switch the legend to %s", names[1])
	}
	stale := boardMsg{theme: names[0], themeSeq: m.peaks.themeSeq - 1}
	m.themed(stale)
	if m.peaks.theme != names[1] {
		t.Fatalf("a board read before the last L took the theme back to %q", m.peaks.theme)
	}
	if err := st.SetTheme(names[0]); err != nil {
		t.Fatal(err)
	}
	m.themed(boardMsg{theme: names[0], themeSeq: m.peaks.themeSeq})
	if m.peaks.theme != names[0] {
		t.Fatalf("ym theme from a shell did not reach the board: %q", m.peaks.theme)
	}
}

// switchTheme presses L and runs the write to ym state it dispatched.
func switchTheme(t *testing.T, m Model) Model {
	t.Helper()
	next, cmd := m.Update(keyOf("L"))
	m = next.(Model)
	if done, ok := cmd().(doneMsg); !ok || done.err != nil {
		t.Fatalf("L dispatched %+v, want the theme written", done)
	}
	return m
}

func TestAKilledUnitFallsForAtMostTheKillTimeThenLeavesTheScene(t *testing.T) {
	cases := []struct {
		name     string
		keys     []string
		id       string
		boardHas bool
	}{
		{"goal still on the board", []string{"node:mgr"}, "mgr", true},
		{"goal already gone", []string{"node:mgr"}, "mgr", false},
		{"session the owner started", []string{"lead"}, "lead", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New(&core.Core{})
			m.board, m.loaded = workBoard(), true
			t0 := time.Now()
			m.fell(c.keys, t0)
			if !c.boardHas {
				m.board.Tree = nil
			}
			dying := func(at time.Time) (float64, bool) {
				d, found := 0.0, false
				m.falling(m.boardScene(), at).Walk(func(p scene.Peak) {
					if p.ID == c.id {
						d, found = p.Dying, true
					}
				}, func(scene.Range) {})
				return d, found
			}
			if d, ok := dying(t0.Add(scene.KillTime / 2)); !ok || d < .4 || d > .6 {
				t.Fatalf("halfway through the kill: present %v, dying %v", ok, d)
			}
			if _, ok := dying(t0.Add(scene.KillTime)); ok {
				t.Fatal("the unit is still on the scene after its kill played")
			}
			m.settled(c.keys, false)
			m.board.Tree, m.board.Projects = nil, nil
			m.fallen(t0.Add(2 * scene.KillTime))
			if len(m.peaks.kills) != 0 {
				t.Fatalf("falls %v kept after the kill reported back and the board dropped the unit", m.peaks.kills)
			}
		})
	}
}

func TestAFailedKillPutsTheUnitBack(t *testing.T) {
	m := New(&core.Core{})
	m.board, m.loaded = workBoard(), true
	m.fell([]string{"node:mgr"}, time.Now().Add(-time.Minute))
	m.settled([]string{"node:mgr"}, true)
	found := false
	m.sceneOf().Walk(func(p scene.Peak) { found = found || p.ID == "mgr" && p.Dying == 0 }, func(scene.Range) {})
	if !found {
		t.Fatal("a failed kill left its goal off the scene")
	}
}

func TestSceneStartsHiddenAndAShowsItWithAnimation(t *testing.T) {
	defer func(was bool) { scene.ReducedMotion = was }(scene.ReducedMotion)
	for _, tc := range []struct {
		name       string
		presses    int
		wantHidden bool
	}{{"default", 0, true}, {"A shows", 1, false}, {"A twice hides", 2, true}} {
		t.Run(tc.name, func(t *testing.T) {
			scene.ReducedMotion = false
			m := emptyBoard(t)
			m.out = &output{}
			defer func(was bool) { pal.color = was }(pal.color)
			pal.color = true
			for range tc.presses {
				next, _ := m.Update(keyOf("A"))
				m = next.(Model)
			}
			if m.peaks.hidden != tc.wantHidden {
				t.Fatalf("hidden %v, want %v", m.peaks.hidden, tc.wantHidden)
			}
			if tc.presses == 1 && !m.peaks.ticking {
				t.Fatal("showing the scene schedules no frames")
			}
			if tc.wantHidden {
				next, cmd := m.Update(mountainFrame{})
				if cmd != nil || next.(Model).peaks.ticking {
					t.Fatal("a frame is scheduled while the scene is hidden")
				}
			}
		})
	}
}
