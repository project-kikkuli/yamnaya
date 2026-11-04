package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/spf13/cobra"
)

func TestReadingAnEventIsAHandoverOnlyForItsGoalsManager(t *testing.T) {
	for _, tc := range []struct {
		name      string
		session   func(manager string) string
		args      func(goal string, seq int64) []string
		delivered bool
	}{
		{"its manager reads the feed", func(m string) string { return m }, func(g string, _ int64) []string { return []string{"feed", g} }, true},
		{"its manager reads the event", func(m string) string { return m }, func(_ string, n int64) []string { return []string{"event", fmt.Sprint(n)} }, true},
		{"nobody's session reads the feed", func(string) string { return "" }, func(g string, _ int64) []string { return []string{"feed", g} }, false},
		{"nobody's session reads the event", func(string) string { return "" }, func(_ string, n int64) []string { return []string{"event", fmt.Sprint(n)} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			k := &core.Core{Store: st, Procs: func() ([]proc.Proc, error) {
				return []proc.Proc{{PID: os.Getpid(), PPID: 1, Args: []string{"sh"}}}, nil
			}}
			const manager = "manager-session"
			g, err := st.AddGoal(core.Goal{Ask: "implement CSV"})
			if err != nil {
				t.Fatal(err)
			}
			m, err := st.AddManager(core.Manager{Role: core.RoleManager, GoalID: g.ID, Session: manager})
			if err != nil {
				t.Fatal(err)
			}
			if err = st.SaveManager(m); err != nil {
				t.Fatal(err)
			}
			g.ManagerID = m.ID
			if err = st.SaveGoal(g); err != nil {
				t.Fatal(err)
			}
			e, _, err := st.AddGoalEvent(core.GoalEvent{Goal: g.ID, Kind: core.EventCIGreen, Payload: "CI passed", Created: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			with := func(run runFunc) func(*cobra.Command, []string) error {
				return func(cmd *cobra.Command, args []string) error {
					return run(&env{core: k, session: tc.session(manager)}, cmd, args)
				}
			}
			cmd := goalCmd(with, with)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(tc.args(g.ID, e.Seq))
			if err := cmd.Execute(); err != nil {
				t.Fatalf("%v: %s", err, out.String())
			}
			got, err := st.GoalEvent(e.Seq)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Delivered.IsZero() != tc.delivered {
				t.Fatalf("delivered at %v, want handed over=%v", got.Delivered, tc.delivered)
			}
		})
	}
}
