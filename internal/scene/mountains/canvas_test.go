package mountains

import (
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func TestMountainsRiseTowardTheFrameAtMostNCellsAStep(t *testing.T) {
	scene.Colors, scene.ReducedMotion = scene.TrueColor, false
	for _, size := range []struct{ w, h int }{{80, 24}, {197, 58}} {
		for _, n := range []int{1, 38, 400, size.w * size.h} {
			s, table := work(), board(12, 0)
			goal := scene.Paint(s, size.w, size.h, 9, table)
			c, left := scene.Blank(size.w, size.h).Toward(goal, 0)
			total := left
			for steps := 0; left > 0; steps++ {
				if steps > total {
					t.Fatalf("%dx%d n=%d: still %d cells to go after %d steps", size.w, size.h, n, left, steps)
				}
				next, rest := c.Toward(goal, n)
				changed, low := 0, size.h
				for i := range size.w * size.h {
					x, y := i%size.w, i/size.w
					if goal.Box().Has(x, y) || same(next, c, x, y) {
						continue
					}
					if _, g := next.Cell(x, y); g != "" {
						changed++
					}
					low = min(low, y)
				}
				for i := range size.w * size.h {
					x, y := i%size.w, i/size.w
					if y > low && !goal.Box().Has(x, y) && !same(next, goal, x, y) {
						t.Fatalf("%dx%d n=%d: row %d changed while row %d below it was unfinished", size.w, size.h, n, low, y)
					}
				}
				if changed > n || left-rest > n {
					t.Fatalf("%dx%d n=%d: one step changed %d cells", size.w, size.h, n, changed)
				}
				c, left = next, rest
			}
			if got, want := c.Draw(table), scene.Frame(s, size.w, size.h, 9, table); got != want {
				t.Fatalf("%dx%d n=%d: risen mountains differ from the frame", size.w, size.h, n)
			}
		}
	}
}

func same(a, b *scene.Canvas, x, y int) bool {
	as, ag := a.Cell(x, y)
	bs, bg := b.Cell(x, y)
	return as == bs && ag == bg
}
