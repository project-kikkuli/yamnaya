package cli

import (
	"fmt"
	"github.com/spf13/cobra"
)

func goalResumeCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var source, reason string
	cmd := &cobra.Command{Use: "resume --source ID --reason TEXT", Short: "Withdraw completion for authorized work received directly in chat", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		caller, err := e.caller()
		if err != nil {
			return err
		}
		if err = e.core.ResumeGoal(caller.Session, source, reason); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "work registered; completion withdrawn")
		return nil
	})}
	cmd.Flags().StringVar(&source, "source", "", "stable work authorization identity; reuse when replaying")
	cmd.Flags().StringVar(&reason, "reason", "", "authorized work that remains")
	return cmd
}

func goalAssignCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var source, reason string
	var planned bool
	cmd := &cobra.Command{Use: "assign <worker> --source ID --reason TEXT", Short: "Register a new assignment generation before sending work to an existing worker", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		caller, err := e.caller()
		if err != nil {
			return err
		}
		worker := args[0]
		if !planned {
			resolved, err := e.core.CallerOf(worker)
			if err != nil {
				return err
			}
			worker = resolved.Session
		}
		a, err := e.core.AssignWork(caller.Session, worker, source, reason)
		if err != nil {
			return err
		}
		if a.ID == "" {
			return fmt.Errorf("caller belongs to no goal")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "assignment %s generation %d; include this id in the worker's terminal report command\n", a.ID, a.Generation)
		return nil
	})}
	cmd.Flags().BoolVar(&planned, "planned", false, "register a planned worker token before native launch; bind its session afterward")
	cmd.Flags().StringVar(&source, "source", "", "stable assignment identity; reuse only for replay")
	cmd.Flags().StringVar(&reason, "reason", "", "work to assign")
	return cmd
}

func goalAssignmentsCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{Use: "assignments [goal]", Short: "List explicit assignments and their terminal report state", Args: cobra.MaximumNArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		ref := ""
		if len(args) > 0 {
			ref = args[0]
		} else {
			caller, err := e.caller()
			if err != nil {
				return err
			}
			org, err := e.core.LoadOrg()
			if err != nil {
				return err
			}
			m, ok := org.ManagerOf(caller.Session)
			if !ok {
				return fmt.Errorf("caller is not a manager")
			}
			ref = m.GoalID
		}
		g, err := e.core.GoalByRef(ref)
		if err != nil {
			return err
		}
		assignments, err := e.core.Store.Assignments(g.ID)
		if err != nil {
			return err
		}
		for _, a := range assignments {
			fmt.Fprintf(cmd.OutOrStdout(), "%s generation %d %s worker=%s report=%d accepted=%d %s\n", a.ID, a.Generation, a.State, a.Session, a.Report, a.AcceptedReport, a.Reason)
		}
		return nil
	})}
}

func goalReportCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var summary string
	cmd := &cobra.Command{Use: "report <assignment> --summary TEXT", Short: "Submit a terminal assignment report for its manager to accept", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		caller, err := e.caller()
		if err != nil {
			return err
		}
		report, err := e.core.ReportAssignment(caller.Session, args[0], summary)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "terminal report #%d; manager: ym goal accept %s --report %d\n", report, args[0], report)
		return nil
	})}
	cmd.Flags().StringVar(&summary, "summary", "", "terminal result, with commit/PR/evidence or failure details")
	return cmd
}

func goalAcceptCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var report int64
	cmd := &cobra.Command{Use: "accept <assignment> --report EVENT", Short: "Accept a terminal report for this assignment generation", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		caller, err := e.caller()
		if err != nil {
			return err
		}
		if err = e.core.AcceptAssignment(caller.Session, args[0], report); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "terminal report accepted")
		return nil
	})}
	cmd.Flags().Int64Var(&report, "report", 0, "terminal report event number")
	return cmd
}

func goalBindCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{Use: "bind <assignment> <worker-session>", Short: "Bind a registered assignment to the session just launched", Args: cobra.ExactArgs(2), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		caller, err := e.caller()
		if err != nil {
			return err
		}
		worker, err := e.core.CallerOf(args[1])
		if err != nil {
			return err
		}
		if err = e.core.BindAssignment(caller.Session, args[0], worker.Session); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "assignment worker bound")
		return nil
	})}
}
