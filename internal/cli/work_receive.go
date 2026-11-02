package cli

import (
	"encoding/json"
	"fmt"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/spf13/cobra"
)

func workReceiveCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{Use: "work-receive <message>", Hidden: true, Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		var m core.WorkMessage
		if err := json.Unmarshal([]byte(args[0]), &m); err != nil {
			return err
		}
		if err := e.core.WorkReceive(m); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "remote work recorded")
		return nil
	})}
}
