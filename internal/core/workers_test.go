package core

import (
	"testing"
	"time"
)

func TestWorkersCountSubagentsOfTheManagerAndItsAgents(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	manager := Live{Session: Session{ID: "mgr", Status: Idle, Crew: []Member{
		{ID: "a1", Description: "Fix the PR", Status: Busy, Last: ago(time.Minute), Facts: Facts{Doing: Action{Tool: "Bash", Detail: "go test", At: ago(time.Minute)}}},
		{ID: "a2", Description: "Old research", Status: Done, Last: ago(time.Hour)},
	}}}
	cases := []struct {
		name         string
		agents       []Live
		active, done int
		states       []string
	}{
		{"manager working through subagents alone", nil, 1, 1, []string{"busy", "done"}},
		{"agents and their subagents", []Live{
			{Session: Session{ID: "w1", Title: "PR 1", Status: Busy, Since: ago(time.Minute), Facts: Facts{Doing: Action{Tool: "Read", At: ago(time.Minute)}},
				Crew: []Member{{ID: "b1", Description: "lint", Status: Waiting, Last: ago(2 * time.Minute)}, {ID: "b2", Cut: true, Status: Busy}}}},
			{Session: Session{ID: "w2", Status: Busy, Since: ago(time.Hour), Facts: Facts{Doing: Action{Tool: "Bash", At: ago(time.Hour)}}}},
			{Session: Session{ID: "w3", Status: Idle, Facts: Facts{Result: "PR is green", ResultAt: ago(3 * time.Minute)}}},
		}, 5, 2, []string{"busy", "idle", "hung", "busy", "waiting", "done", "done"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := Node{Live: &manager, Agents: c.agents}
			n.Workers = n.workers(now, time.Time{})
			active, done := n.Working()
			if active != c.active || done != c.done {
				t.Fatalf("working = %d active, %d done; want %d, %d", active, done, c.active, c.done)
			}
			var states []string
			for _, w := range n.Workers {
				states = append(states, w.State)
			}
			if len(states) != len(c.states) {
				t.Fatalf("states = %v, want %v", states, c.states)
			}
			for i := range states {
				if states[i] != c.states[i] {
					t.Fatalf("states = %v, want %v", states, c.states)
				}
			}
		})
	}
}

func TestLatestActivityPicksTheNewestSign(t *testing.T) {
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		facts Facts
		want  string
	}{
		{"only the task, its tags dropped", Facts{Task: `<teammate-message id="x">Ship the PR</teammate-message>`}, "started: Ship the PR"},
		{"a tool call after the reply", Facts{Result: "done", ResultAt: at, Doing: Action{Tool: "Bash", Detail: "gh pr view", At: at.Add(time.Minute)}}, "Bash: gh pr view"},
		{"a reply after the tool call", Facts{Result: "PR  is\ngreen", ResultAt: at.Add(time.Minute), Doing: Action{Tool: "Bash", At: at}}, "said: PR is green"},
		{"an open question wins", Facts{Asked: "merge it?", Result: "merge it?", ResultAt: at}, "asks: merge it?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, _ := latestActivity(c.facts, at); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
