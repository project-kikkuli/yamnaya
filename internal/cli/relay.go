package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func relayCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "relay",
		Short: "List what this host still has to carry to other hosts: asks, messages and commands for managers and chiefs there",
		Args:  cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			pending, err := e.core.Store.PendingRelays()
			if err != nil {
				return err
			}
			self, err := e.core.Store.RelaySelf()
			if err != nil {
				return err
			}
			if self.Courier != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s carries this host's relays as %s; last at %s\n", self.Courier, self.Name, self.At.Local().Format(time.DateTime))
			}
			if len(pending) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "nothing waiting")
			}
			for _, r := range pending {
				fmt.Fprintf(cmd.OutOrStdout(), "%-5d to %-20s %-7s since %s  %s\n", r.Seq, r.Dest, r.Kind, r.Created.Local().Format(time.DateTime), oneLine(r.Body))
				if r.Error != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "      refused there: %s; sent again each cycle, ym relay drop %d stops it\n", oneLine(r.Error), r.Seq)
				}
			}
			return nil
		}),
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "drop <seq>",
		Short: "Stop carrying a relay another host keeps refusing",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			seq, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("%q is not a relay number from ym relay", args[0])
			}
			ok, err := e.core.Store.DropRelay(seq)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no relay %d waits to be carried", seq)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "dropped relay %d\n", seq)
			return nil
		}),
	}, &cobra.Command{
		Use:    "sync",
		Short:  "Trade relays with the host that reaches this one: read its request on stdin, print what this host holds",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if err := ensureWorkService(); err != nil {
				return err
			}
			var req core.RelaySync
			if err := json.NewDecoder(cmd.InOrStdin()).Decode(&req); err != nil {
				return fmt.Errorf("relay sync request: %w", err)
			}
			reply, err := e.core.ServeSync(req)
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(reply)
		}),
	})
	return cmd
}

// relayHosts carries relays between this host and every host ym remote reached.
func relayHosts(c *core.Core) error {
	hosts, err := c.Store.KnownHosts()
	if err != nil || len(hosts) == 0 {
		return err
	}
	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = h.Name
	}
	return c.SyncRelays(names, newRemoteHosts(names, 0, nil))
}

// runSelf runs this executable with args, as a relay that asks for a ym command does.
func runSelf(args ...string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	out, err := exec.Command(self, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ym %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
