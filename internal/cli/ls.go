package cli

import (
	"fmt"
	"io"

	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/project-kikkuli/yamnaya/internal/tmux"
	"github.com/spf13/cobra"
)

// describe joins a project's stored state with live tmux windows; live is nil for landed projects.
// While the project's session runs, a window missing from it is confirmed dead and its row is closed.
func (a *app) describe(p store.Project, live map[string]tmux.WindowInfo) (projectJSON, error) {
	total, byWindow, err := a.store.OpenItemCounts(p.Name)
	if err != nil {
		return projectJSON{}, err
	}
	session := tmux.SessionName(p.Name)
	pj := projectJSON{
		Name: p.Name, Cwd: p.Cwd, State: p.State, Phases: p.Phases, PhaseIndex: p.PhaseIndex,
		Phase: currentPhase(p), OpenItems: total, TmuxSession: session,
		CreatedAt: ts(p.CreatedAt), LandedAt: ts(p.LandedAt), Windows: []windowJSON{},
	}
	if p.State != store.ProjectActive {
		return pj, nil
	}
	windows, err := a.store.OpenWindows(p.Name)
	if err != nil {
		return pj, err
	}
	if err := a.resolveCodexSessions(p, windows); err != nil {
		return pj, err
	}
	for _, info := range live {
		if info.Session == session {
			pj.Alive = true
			break
		}
	}
	for _, w := range windows {
		alive := liveWindow(w, live)
		if pj.Alive && !alive {
			if err := a.store.CloseWindow(w.ID); err != nil {
				return pj, err
			}
			continue
		}
		pj.Windows = append(pj.Windows, windowJSON{
			Name: w.Name, Role: w.Role, Harness: w.Harness, SessionID: w.SessionID,
			TmuxTarget: w.TmuxTarget, Managed: w.Managed, Alive: alive,
			OpenItems: byWindow[w.Name], CreatedAt: ts(w.CreatedAt),
		})
	}
	return pj, nil
}

func printProject(w io.Writer, p projectJSON) {
	dots := ""
	for i := range p.Phases {
		if i <= p.PhaseIndex {
			dots += "●"
		} else {
			dots += "○"
		}
	}
	fmt.Fprintf(w, "%-24s %s  %s  %d open\n", p.Name, dots, p.Phase, p.OpenItems)
	for _, win := range p.Windows {
		state := "gone"
		if win.Alive {
			state = "alive"
		}
		extra := ""
		if win.OpenItems > 0 {
			extra = fmt.Sprintf("  asks you (%d)", win.OpenItems)
		}
		fmt.Fprintf(w, "  %-22s %-7s %-6s %s%s\n", win.Name, win.Role, win.Harness, state, extra)
	}
}

func lsCmd(withApp runner) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List active projects (or only -p) with phase, open items, and windows",
		Args:  cobra.NoArgs,
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			projects, err := a.store.Projects(store.ProjectActive)
			if err != nil {
				return err
			}
			if a.projectFlag != "" {
				p, err := a.store.ActiveProject(a.projectFlag)
				if err != nil {
					return err
				}
				projects = []store.Project{p}
			}
			live, err := a.tmux.Windows()
			if err != nil {
				return err
			}
			out := make([]projectJSON, 0, len(projects))
			for _, p := range projects {
				pj, err := a.describe(p, live)
				if err != nil {
					return err
				}
				out = append(out, pj)
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"projects": out})
			}
			for _, p := range out {
				printProject(cmd.OutOrStdout(), p)
			}
			return nil
		}),
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func jumpCmd(withApp runner) *cobra.Command {
	return &cobra.Command{
		Use:   "jump <project> [window]",
		Short: "Switch the tmux client to a project or one of its windows",
		Args:  cobra.RangeArgs(1, 2),
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			p, err := a.store.ActiveProject(args[0])
			if err != nil {
				return err
			}
			session := tmux.SessionName(p.Name)
			if !a.tmux.HasSession(session) {
				return fmt.Errorf("project %q has no live tmux session %s", p.Name, session)
			}
			target := "=" + session
			if len(args) == 2 {
				w, err := a.store.OpenWindow(p.Name, args[1])
				if err != nil {
					return err
				}
				live, err := a.tmux.Windows()
				if err != nil {
					return err
				}
				if !liveWindow(w, live) {
					return fmt.Errorf("window %q is not alive in tmux", w.Name)
				}
				target = w.TmuxTarget
			}
			return a.tmux.Jump(target)
		}),
	}
}
