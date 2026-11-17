package cli

import (
	"github.com/project-kikkuli/yamnaya/internal/core"
	"time"
)

type inboxMessageJSON struct {
	Seq       int64     `json:"seq"`
	Source    string    `json:"source"`
	Text      string    `json:"text"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	Delivered time.Time `json:"delivered_at,omitempty"`
}

func inboxMessage(m core.Message) inboxMessageJSON {
	return inboxMessageJSON{Seq: m.Seq, Source: m.Source, Text: m.Text, State: m.State, CreatedAt: m.CreatedAt, Delivered: m.Delivered}
}
