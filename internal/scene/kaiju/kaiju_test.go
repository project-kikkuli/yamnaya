package kaiju

import (
	"strconv"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func lone(f func(*scene.Peak)) scene.Scene {
	p := scene.Peak{ID: "u", Name: "the unit", Progress: .5, Team: []scene.Climber{{Name: "a"}}, Camps: []scene.Camp{{Label: "#1", Stage: 2}}}
	f(&p)
	return scene.Scene{Ranges: []scene.Range{{ID: "repo:r", Name: "repo", Peaks: []scene.Peak{{ID: "left", Name: "left"}, p}}}}
}

func find(t *testing.T, s scene.Scene) monster {
	t.Helper()
	for _, m := range plan(s, 160, 48).monsters {
		if m.id == "u" {
			return m
		}
	}
	t.Fatal("no monster u")
	return monster{}
}

func TestAMonsterWadesInlandAsItsWorkGetsDone(t *testing.T) {
	var foot, water float64
	for i, progress := range []float64{0, .25, .5, .75, 1} {
		m := find(t, lone(func(p *scene.Peak) { p.Progress = progress }))
		if i > 0 && (m.foot >= foot || m.foot-m.water > foot-water) {
			t.Errorf("at %v%%: stands at %v with %v under water, was %v with %v", progress*100, m.foot, m.foot-m.water, foot, foot-water)
		}
		foot, water = m.foot, m.water
	}
}

func TestAMonsterKeepsItsPlaceWhateverHappensToIt(t *testing.T) {
	base := find(t, lone(func(*scene.Peak) {}))
	for name, f := range map[string]func(*scene.Peak){
		"waiting":   func(p *scene.Peak) { p.Team[0].Stance = scene.Waiting },
		"needs you": func(p *scene.Peak) { p.NeedsYou = true },
		"storm":     func(p *scene.Peak) { p.Weather = scene.Storm },
		"renamed":   func(p *scene.Peak) { p.Name = "another name" },
		"dying":     func(p *scene.Peak) { p.Dying = .7 },
	} {
		if m := find(t, lone(f)); m.x != base.x || m.foot != base.foot {
			t.Errorf("%s: moved from %v,%v to %v,%v", name, base.x, base.foot, m.x, m.foot)
		}
	}
}

func TestEveryOpenCellShowsTheColorsOfItsPixels(t *testing.T) {
	old := scene.Colors
	scene.Colors = scene.TrueColor
	t.Cleanup(func() { scene.Colors = old })
	for _, weather := range []scene.Weather{scene.Clear, scene.Storm, scene.Eruption} {
		t.Run(strconv.Itoa(int(weather)), func(t *testing.T) {
			s := lone(func(p *scene.Peak) { p.Weather = weather })
			w, h := 160, 48
			p := still(s, w, h).at(5, scene.Rect{})
			defer p.release()
			p.draw()
			if weather == scene.Eruption && len(p.tint) == 0 {
				t.Fatal("an eruption tinted nothing")
			}
			for y := range h {
				for x := range w {
					_, glyph := p.c.Cell(x, y)
					if p.taken[y*w+x] || (glyph != " " && glyph != "▀") {
						continue
					}
					fg, bg, _ := p.c.Ink(x, y)
					top, bottom := scene.Code(p.px[2*y*w+x]), scene.Code(p.px[(2*y+1)*w+x])
					if scene.Code(fg) != top || scene.Code(bg) != bottom {
						t.Fatalf("cell %d,%d shows %06x/%06x over pixels %06x/%06x", x, y, scene.Code(fg), scene.Code(bg), top, bottom)
					}
				}
			}
		})
	}
}

func changedCells(a, b *scene.Canvas, w, h int) int {
	n := 0
	for y := range h {
		for x := range w {
			as, ag := a.Cell(x, y)
			bs, bg := b.Cell(x, y)
			if as != bs || ag != bg {
				n++
			}
		}
	}
	return n
}

func TestThePrintHoldsBetweenBeatsAndStillMoves(t *testing.T) {
	old := scene.Colors
	scene.Colors = scene.TrueColor
	t.Cleanup(func() { scene.Colors = old })
	s := lone(func(*scene.Peak) {})
	w, h := 160, 48
	paint := func(frame int) *scene.Canvas { return Kaiju{}.Paint(s, w, h, frame, scene.Rect{}) }
	held, moved := 0, 0
	for frame := 1; frame < grainBeat; frame++ {
		n := changedCells(paint(frame-1), paint(frame), w, h)
		held = max(held, n)
		if n > 0 {
			moved++
		}
	}
	if moved < grainBeat-2 {
		t.Fatalf("only %d of %d frames between beats moved: the film went still", moved, grainBeat-1)
	}
	if held > w*h/4 {
		t.Fatalf("%d of %d cells redrew between beats", held, w*h)
	}
	if beat := changedCells(paint(grainBeat-1), paint(grainBeat), w, h); beat <= held {
		t.Fatalf("the beat redrew %d cells, no more than the %d between beats", beat, held)
	}
}
