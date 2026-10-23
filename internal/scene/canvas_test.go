package scene

import "testing"

func TestMountainsRiseTowardTheFrameAtMostNCellsAStep(t *testing.T) {
	profile, reducedMotion = trueColor, false
	for _, size := range []struct{ w, h int }{{80, 24}, {197, 58}} {
		for _, n := range []int{1, 38, 400, size.w * size.h} {
			s, table := work(), board(12, 0)
			goal := Paint(s, size.w, size.h, 9, table)
			c, left := Blank(size.w, size.h).Toward(goal, 0)
			total := left
			for steps := 0; left > 0; steps++ {
				if steps > total {
					t.Fatalf("%dx%d n=%d: still %d cells to go after %d steps", size.w, size.h, n, left, steps)
				}
				next, rest := c.Toward(goal, n)
				changed, low := 0, size.h
				for i := range next.style {
					x, y := i%size.w, i/size.w
					if goal.box.has(x, y) || (next.style[i] == c.style[i] && next.glyph[i] == c.glyph[i]) {
						continue
					}
					if next.glyph[i] != "" {
						changed++
					}
					low = min(low, y)
				}
				for i := range next.style {
					x, y := i%size.w, i/size.w
					if y > low && !goal.box.has(x, y) && (next.style[i] != goal.style[i] || next.glyph[i] != goal.glyph[i]) {
						t.Fatalf("%dx%d n=%d: row %d changed while row %d below it was unfinished", size.w, size.h, n, low, y)
					}
				}
				if changed > n || left-rest > n {
					t.Fatalf("%dx%d n=%d: one step changed %d cells", size.w, size.h, n, changed)
				}
				c, left = next, rest
			}
			if got, want := c.Draw(table), Frame(s, size.w, size.h, 9, table); got != want {
				t.Fatalf("%dx%d n=%d: risen mountains differ from the frame", size.w, size.h, n)
			}
		}
	}
}
