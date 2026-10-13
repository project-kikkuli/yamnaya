package cli

import (
	"encoding/json"
	"io"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/store"
)

type itemJSON struct {
	ID           string         `json:"id"`
	Project      string         `json:"project"`
	Kind         string         `json:"kind"`
	Question     string         `json:"question"`
	Rec          string         `json:"rec"`
	Options      []store.Option `json:"options"`
	Assumption   string         `json:"assumption"`
	SourceWindow string         `json:"source_window"`
	CreatedAt    string         `json:"created_at"`
	State        string         `json:"state"`
	Answer       string         `json:"answer"`
	Reason       string         `json:"reason"`
	AnsweredAt   string         `json:"answered_at"`
	History      []eventJSON    `json:"history,omitempty"`
}

type eventJSON struct {
	At   string `json:"at"`
	From string `json:"from"`
	To   string `json:"to"`
	Text string `json:"text"`
}

type windowJSON struct {
	Name       string `json:"name"`
	Role       string `json:"role"`
	Harness    string `json:"harness"`
	SessionID  string `json:"session_id"`
	TmuxTarget string `json:"tmux_target"`
	Managed    bool   `json:"managed"`
	Alive      bool   `json:"alive"`
	OpenItems  int    `json:"open_items"`
	CreatedAt  string `json:"created_at"`
}

type projectJSON struct {
	Name        string       `json:"name"`
	Cwd         string       `json:"cwd"`
	State       string       `json:"state"`
	Phases      []string     `json:"phases"`
	PhaseIndex  int          `json:"phase_index"`
	Phase       string       `json:"phase"`
	OpenItems   int          `json:"open_items"`
	TmuxSession string       `json:"tmux_session"`
	Alive       bool         `json:"alive"`
	CreatedAt   string       `json:"created_at"`
	LandedAt    string       `json:"landed_at"`
	Windows     []windowJSON `json:"windows"`
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func toItemJSON(it store.Item) itemJSON {
	return itemJSON{
		ID: it.ID, Project: it.Project, Kind: it.Kind, Question: it.Question, Rec: it.Rec,
		Options: it.Options, Assumption: it.Assumption, SourceWindow: it.SourceWindow,
		CreatedAt: ts(it.CreatedAt), State: it.State, Answer: it.Answer, Reason: it.Reason,
		AnsweredAt: ts(it.AnsweredAt), History: toEventsJSON(it.History),
	}
}

func toEventsJSON(events []store.Event) []eventJSON {
	if events == nil {
		return nil
	}
	out := make([]eventJSON, 0, len(events))
	for _, e := range events {
		out = append(out, eventJSON{At: ts(e.At), From: e.From, To: e.To, Text: e.Text})
	}
	return out
}

func currentPhase(p store.Project) string {
	if p.PhaseIndex < len(p.Phases) {
		return p.Phases[p.PhaseIndex]
	}
	return ""
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
