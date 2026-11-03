package scene

import "testing"

func TestARecycledCanvasLeavesNothingBehind(t *testing.T) {
	old := Colors
	Colors = TrueColor
	t.Cleanup(func() { Colors = old })
	dirty := func() *Canvas {
		c := NewCanvas(20, 10, Rect{X0: 1, Y0: 1, X1: 3, Y1: 3})
		for y := range 10 {
			for x := range 20 {
				c.Set(x, y, White, Black, "x")
			}
		}
		c.Spill(5, 5)
		return c
	}
	sizes := map[string]struct{ w, h int }{"same": {20, 10}, "smaller": {8, 4}, "larger": {40, 20}}
	for name, size := range sizes {
		t.Run(name, func(t *testing.T) {
			want := &Canvas{w: size.w, h: size.h, ink: make([]ink, size.w*size.h), glyph: make([]string, size.w*size.h)}
			for i := range want.glyph {
				want.glyph[i] = " "
			}
			dirty().Recycle()
			c := Blank(size.w, size.h)
			if c.Draw("") != want.Draw("") || c.Box() != (Rect{}) {
				t.Fatal("a blank canvas shows what a recycled one held")
			}
			goal := NewCanvas(size.w, size.h, Rect{X0: 2, Y0: 2, X1: 4, Y1: 4})
			goal.Set(0, size.h-1, White, Black, "g")
			dirty().Recycle()
			next, left := want.Toward(goal, size.w*size.h)
			if left != 0 || next.Draw("") != goal.Draw("") || next.Box() != goal.Box() {
				t.Fatal("a step toward the goal shows what a recycled canvas held")
			}
		})
	}
}
