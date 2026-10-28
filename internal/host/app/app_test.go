package app

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestFrontPicksTheTabShowingTheTTY(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("terminal apps are macOS only")
	}
	terminal := "63\t1\t\t/dev/ttys000\n63\t2\t\t/dev/ttys004\n"
	iterm := "7\t1\tA1\t/dev/ttys010\n7\t2\tB1\t/dev/ttys011\n7\t2\tB2\t/dev/ttys012\n"
	for _, tc := range []struct {
		name, tty, terminal, iterm string
		denied                     error
		picked                     string
		noHost, failed             bool
	}{
		{name: "Terminal second tab", tty: "/dev/ttys004", terminal: terminal, iterm: iterm, picked: "Terminal 63 2 "},
		{name: "iTerm2 split pane", tty: "/dev/ttys012", terminal: terminal, iterm: iterm, picked: "iTerm2 7 2 B2"},
		{name: "no app shows it", tty: "/dev/ttys099", terminal: terminal, iterm: iterm, noHost: true},
		{name: "no app runs", tty: "/dev/ttys004", noHost: true},
		{name: "one app shows it while another refuses", tty: "/dev/ttys004", terminal: terminal, denied: errors.New("not authorized (-1743)"), picked: "Terminal 63 2 "},
		{name: "the only app that might refuses", tty: "/dev/ttys012", terminal: terminal, denied: errors.New("not authorized (-1743)"), failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var picked []string
			a := &Apps{Script: func(program string, args ...string) (string, error) {
				for _, ap := range apps {
					switch program {
					case ap.list:
						if ap.name == "iTerm2" {
							return tc.iterm, tc.denied
						}
						return tc.terminal, nil
					case ap.pick:
						picked = append(picked, ap.name+" "+strings.Join(args, " "))
						return "", nil
					}
				}
				t.Fatalf("unexpected program %q", program)
				return "", nil
			}}
			err := a.Front(tc.tty)
			if got := strings.Join(picked, ";"); got != tc.picked {
				t.Fatalf("picked %q, want %q", got, tc.picked)
			}
			if errors.Is(err, core.ErrNoHost) != tc.noHost || (err != nil && !errors.Is(err, core.ErrNoHost)) != tc.failed {
				t.Fatalf("Front = %v, want no host %v, failed %v", err, tc.noHost, tc.failed)
			}
		})
	}
}
