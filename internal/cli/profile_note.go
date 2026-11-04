package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

// busyCrew is a subagent still working when the switch stopped its session.
type busyCrew struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// switchNote is the first message the session reads after m moves it: what moved and why, and which background tasks ended with it.
func switchNote(m migration, tasks []string) string {
	note := m.ResumeNote
	if note == "" {
		note = fmt.Sprintf("ym switched this conversation from profile %s to profile %s.", profile.ID(m.Source.Profile), m.Destination.ID)
		if m.Reason != "" {
			note += " Reason: " + strings.TrimSuffix(m.Reason, ".") + "."
		}
	}
	if m.Destination.Harness != "" && m.Source.Harness != m.Destination.Harness {
		note += " The port to " + m.Destination.Harness + " carried this conversation's prompts and replies only, so no tool calls or results of earlier turns are recorded here: check the filesystem and git for what they did."
	}
	if len(tasks) > 0 {
		note += " These background tasks were still running and ended with the switch; re-check each one and rerun it if its result is missing: " + strings.Join(tasks, "; ") + "."
	}
	return note
}

// backgroundTasks are the background tasks rec's conversation started and never saw finish, and any crew still busy at the stop.
func backgroundTasks(rec launch.Record, crew []busyCrew) []string {
	var tasks []string
	if rec.Harness == claude.Name {
		tasks = claude.BackgroundTasks(transcript(rec))
	}
	for _, c := range crew {
		if !slices.ContainsFunc(tasks, func(t string) bool { return strings.Contains(t, c.ID) }) {
			tasks = append(tasks, fmt.Sprintf("subagent %s (%s)", c.Name, c.ID))
		}
	}
	return tasks
}
