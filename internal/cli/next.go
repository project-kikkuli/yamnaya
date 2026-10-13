package cli

import (
	"fmt"
	"sort"

	"github.com/project-kikkuli/yamnaya/internal/signals"
	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/spf13/cobra"
)

// askingWindows lists live windows waiting on a permission prompt, then live windows with open items ordered by each window's oldest open item.
func (a *app) askingWindows(snap signals.Snapshot) ([]signals.Window, error) {
	type pending struct {
		item   store.Item
		window signals.Window
	}
	var all []pending
	for _, p := range snap.Projects {
		items, err := a.store.Items(p.Name, false)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if w, ok := sourceWindow(p, it); ok && w.Alive {
				all = append(all, pending{it, w})
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].item.CreatedAt.Before(all[j].item.CreatedAt) })
	var out []signals.Window
	seen := map[int64]bool{}
	for _, p := range snap.Projects {
		for _, w := range p.Windows {
			if w.Alive && w.Permission != nil && !seen[w.ID] {
				seen[w.ID] = true
				out = append(out, w)
			}
		}
	}
	for _, pd := range all {
		if !seen[pd.window.ID] {
			seen[pd.window.ID] = true
			out = append(out, pd.window)
		}
	}
	return out, nil
}

// sourceWindow is the window that filed it, falling back to the manager when that window is gone.
func sourceWindow(p signals.Project, it store.Item) (signals.Window, bool) {
	if w, ok := p.Window(it.SourceWindow); ok {
		return w, true
	}
	return p.Window("")
}

// nextWindow is the window after the one matching current, or the first.
func nextWindow(windows []signals.Window, current func(signals.Window) bool) signals.Window {
	for i, w := range windows {
		if current(w) {
			return windows[(i+1)%len(windows)]
		}
	}
	return windows[0]
}

func nextCmd(withApp runner) *cobra.Command {
	return &cobra.Command{
		Use:   "next",
		Short: "Jump to the next window waiting on a permission prompt, then one with an open item, oldest first; inside ym home, stage it",
		Args:  cobra.NoArgs,
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			snap, err := signals.NewReader(a.store, a.tmux).Snapshot()
			if err != nil {
				return err
			}
			windows, err := a.askingWindows(snap)
			if err != nil {
				return err
			}
			if len(windows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "nothing asks you")
				return nil
			}
			session, current, _ := a.tmux.CurrentSessionWindow()
			if session == homeSession {
				st, _, err := a.store.Stage()
				if err != nil {
					return err
				}
				w := nextWindow(windows, func(w signals.Window) bool { return w.ID == st.WindowID })
				return a.stage(w.ID, true)
			}
			w := nextWindow(windows, func(w signals.Window) bool { return w.TmuxTarget == current })
			return a.tmux.Jump(w.Pane)
		}),
	}
}
