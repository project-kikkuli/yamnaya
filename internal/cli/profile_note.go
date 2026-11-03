package cli

import (
	"fmt"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

// switchNote is the first message the session reads after m moves it: what moved and why, and which background tasks ended with it.
func switchNote(m migration, tasks []string) string {
	note := m.ResumeNote
	if note == "" {
		note = fmt.Sprintf("ym switched this conversation from profile %s to profile %s.", profile.ID(m.Source.Profile), m.Destination.ID)
		if m.Reason != "" {
			note += " Reason: " + strings.TrimSuffix(m.Reason, ".") + "."
		}
	}
	if len(tasks) > 0 {
		note += " These background tasks were still running and ended with the switch; re-check each one and rerun it if its result is missing: " + strings.Join(tasks, "; ") + "."
	}
	return note
}

// backgroundTasks are the background tasks rec's conversation started and never saw finish.
func backgroundTasks(rec launch.Record) []string {
	if rec.Harness != claude.Name {
		return nil
	}
	return claude.BackgroundTasks(transcript(rec))
}
