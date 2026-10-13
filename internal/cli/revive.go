package cli

import (
	"fmt"
	"strconv"

	"github.com/project-kikkuli/yamnaya/internal/harness"
	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/project-kikkuli/yamnaya/internal/tmux"
	"github.com/spf13/cobra"
)

func reviveCmd(withApp runner) *cobra.Command {
	return &cobra.Command{
		Use:   "revive <project>",
		Short: "Recreate a landed or dead project's tmux session, resuming each window's session",
		Args:  cobra.ExactArgs(1),
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			p, n, err := a.revive(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: revived %d windows in tmux session %s\n", p.Name, n, tmux.SessionName(p.Name))
			return nil
		}),
	}
}

// relaunch resumes the window's session, or starts it fresh from its prompt when no session was ever recorded.
func relaunch(w store.Window) (harness.Launch, error) {
	if w.SessionID != "" {
		return harness.Resume(w.Harness, w.SessionID)
	}
	return harness.Start(w.Harness, w.Prompt)
}

func trustFor(harnessName, cwd string) error {
	trust := map[string]func(string) error{harness.Claude: harness.TrustClaudeDir, harness.Codex: harness.TrustCodexDir}[harnessName]
	if trust == nil {
		return nil
	}
	if err := trust(cwd); err != nil {
		return fmt.Errorf("mark %s trusted for %s: %w", cwd, harnessName, err)
	}
	return nil
}

// revive recreates a landed or dead project's session, resuming each window, and reports how many windows came back.
func (a *app) revive(name string) (store.Project, int, error) {
	p, windows, err := a.store.ReviveWindows(name)
	if err != nil {
		return p, 0, err
	}
	session := tmux.SessionName(p.Name)
	if a.tmux.HasSession(session) {
		return p, 0, fmt.Errorf("project %q is alive in tmux session %s; use ym jump", p.Name, session)
	}
	if len(windows) == 0 {
		return p, 0, fmt.Errorf("project %q has no windows to revive", p.Name)
	}
	if err := restoreCwd(p); err != nil {
		return p, 0, err
	}
	if err := a.recordGit(p); err != nil {
		return p, 0, err
	}
	if err := a.resolveCodexSessions(p, windows); err != nil {
		return p, 0, err
	}
	launch := func(w store.Window) (string, store.Window, error) {
		l, err := relaunch(w)
		if err != nil {
			return "", w, err
		}
		w.SessionID = l.SessionID
		if err := trustFor(w.Harness, p.Cwd); err != nil {
			return "", w, err
		}
		env := windowEnv(p.Name, w.Name)
		target, err := a.tmux.NewWindow(session, w.Name, strconv.FormatInt(w.ID, 10), p.Cwd, env, harness.CarryEnv(w.Harness, l.Argv, env))
		return target, w, err
	}
	if err := a.store.Revive(p.Name, windows, launch); err != nil {
		if a.tmux.HasSession(session) {
			a.tmux.KillSession(session)
		}
		return p, 0, err
	}
	return p, len(windows), nil
}
