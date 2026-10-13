package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/harness"
	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/spf13/cobra"
)

var invalidNameChars = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func adoptName(windowName string) string {
	name := strings.Trim(invalidNameChars.ReplaceAllString(windowName, "-"), "-_")
	if name == "" {
		return "adopted"
	}
	return name
}

// adopt records an existing tmux window as an unmanaged worker of project, leaving it where it is.
func (a *app) adopt(target, project, name, harnessName string) (store.Window, error) {
	id, err := a.tmux.WindowID(target)
	if err != nil {
		return store.Window{}, err
	}
	if w, err := a.store.OpenWindowByTarget(id); err == nil {
		return w, fmt.Errorf("window %s is already %s/%s", id, w.Project, w.Name)
	}
	live, err := a.tmux.Windows()
	if err != nil {
		return store.Window{}, err
	}
	info, ok := live[id]
	if !ok {
		return store.Window{}, fmt.Errorf("no tmux window %s", target)
	}
	if name == "" {
		name = adoptName(info.Name)
	}
	if err := validName("window", name); err != nil {
		return store.Window{}, err
	}
	if harnessName == "" {
		harnessName = harness.Claude
		if strings.Contains(info.Command, "codex") {
			harnessName = harness.Codex
		}
	}
	if err := harness.Valid(harnessName); err != nil {
		return store.Window{}, err
	}
	w := store.Window{Project: project, Name: name, Role: store.RoleWorker, Harness: harnessName, Managed: false}
	tag := func(w store.Window) (string, error) { return id, a.tmux.Tag(id, strconv.FormatInt(w.ID, 10)) }
	if err := a.store.AddWindow(w, tag); err != nil {
		return w, err
	}
	w.TmuxTarget = id
	return w, nil
}

func adoptCmd(withApp runner) *cobra.Command {
	var name, harnessName string
	cmd := &cobra.Command{
		Use:   "adopt <tmux-target>",
		Short: "Track an existing tmux window as a worker of the project",
		Args:  cobra.ExactArgs(1),
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			project, err := a.requireProject()
			if err != nil {
				return err
			}
			w, err := a.adopt(args[0], project, name, harnessName)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: adopted %s as %s (%s)\n", project, w.TmuxTarget, w.Name, w.Harness)
			return nil
		}),
	}
	cmd.Flags().StringVar(&name, "name", "", "window name in the project (default: the tmux window name)")
	cmd.Flags().StringVar(&harnessName, "harness", "", "claude or codex (default: detected from the running command)")
	return cmd
}
