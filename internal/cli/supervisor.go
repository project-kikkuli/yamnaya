package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/spf13/cobra"
)

func supervisorOwner(e *env) error {
	author, err := e.author()
	if err != nil {
		return err
	}
	if author != core.Owner {
		return errors.New("only the owner changes supervisor identities, context or attachments")
	}
	return nil
}

func (e *env) supervisorCaller() (core.Caller, error) {
	if e.session != "" {
		return core.Caller{}, errors.New("supervisor memory and reports come from their native session, without --session")
	}
	return e.core.Calling(os.Getpid(), os.Getenv)
}

func supervisorCmd(with, hosts func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var intent, over, profile string
	var codex bool
	cmd := &cobra.Command{
		Use:   "supervisor <name> --intent TEXT [--over ID,HOST/ID]",
		Short: "Start a local supervisor that retains intent and inspects attached work independently",
		Args:  cobra.ExactArgs(1),
		RunE: hosts(func(e *env, cmd *cobra.Command, args []string) error {
			if err := supervisorOwner(e); err != nil {
				return err
			}
			var targets []string
			if over != "" {
				targets = strings.Split(over, ",")
			}
			m, err := e.core.NewSupervisor(args[0], intent, harnessFlag(codex), profile, targets)
			if err != nil {
				return err
			}
			if err := startService(); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "supervisor recorded; service failed to start: %v\n", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "supervisor %s (%s): %s\n", m.Shown(), core.Short(m.Session), m.BriefPath)
			if m.LastError != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "launch deferred: %s; ym serve retries\n", m.LastError)
			}
			return nil
		}),
	}
	cmd.Flags().StringVar(&intent, "intent", "", "the owner's original intent and authorization limits")
	cmd.Flags().StringVar(&over, "over", "", "independent attachments, comma separated; does not change reporting lines")
	cmd.Flags().StringVar(&profile, "profile", "", "login profile")
	cmd.Flags().BoolVar(&codex, "codex", false, "use codex")
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List local supervisor identities", Args: cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			o, err := e.core.LoadOrg()
			if err != nil {
				return err
			}
			for _, m := range o.Ordered() {
				if m.Role == core.RoleSupervisor {
					targets, err := e.core.Store.SupervisorTargets(m.ID)
					if err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", m.Shown(), core.Short(m.Session), strings.Join(targets, ","))
				}
			}
			return nil
		}),
	}, &cobra.Command{
		Use: "show <supervisor>", Short: "Read original intent, all corrections, reports and current attachment evidence as JSON", Args: cobra.ExactArgs(1),
		RunE: hosts(func(e *env, cmd *cobra.Command, args []string) error {
			view, err := e.core.Supervisor(args[0])
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(view)
		}),
	})
	for _, detach := range []bool{false, true} {
		name := "attach"
		if detach {
			name = "detach"
		}
		cmd.AddCommand(&cobra.Command{
			Use: name + " <supervisor> <manager|chief|host/id>", Short: name + " an independent supervision relation", Args: cobra.ExactArgs(2),
			RunE: hosts(func(e *env, cmd *cobra.Command, args []string) error {
				if err := supervisorOwner(e); err != nil {
					return err
				}
				return e.core.SupervisorAttach(args[0], args[1], detach)
			}),
		})
	}
	var correction string
	context := &cobra.Command{
		Use: "context <supervisor> --text TEXT", Short: "Append an owner correction to durable supervisor context", Args: cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if err := supervisorOwner(e); err != nil {
				return err
			}
			view, err := e.core.Supervisor(args[0])
			if err != nil {
				return err
			}
			if strings.TrimSpace(correction) == "" {
				return errors.New("a context correction needs text")
			}
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			st, err := e.core.SteerContext(b, view.Supervisor.ID, view.Context+"\n\nOwner correction:\n"+correction, core.Owner)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s context revision %d\n", view.Supervisor.Shown(), st.Revision)
			return nil
		}),
	}
	context.Flags().StringVar(&correction, "text", "", "the owner's correction, added to existing context")
	cmd.AddCommand(context)
	var assessment, uncertainty string
	var sources []string
	report := &cobra.Command{Use: "report <supervisor> --assessment TEXT --uncertainty TEXT --evidence SOURCE", Short: "Record an attributed assessment with primary evidence and its observed snapshot", Args: cobra.ExactArgs(1), RunE: hosts(func(e *env, cmd *cobra.Command, args []string) error {
		caller, err := e.supervisorCaller()
		if err != nil {
			return err
		}
		report, err := e.core.ReportSupervision(caller.Session, args[0], assessment, uncertainty, sources)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	})}
	report.Flags().StringVar(&assessment, "assessment", "", "alignment findings grounded in evidence")
	report.Flags().StringVar(&uncertainty, "uncertainty", "", "missing evidence or none")
	report.Flags().StringArrayVar(&sources, "evidence", nil, "primary evidence reference; repeat for each source")
	cmd.AddCommand(report)
	var remembered, source string
	remember := &cobra.Command{Use: "remember <supervisor> --text TEXT --source REFERENCE", Short: "Preserve a received correction with supervisor attribution and provenance", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		caller, err := e.supervisorCaller()
		if err != nil {
			return err
		}
		st, err := e.core.RememberSupervision(caller.Session, args[0], remembered, source)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "supervisor context revision %d\n", st.Revision)
		return nil
	})}
	remember.Flags().StringVar(&remembered, "text", "", "received correction; does not grant authority")
	remember.Flags().StringVar(&source, "source", "", "conversation or message reference for the correction")
	cmd.AddCommand(remember)
	return cmd
}
