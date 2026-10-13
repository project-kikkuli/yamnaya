package cli

import (
	"fmt"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/spf13/cobra"
)

func managerPrompt(project, prompt string) string {
	line := fmt.Sprintf("You are the manager of the Yamnaya project %s. Use the ym skill to plan, spawn workers, and file open items.", project)
	if prompt == "" {
		return line
	}
	return line + "\n\n" + prompt
}

func workerPrompt(project, window, prompt string) string {
	line := fmt.Sprintf("You are the worker %s in the Yamnaya project %s. Use the ym skill to file open items.", window, project)
	if prompt == "" {
		return line
	}
	return line + "\n\n" + prompt
}

func splitPhases(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func newCmd(withApp runner) *cobra.Command {
	var cwd, harnessName, phases, prompt string
	cmd := &cobra.Command{
		Use:   "new <project>",
		Short: "Create a project with a tmux session and a manager window",
		Args:  cobra.ExactArgs(1),
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := a.newProject(name, cwd, harnessName, phases, prompt); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: manager started (%s) in tmux session ym-%s\n", name, harnessName, name)
			return nil
		}),
	}
	cmd.Flags().StringVar(&cwd, "cwd", ".", "working directory for the project's windows")
	cmd.Flags().StringVar(&harnessName, "harness", "claude", "manager harness: claude or codex")
	cmd.Flags().StringVar(&phases, "phases", "", "comma-separated phase list")
	cmd.Flags().StringVar(&prompt, "prompt", "", "initial prompt for the manager")
	return cmd
}

func spawnCmd(withApp runner) *cobra.Command {
	var harnessName, prompt string
	cmd := &cobra.Command{
		Use:   "spawn <name>",
		Short: "Start a worker window in the project",
		Args:  cobra.ExactArgs(1),
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			project, err := a.requireProject()
			if err != nil {
				return err
			}
			name := args[0]
			if err := a.spawn(project, name, harnessName, prompt); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: worker %s started (%s)\n", project, name, harnessName)
			return nil
		}),
	}
	cmd.Flags().StringVar(&harnessName, "harness", "claude", "worker harness: claude or codex")
	cmd.Flags().StringVar(&prompt, "prompt", "", "initial prompt for the worker")
	return cmd
}

func (a *app) spawn(project, name, harnessName, prompt string) error {
	if err := validName("window", name); err != nil {
		return err
	}
	p, err := a.store.ActiveProject(project)
	if err != nil {
		return err
	}
	if err := a.closeIfDead(project, name); err != nil {
		return err
	}
	w, launch, err := a.newWindow(project, p.Cwd, name, store.RoleWorker, harnessName, workerPrompt(project, name, prompt))
	if err != nil {
		return err
	}
	return a.store.AddWindow(w, launch)
}

func (a *app) newProject(name, cwd, harnessName, phases, prompt string) error {
	if err := validName("project", name); err != nil {
		return err
	}
	if p, err := a.store.Project(name); err == nil && p.State == store.ProjectLanded {
		return fmt.Errorf("project %q is in the kurgan; use ym revive %s", name, name)
	}
	dir, err := absDir(cwd)
	if err != nil {
		return err
	}
	w, launch, err := a.newWindow(name, dir, store.RoleManager, store.RoleManager, harnessName, managerPrompt(name, prompt))
	if err != nil {
		return err
	}
	return a.store.CreateProject(store.Project{Name: name, Cwd: dir, Phases: splitPhases(phases)}, w, launch)
}
