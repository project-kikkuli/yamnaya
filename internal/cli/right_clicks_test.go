package cli

import (
	"testing"
	"time"
)

func TestARightClickHereAllowsOneOpenOfItsCellSoonAfter(t *testing.T) {
	type at struct{ x, y int }
	for _, tc := range []struct {
		name   string
		reads  []string
		after  time.Duration
		takes  []at
		opened int
	}{
		{"a right press", []string{"\x1b[<2;10;3M"}, 0, []at{{10, 3}}, 1},
		{"a right press split across reads", []string{"ab\x1b[<", "2;10", ";3Mcd"}, 0, []at{{10, 3}}, 1},
		{"a legacy right press", []string{"\x1b[M\"*#"}, 0, []at{{10, 3}}, 1},
		{"an open of another cell", []string{"\x1b[<2;10;3M"}, 0, []at{{11, 3}, {10, 3}}, 0},
		{"two presses count the latest", []string{"\x1b[<2;1;1M", "\x1b[<2;5;6M"}, 0, []at{{5, 6}}, 1},
		{"one press spent by the first of two opens", []string{"\x1b[<2;10;3M\x1b[<2;10;3m"}, 0, []at{{10, 3}, {10, 3}}, 1},
		{"a press older than the window", []string{"\x1b[<2;10;3M"}, clickWindow + time.Millisecond, []at{{10, 3}}, 0},
		{"a release only", []string{"\x1b[<2;10;3m"}, 0, []at{{10, 3}}, 0},
		{"a left press", []string{"\x1b[<0;10;3M"}, 0, []at{{10, 3}}, 0},
		{"a shifted right press", []string{"\x1b[<6;10;3M"}, 0, []at{{10, 3}}, 0},
		{"a drag with the right button", []string{"\x1b[<34;10;3M"}, 0, []at{{10, 3}}, 0},
		{"typed text", []string{"open https://e.x/ 2;10;3M"}, 0, []at{{10, 3}}, 0},
		{"a press inside a paste", []string{"\x1b[200~x\x1b[<2;10;3My\x1b[201~"}, 0, []at{{10, 3}}, 0},
		{"a press inside a paste split across reads", []string{"\x1b[20", "0~x\x1b[<2;10;3M", "\x1b[M\"*#", "\x1b[20", "1~"}, 0, []at{{10, 3}}, 0},
		{"a press after a paste", []string{"\x1b[200~x\x1b[201~", "\x1b[<2;10;3M"}, 0, []at{{10, 3}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			c := &rightClicks{now: func() time.Time { return now }}
			for _, r := range tc.reads {
				c.see([]byte(r))
			}
			now = now.Add(tc.after)
			opened := 0
			for _, a := range tc.takes {
				if c.take(a.x, a.y) {
					opened++
				}
			}
			if opened != tc.opened {
				t.Errorf("%d of %d opens allowed, want %d", opened, len(tc.takes), tc.opened)
			}
		})
	}
}
