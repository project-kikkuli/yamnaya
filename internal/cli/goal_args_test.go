package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestGoalStartsOnlyFromOneQuotedAsk(t *testing.T) {
	for _, c := range []struct {
		name    string
		args    []string
		started bool
		err     string
	}{
		{"quoted ask", []string{"Add a -min flag to tally"}, true, ""},
		{"a subcommand this ym lacks", []string{"status", "Bigrams implementer dispatched; PR next"}, false, `no subcommand "status"`},
		{"another one", []string{"snooze", "20m", "check the implementer"}, false, `no subcommand "snooze"`},
		{"no ask", nil, false, "needs the ask"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			with := func(run runFunc) func(*cobra.Command, []string) error {
				return func(cmd *cobra.Command, args []string) error {
					if cmd.Name() == "goal" {
						got = args
					}
					return nil
				}
			}
			cmd := goalCmd(with, with)
			cmd.SetArgs(c.args)
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			err := cmd.Execute()
			if c.started != (got != nil) || (c.started && got[0] != c.args[0]) {
				t.Fatalf("ym goal %q started %q", c.args, got)
			}
			if (c.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.err)) {
				t.Fatalf("ym goal %q: error %v, want %q", c.args, err, c.err)
			}
		})
	}
}
