package themes

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// agentSignals are the glyphs that must read at a glance at every hour: who is busy, who waits, what needs you.
// Each shows on a lone unit changed by scene, and is gone from it changed by without.
var agentSignals = []struct {
	signal         scene.Signal
	scene, without func(*scene.Peak)
}{
	{scene.SigBusy, func(*scene.Peak) {}, noTeam},
	{scene.SigWaiting, func(p *scene.Peak) { p.Team[0].Stance = scene.Waiting }, noTeam},
	{scene.SigNeedsYou, func(p *scene.Peak) { p.NeedsYou = true }, func(*scene.Peak) {}},
}

func noTeam(p *scene.Peak) { p.Team = nil }

// redSignals must stay red whatever is done to make them read.
var redSignals = map[scene.Signal]bool{scene.SigWaiting: true, scene.SigNeedsYou: true}

// everyHour is each quarter hour of a day, in a dark and a light terminal.
func everyHour(f func(now time.Time, dark bool)) {
	for _, dark := range []bool{true, false} {
		for minutes := 0; minutes < 24*60; minutes += 15 {
			f(time.Date(2026, 1, 15, minutes/60, minutes%60, 0, 0, time.Local), dark)
		}
	}
}

// inks is every color pair the signal's legend glyph is drawn in on a lone unit at now: the cells holding a rune
// of that glyph that the signal changed.
func inks(t *testing.T, th scene.Theme, g scene.Signal, f, without func(*scene.Peak), now time.Time, dark bool) [][2]scene.RGB {
	t.Helper()
	s, base := lone(th.Name(), f), lone(th.Name(), without)
	s.Now, s.Dark = now, dark
	base.Now, base.Dark = now, dark
	var glyph string
	for _, e := range th.Legend(s).Entries {
		if e.Signal == g {
			glyph = e.Glyph
		}
	}
	runes := strings.ReplaceAll(glyph, " ", "")
	if runes == "" {
		t.Fatalf("%s: %s has no glyph to check", th.Name(), g)
	}
	const w, h = 160, 48
	c, b := th.Paint(s, w, h, 0, scene.Rect{}), th.Paint(base, w, h, 0, scene.Rect{})
	var out [][2]scene.RGB
	for y := range h {
		for x := range w {
			style, cell := c.Cell(x, y)
			if bs, bc := b.Cell(x, y); cell == "" || !strings.Contains(runes, cell) || style == bs && cell == bc {
				continue
			}
			if fg, bg, ok := c.Ink(x, y); ok {
				out = append(out, [2]scene.RGB{fg, bg})
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s at %s dark=%v: no %q drawn for %s", th.Name(), now.Format("15:04"), dark, glyph, g)
	}
	return out
}

func TestAgentAlertAndFailingGlyphsStandOutAndRedOnesStayRedAtEveryHour(t *testing.T) {
	for _, th := range scene.Themes() {
		for _, a := range agentSignals {
			t.Run(fmt.Sprint(th.Name(), "/", a.signal), func(t *testing.T) {
				everyHour(func(now time.Time, dark bool) {
					for _, ink := range inks(t, th, a.signal, a.scene, a.without, now, dark) {
						if c := scene.Contrast(ink[0], ink[1]); c < scene.MinContrast {
							t.Errorf("at %s dark=%v: %s drawn %s on %s, contrast %.2f, want at least %.1f",
								now.Format("15:04"), dark, a.signal, ink[0].Hex(), ink[1].Hex(), c, scene.MinContrast)
						}
						if redSignals[a.signal] && !scene.IsRed(ink[0]) && !scene.IsRed(ink[1]) {
							t.Errorf("at %s dark=%v: %s drawn %s on %s, neither red", now.Format("15:04"), dark, a.signal, ink[0].Hex(), ink[1].Hex())
						}
					}
				})
			})
		}
	}
}

func TestBusyAndWaitingAgentsDifferAtEveryHour(t *testing.T) {
	for _, th := range scene.Themes() {
		t.Run(th.Name(), func(t *testing.T) {
			everyHour(func(now time.Time, dark bool) {
				busy := inks(t, th, scene.SigBusy, agentSignals[0].scene, noTeam, now, dark)[0][0]
				waiting := inks(t, th, scene.SigWaiting, agentSignals[1].scene, noTeam, now, dark)[0][0]
				dr, dg, db := busy.R-waiting.R, busy.G-waiting.G, busy.B-waiting.B
				if dist := dr*dr + dg*dg + db*db; dist < .35*.35 {
					t.Errorf("at %s dark=%v: busy %s and waiting %s agents are too alike", now.Format("15:04"), dark, busy.Hex(), waiting.Hex())
				}
			})
		})
	}
}
