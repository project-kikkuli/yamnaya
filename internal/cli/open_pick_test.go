package cli

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func TestOpenPickSelectsPhysicalCopyInsteadOfTheLaunchShortcut(t *testing.T) {
	for _, pick := range []int{1, 2} {
		t.Run(fmt.Sprint(pick), func(t *testing.T) {
			base := t.TempDir()
			st, err := core.OpenStore(filepath.Join(base, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			l, err := newLauncher(base)
			if err != nil {
				t.Fatal(err)
			}
			if err := l.records.Save(launch.Record{Key: "launched", Harness: "claude", Session: "shared"}); err != nil {
				t.Fatal(err)
			}
			focused := ""
			c := &core.Core{Store: st, StateDir: base,
				Harnesses: []core.Harness{namedSessions{ids: []string{"shared", "shared"}}},
				Procs: func() ([]proc.Proc, error) {
					return []proc.Proc{
						{PID: 10, TTY: "/dev/first", Args: []string{"claude"}},
						{PID: 11, TTY: "/dev/second", Args: []string{"claude"}},
					}, nil
				},
				Front: func(tty string) error { focused = tty; return nil },
			}
			with := func(run runFunc) func(*cobra.Command, []string) error {
				return func(cmd *cobra.Command, args []string) error { return run(&env{core: c}, cmd, args) }
			}
			cmd := openCmd(with)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"claude:default@shared", "--pick", fmt.Sprint(pick)})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			want := []string{"/dev/first", "/dev/second"}[pick-1]
			if focused != want {
				t.Fatalf("picked %q instead of %q", focused, want)
			}
		})
	}
}
