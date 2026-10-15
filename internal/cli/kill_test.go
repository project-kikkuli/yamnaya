package cli

import (
	"bytes"
	"strings"
	"syscall"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

type agent struct{}

func (agent) Name() string                           { return "claude" }
func (agent) Owns(p proc.Proc) bool                  { return p.Args[0] == "claude" }
func (agent) SessionID(p proc.Proc) string           { return "06456f12-aaaa" }
func (agent) Resume(id string) []string              { return nil }
func (agent) ParsePrompt(string) (core.Prompt, bool) { return core.Prompt{}, false }
func (agent) Composer(string) (string, bool)         { return "", false }
func (agent) Discover(procs []proc.Proc) []core.Session {
	var out []core.Session
	for _, p := range procs {
		out = append(out, core.Session{Harness: "claude", ID: "06456f12-aaaa", PID: p.PID, TTY: p.TTY, Title: "Allergies rollout"})
	}
	return out
}

func TestKillAsksOnATerminalAndNeedsYesWithout(t *testing.T) {
	for _, c := range []struct {
		name   string
		yes    bool
		tty    bool
		in     string
		signal bool
		want   string
	}{
		{"no terminal, no --yes", false, false, "", false, "pass --yes"},
		{"no terminal, --yes", true, false, "", true, "killed claude 06456f12"},
		{"terminal, n", false, true, "n\n", false, "not killed"},
		{"terminal, y", false, true, "y\n", true, "kill claude 06456f12 (Allergies rollout)? y/n killed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dead := false
			var sent []int
			k := &core.Core{Harnesses: []core.Harness{agent{}},
				Procs: func() ([]proc.Proc, error) {
					if dead {
						return nil, nil
					}
					return []proc.Proc{{PID: 4242, TTY: "/dev/ttys999", Args: []string{"claude"}}}, nil
				},
				Signal: func(pid int, sig syscall.Signal) error {
					if sig == syscall.SIGTERM {
						sent, dead = append(sent, pid), true
					}
					return nil
				}}
			var out bytes.Buffer
			err := kill(k, "06456f12", c.yes, c.tty, strings.NewReader(c.in), &out)
			got := out.String()
			if err != nil {
				got += err.Error()
			}
			if !strings.Contains(got, c.want) || (len(sent) == 1) != c.signal {
				t.Fatalf("got %q, signalled %v; want %q, signal %v", got, sent, c.want, c.signal)
			}
		})
	}
}
