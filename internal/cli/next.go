package cli

import (
	"fmt"
	"sort"

	"github.com/project-kikkuli/yamnaya/internal/signals"
	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/spf13/cobra"
)

// askingTargets lists live windows with open items, ordered by each window's oldest open item.
func (a *app) askingTargets(snap signals.Snapshot) ([]string, error) {
	type pending struct {
		item   store.Item
		target string
	}
	var all []pending
	for _, p := range snap.Projects {
		items, err := a.store.Items(p.Name, false)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if w, ok := sourceWindow(p, it); ok && w.Alive {
				all = append(all, pending{it, w.TmuxTarget})
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].item.CreatedAt.Before(all[j].item.CreatedAt) })
	var targets []string
	seen := map[string]bool{}
	for _, pd := range all {
		if !seen[pd.target] {
			seen[pd.target] = true
			targets = append(targets, pd.target)
		}
	}
	return targets, nil
}

// sourceWindow is the window that filed it, falling back to the manager when that window is gone.
func sourceWindow(p signals.Project, it store.Item) (signals.Window, bool) {
	if w, ok := p.Window(it.SourceWindow); ok {
		return w, true
	}
	return p.Window("")
}

func nextTarget(targets []string, current string) string {
	for i, t := range targets {
		if t == current {
			return targets[(i+1)%len(targets)]
		}
	}
	return targets[0]
}

func nextCmd(withApp runner) *cobra.Command {
	return &cobra.Command{
		Use:   "next",
		Short: "Jump to the next window with an open item, oldest first",
		Args:  cobra.NoArgs,
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			snap, err := signals.NewReader(a.store, a.tmux).Snapshot()
			if err != nil {
				return err
			}
			targets, err := a.askingTargets(snap)
			if err != nil {
				return err
			}
			if len(targets) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "nothing asks you")
				return nil
			}
			current, _ := a.tmux.CurrentWindow()
			return a.tmux.Jump(nextTarget(targets, current))
		}),
	}
}
