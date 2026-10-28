package themes

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// seen is every color a cell shows: the background of a space, the foreground of a full block, both halves of a
// half block, and a glyph on its background. A wide glyph's second cell shows nothing of its own.
func seen(c *scene.Canvas, x, y int) []scene.RGB {
	fg, bg, ok := c.Ink(x, y)
	if !ok {
		return nil
	}
	switch _, glyph := c.Cell(x, y); glyph {
	case "":
		return nil
	case " ":
		return []scene.RGB{bg}
	case "█":
		return []scene.RGB{fg}
	}
	return []scene.RGB{fg, bg}
}

// reds is every color a canvas shows that is red or near it, glyph or pixel, with where and in what it is drawn,
// and how many colors it shows in all.
func reds(c *scene.Canvas, w, h int) (found []string, shown int) {
	for y := range h {
		for x := range w {
			for _, ink := range seen(c, x, y) {
				shown++
				if scene.NearRed(ink) {
					_, glyph := c.Cell(x, y)
					found = append(found, fmt.Sprintf("%q %s at %d,%d", glyph, ink.Hex(), x, y))
				}
			}
		}
	}
	return found, shown
}

// sample is the first few of found and how many there are.
func sample(found []string) string {
	return fmt.Sprintf("%d, e.g. %s", len(found), strings.Join(found[:min(4, len(found))], ", "))
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
			fail := failer(t, th.Name())
			for minutes := 0; minutes < 24*60; minutes += 120 {
				for _, dark := range []bool{true, false} {
					now := time.Date(2026, 1, 15, minutes/60, minutes%60, 0, 0, time.Local)
					s := calm(th.Name())
					s.Now, s.Dark = now, dark
					box := scene.TableBox(table(8), 200, 60)
					if got, _ := reds(th.Paint(s, 200, 60, 4, box), 200, 60); len(got) > 0 {
						fail("at %s dark=%v: red or near-red colors on a board with nothing waiting, needing you or failing: %s",
							now.Format("15:04"), dark, sample(got))
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

// redShare is the most of the colors on the busy board that may be red or near it where no waiting agent, unit
// needing the owner or failing pull request is drawn: a stray anti-aliased cell, never a fire or a wash.
const redShare = .002

func TestRedOnABusyBoardIsOnlyItsSignals(t *testing.T) {
	for _, th := range scene.Themes() {
		t.Run(th.Name(), func(t *testing.T) {
			fail := failer(t, th.Name())
			for _, size := range labelSizes {
				box := scene.TableBox(table(8), size.w, size.h)
				c := th.Paint(busy(th.Name(), .5), size.w, size.h, 4, box)
				quiet := th.Paint(calm(th.Name()), size.w, size.h, 4, box)
				var found []string
				shown, signal := 0, 0
				for y := range size.h {
					for x := range size.w {
						st, g := c.Cell(x, y)
						qs, qg := quiet.Cell(x, y)
						for _, ink := range seen(c, x, y) {
							shown++
							switch {
							case !scene.NearRed(ink):
							case st != qs || g != qg:
								signal++
							default:
								found = append(found, fmt.Sprintf("%q %s at %d,%d", g, ink.Hex(), x, y))
							}
						}
					}
				}
				t.Logf("%dx%d: signals %.1f%% red, the rest %.2f%%", size.w, size.h, float64(signal)*100/float64(shown), float64(len(found))*100/float64(shown))
				if share := float64(len(found)) / float64(shown); share > redShare {
					fail("%dx%d: %.1f%% of the busy board is red or near it where no signal is drawn, want at most %.1f%%: %s",
						size.w, size.h, share*100, redShare*100, sample(found))
				}
			}
		})
	}
}
