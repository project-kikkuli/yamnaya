package discover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type CrewStatus int

const (
	CrewRunning CrewStatus = iota
	CrewIdle
	CrewDone
)

func (c CrewStatus) String() string {
	return [...]string{"running", "idle", "done"}[c]
}

// StaleAfter is how long an agent mid-step may stay silent before it no longer reads as running.
const StaleAfter = 15 * time.Minute

// Member is a background subagent or agent-team teammate working inside a Claude session.
type Member struct {
	ID          string
	Name        string
	Description string
	AgentType   string
	Teammate    bool
	Status      CrewStatus
	LastActive  time.Time
}

func (m Member) Label() string {
	if m.Name != "" {
		return m.Name
	}
	return m.Description
}

type agentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	Name        string `json:"name"`
	TaskKind    string `json:"taskKind"`
}

// claudeCrew reads <session>/subagents/agent-<id>.meta.json and the matching transcript for each agent.
// Claude reports a session idle only when no delegated work runs, so a session not running has no running crew.
func (d *Discoverer) claudeCrew(dir string, sessionRunning bool, now time.Time) []Member {
	metas, _ := filepath.Glob(filepath.Join(dir, "agent-*.meta.json"))
	var crew []Member
	for _, mp := range metas {
		b, err := os.ReadFile(mp)
		if err != nil {
			continue
		}
		var meta agentMeta
		if json.Unmarshal(b, &meta) != nil {
			continue
		}
		base := strings.TrimSuffix(filepath.Base(mp), ".meta.json")
		m := Member{
			ID:          strings.TrimPrefix(base, "agent-"),
			Name:        meta.Name,
			Description: meta.Description,
			AgentType:   meta.AgentType,
			Teammate:    meta.Name != "" || meta.TaskKind == "in_process_teammate",
		}
		transcript := filepath.Join(dir, base+".jsonl")
		pending := false
		if info, err := os.Stat(transcript); err == nil {
			m.LastActive = info.ModTime()
			if r, ok := lastRecord(transcript); ok {
				pending = midStep(r, now.Sub(m.LastActive))
			}
		}
		switch {
		case pending && sessionRunning && now.Sub(m.LastActive) < StaleAfter:
			m.Status = CrewRunning
		case m.Teammate:
			m.Status = CrewIdle
		default:
			m.Status = CrewDone
		}
		crew = append(crew, m)
	}
	sort.SliceStable(crew, func(i, j int) bool {
		a, b := crew[i], crew[j]
		if a.Status != b.Status {
			return a.Status < b.Status
		}
		return a.LastActive.After(b.LastActive)
	})
	return crew
}

// midStep reports whether an agent's last record leaves work outstanding: a tool call, a tool result, or an assistant message still streaming.
func midStep(r record, quiet time.Duration) bool {
	switch r.Type {
	case "user":
		return true
	case "assistant":
		if r.Message == nil || r.Message.StopReason == nil {
			return quiet < 2*time.Minute
		}
		return *r.Message.StopReason == "tool_use"
	}
	return false
}
