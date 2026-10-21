package cli

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/project-kikkuli/yamnaya/internal/work"
	"github.com/spf13/cobra"
)

func agentBatchCommands(with func(runFunc) func(*cobra.Command, []string) error) []*cobra.Command {
	var recipients []string
	var text, id string
	send := &cobra.Command{Use: "broadcast", Short: "Send one durable message to selected managers", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		var agents []work.Agent
		for _, ref := range recipients {
			a, err := e.core.Store.Work.Agent(ref)
			if err != nil {
				return err
			}
			agents = append(agents, a)
		}
		if id == "" {
			id = uuid.NewString()
		}
		batch, err := e.core.Store.Work.SendAgentBatch(id, e.actor(), text, agents)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(batch)
	})}
	send.Flags().StringArrayVar(&recipients, "to", nil, "recipient agent identity or unambiguous name; repeat for each manager")
	send.Flags().StringVar(&text, "text", "", "message text")
	send.Flags().StringVar(&id, "id", "", "stable message identity for idempotent submission")
	list := &cobra.Command{Use: "batches [id]", Short: "Read per-manager message acceptance, delivery and incorporation", Args: cobra.MaximumNArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			b, err := e.core.Store.Work.AgentBatch(args[0])
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(b)
		}
		b, err := e.core.Store.Work.AgentBatches()
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(b)
	})}
	retry := &cobra.Command{Use: "retry-batch <id>", Short: "Retry only failed recipients after resolving the reported cause", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		if err := e.core.Store.Work.DispatchAgentBatch(args[0], true); err != nil {
			return err
		}
		b, err := e.core.Store.Work.AgentBatch(args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(b)
	})}
	return []*cobra.Command{send, list, retry}
}
