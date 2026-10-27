package cli

import (
	"testing"
	"time"
)

func TestARightClickHereAllowsOneOpenSoonAfter(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reads  []string
		after  time.Duration
		takes  int
		opened int
	}{
		{"a right press", []string{"\x1b[<2;10;3M"}, 0, 1, 1},
		{"a right press split across reads", []string{"ab\x1b[<", "2;10", ";3Mcd"}, 0, 1, 1},
		{"a legacy right press", []string{"\x1b[M\"*#"}, 0, 1, 1},
		{"one press spent by the first of two opens", []string{"\x1b[<2;10;3M\x1b[<2;10;3m"}, 0, 2, 1},
		{"two presses", []string{"\x1b[<2;1;1M", "\x1b[<2;1;1M"}, 0, 1, 1},
		{"a press older than the window", []string{"\x1b[<2;10;3M"}, clickWindow + time.Millisecond, 1, 0},
		{"a release only", []string{"\x1b[<2;10;3m"}, 0, 1, 0},
		{"a left press", []string{"\x1b[<0;10;3M"}, 0, 1, 0},
		{"a shifted right press", []string{"\x1b[<6;10;3M"}, 0, 1, 0},
		{"a drag with the right button", []string{"\x1b[<34;10;3M"}, 0, 1, 0},
		{"typed text", []string{"open https://e.x/ 2;10;3M"}, 0, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			c := &rightClicks{now: func() time.Time { return now }}
			for _, r := range tc.reads {
				c.see([]byte(r))
			}
			now = now.Add(tc.after)
			opened := 0
			for range tc.takes {
				if c.take() {
					opened++
				}
			}
			if opened != tc.opened {
				t.Errorf("%d of %d opens allowed, want %d", opened, tc.takes, tc.opened)
			}
		})
	}
}
