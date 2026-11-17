package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestSessionHistoryDisplaysRecordedMessageSources(t *testing.T) {
	now := time.Now()
	m := Model{board: core.Board{At: now}}
	live := core.Live{Session: core.Session{ID: "session"}, Messages: []core.Message{
		{Source: "owner", Text: "owner request", State: core.MsgPending, CreatedAt: now},
		{Source: "system", Text: "remote tool output", State: core.MsgPending, CreatedAt: now},
		{Source: "legacy", Text: "unattributed content", State: core.MsgPending, CreatedAt: now},
	}}
	var output strings.Builder
	for _, section := range m.sessionSections(live) {
		output.WriteString(section.row.body)
	}
	for _, want := range []string{"[owner] owner request", "[system tool result] remote tool output", "[legacy, source unknown] unattributed content"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("history lost provenance %q: %s", want, output.String())
		}
	}
}
