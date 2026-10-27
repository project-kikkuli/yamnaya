package themes

import (
	"os"
	"regexp"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/agenttest"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func TestMain(m *testing.M) {
	scene.Colors, scene.ReducedMotion = scene.TrueColor, false
	os.Exit(agenttest.Run(m))
}

func TestEveryThemeHasAOneWordNameAndExplainsEverySignalOnce(t *testing.T) {
	for _, th := range scene.Themes() {
		t.Run(th.Name(), func(t *testing.T) {
			if !regexp.MustCompile(`^[a-z]+$`).MatchString(th.Name()) {
				t.Errorf("name %q is not one lowercase word", th.Name())
			}
			for _, dark := range []bool{true, false} {
				seen := map[scene.Signal]int{}
				for _, e := range th.Legend(scene.Scene{Dark: dark}).Entries {
					seen[e.Signal]++
					if e.Look == "" {
						t.Errorf("%s has no look", e.Signal)
					}
					if e.Color != "" && !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(e.Color) {
						t.Errorf("%s color %q is not #rrggbb", e.Signal, e.Color)
					}
				}
				for _, g := range scene.Signals() {
					if seen[g] != 1 {
						t.Errorf("dark=%v: %s explained %d times, want once", dark, g, seen[g])
					}
				}
			}
		})
	}
}

func TestEveryThemeFillsTheScreenTheSameWayEachTimeAndLeavesTheTableAlone(t *testing.T) {
	for _, th := range scene.Themes() {
		for _, size := range []struct{ w, h int }{{72, 24}, {120, 40}, {200, 60}, {260, 70}} {
			for _, s := range []scene.Scene{busy(th.Name(), 0), busy(th.Name(), .6), {Theme: th.Name()}} {
				box := scene.TableBox(table(8), size.w, size.h)
				c := th.Paint(s, size.w, size.h, 5, box)
				if !c.Fits(size.w, size.h) {
					t.Fatalf("%s %dx%d: canvas is not the screen's size", th.Name(), size.w, size.h)
				}
				if c.Box() != box {
					t.Fatalf("%s: canvas box %v, want %v", th.Name(), c.Box(), box)
				}
				for y := box.Y0; y < box.Y1; y++ {
					for x := box.X0; x < box.X1; x++ {
						if style, glyph := c.Cell(x, y); style != scene.Reset || glyph != " " {
							t.Fatalf("%s %dx%d: painted %q under the table at %d,%d", th.Name(), size.w, size.h, glyph, x, y)
						}
					}
				}
				if c.Draw(table(8)) != th.Paint(s, size.w, size.h, 5, box).Draw(table(8)) {
					t.Fatalf("%s %dx%d: the same frame drew two pictures", th.Name(), size.w, size.h)
				}
			}
		}
	}
}

// lone is one unit alone, changed by f, so each signal can be seen to change the picture.
func lone(theme string, f func(*scene.Peak)) scene.Scene {
	p := scene.Peak{ID: "u", Name: "the unit", Weather: scene.Clear, Progress: .5,
		Team:  []scene.Climber{{Name: "worker", Stance: scene.Climbing}},
		Camps: []scene.Camp{{Label: "#1", State: scene.Open, Stage: 3}}}
	f(&p)
	return scene.Scene{Theme: theme, Dark: true, Ranges: []scene.Range{{ID: "repo:r", Name: "repo", Peaks: []scene.Peak{{ID: "left", Name: "left"}, p, {ID: "right", Name: "right"}}}}}
}

func TestEverySignalChangesThePicture(t *testing.T) {
	signals := map[string]func(*scene.Peak){
		"waiting":     func(p *scene.Peak) { p.Team[0].Stance = scene.Waiting },
		"resting":     func(p *scene.Peak) { p.Team[0].Stance = scene.Resting },
		"more agents": func(p *scene.Peak) { p.Team = append(p.Team, scene.Climber{Name: "second", Stance: scene.Climbing}) },
		"pr green":    func(p *scene.Peak) { p.Camps[0].State = scene.Green },
		"pr red":      func(p *scene.Peak) { p.Camps[0].State = scene.Failing },
		"pr merged":   func(p *scene.Peak) { p.Camps[0].State, p.Camps[0].Stage = scene.Merged, 6 },
		"pr stage":    func(p *scene.Peak) { p.Camps[0].Stage = 5 },
		"progress":    func(p *scene.Peak) { p.Progress = .9 },
		"needs you":   func(p *scene.Peak) { p.NeedsYou = true },
		"clouds":      func(p *scene.Peak) { p.Weather = scene.Clouds },
		"storm":       func(p *scene.Peak) { p.Weather = scene.Storm },
		"eruption":    func(p *scene.Peak) { p.Weather = scene.Eruption },
		"summit":      func(p *scene.Peak) { p.Weather = scene.Summit },
		"kill":        func(p *scene.Peak) { p.Dying = .5 },
	}
	for _, th := range scene.Themes() {
		base := th.Paint(lone(th.Name(), func(*scene.Peak) {}), 160, 48, 0, scene.Rect{}).Draw("")
		for name, f := range signals {
			if th.Paint(lone(th.Name(), f), 160, 48, 0, scene.Rect{}).Draw("") == base {
				t.Errorf("%s: %s looks the same as without it", th.Name(), name)
			}
		}
		s := lone(th.Name(), func(*scene.Peak) {})
		s.Selected = "u"
		if th.Paint(s, 160, 48, 0, scene.Rect{}).Draw("") == base {
			t.Errorf("%s: selecting the unit changes nothing", th.Name())
		}
	}
}

func TestEveryThemeHitsEachUnitSomewhere(t *testing.T) {
	for _, th := range scene.Themes() {
		s := lone(th.Name(), func(*scene.Peak) {})
		found := map[string]bool{}
		for y := range 48 {
			for x := range 160 {
				found[th.Hit(s, 160, 48, scene.Rect{}, x, y)] = true
			}
		}
		for _, id := range []string{"left", "u", "right"} {
			if !found[id] {
				t.Errorf("%s: no cell hits %s", th.Name(), id)
			}
		}
	}
}
