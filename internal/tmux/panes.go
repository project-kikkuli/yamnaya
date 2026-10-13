package tmux

import (
	"strconv"
	"strings"
)

type PaneInfo struct {
	ID          string
	WindowID    string
	Session     string
	WindowIndex int
	WindowName  string
	PID         int
	Cwd         string
	Dead        bool
}

// Panes lists every pane on the server; a missing server yields none.
func (c Client) Panes() ([]PaneInfo, error) {
	out, err := c.run("list-panes", "-a", "-F", "#{pane_id}\t#{window_id}\t#{session_name}\t#{window_index}\t#{window_name}\t#{pane_pid}\t#{pane_current_path}\t#{pane_dead}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") {
			return nil, nil
		}
		return nil, err
	}
	var panes []PaneInfo
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 8 {
			continue
		}
		idx, _ := strconv.Atoi(f[3])
		pid, _ := strconv.Atoi(f[5])
		panes = append(panes, PaneInfo{ID: f[0], WindowID: f[1], Session: f[2], WindowIndex: idx, WindowName: f[4], PID: pid, Cwd: f[6], Dead: f[7] == "1"})
	}
	return panes, nil
}
