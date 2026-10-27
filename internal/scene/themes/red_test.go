package themes

import (
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// reds is every glyph a canvas draws in red; pixel cells, a space or a half or full block, are not glyphs.
func reds(c *scene.Canvas, w, h int) []string {
	var out []string
	for y := range h {
		for x := range w {
			_, glyph := c.Cell(x, y)
			switch glyph {
			case "", " ", "▀", "▄", "█":
				continue
			}
			if fg, _, ok := c.Ink(x, y); ok && scene.IsRed(fg) {
				out = append(out, glyph)
			}
		}
	}
	return out
}

// showsRed reports whether any cell of a canvas is red, in its glyph or behind it.
func showsRed(c *scene.Canvas, w, h int) bool {
	for y := range h {
		for x := range w {
			if fg, bg, ok := c.Ink(x, y); ok && (scene.IsRed(fg) || scene.IsRed(bg)) {
				return true
			}
		}
	}
	return false
}

// calm is the busy board with nothing waiting, nothing needing the owner and no CI failing.
func calm(theme string) scene.Scene {
	s := busy(theme, .5)
	var quiet func(rs []scene.Range)
	quiet = func(rs []scene.Range) {
		for i := range rs {
			for k := range rs[i].Peaks {
				p := &rs[i].Peaks[k]
				p.NeedsYou = false
				for j := range p.Team {
					if p.Team[j].Stance == scene.Waiting {
						p.Team[j].Stance = scene.Climbing
					}
				}
				for j := range p.Camps {
					if p.Camps[j].State == scene.Failing {
						p.Camps[j].State = scene.Open
					}
				}
			}
			quiet(rs[i].Ranges)
		}
	}
	quiet(s.Ranges)
	return s
}

func TestRedMarksOnlyWaitingAgentsNeedsYouAndFailingCI(t *testing.T) {
	reserved := map[string]func(*scene.Peak){
		"waiting":    func(p *scene.Peak) { p.Team[0].Stance = scene.Waiting },
		"needs you":  func(p *scene.Peak) { p.NeedsYou = true },
		"failing CI": func(p *scene.Peak) { p.Camps[0].State = scene.Failing },
	}
	for _, th := range scene.Themes() {
		t.Run(th.Name(), func(t *testing.T) {
			for minutes := 0; minutes < 24*60; minutes += 120 {
				for _, dark := range []bool{true, false} {
					now := time.Date(2026, 1, 15, minutes/60, minutes%60, 0, 0, time.Local)
					s := calm(th.Name())
					s.Now, s.Dark = now, dark
					box := scene.TableBox(table(8), 200, 60)
					if got := reds(th.Paint(s, 200, 60, 4, box), 200, 60); len(got) > 0 {
						t.Errorf("at %s dark=%v: red %q on a board with nothing waiting, needing you or failing", now.Format("15:04"), dark, got)
					}
					for name, f := range reserved {
						s := lone(th.Name(), f)
						s.Now, s.Dark = now, dark
						if !showsRed(th.Paint(s, 160, 48, 0, scene.Rect{}), 160, 48) {
							t.Errorf("at %s dark=%v: %s shows no red", now.Format("15:04"), dark, name)
						}
					}
				}
			}
		})
	}
}
