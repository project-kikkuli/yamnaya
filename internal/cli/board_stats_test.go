package cli

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestAHostsStatsAndLastLinesSurviveItsSnapshot(t *testing.T) {
	at := time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC)
	done, spend := 0.5, core.Spend{USD: 4.2}
	mission := core.Stats{Vibe: &core.Vibe{Place: "Reykjavik", At: at}, Phases: core.Phases{Names: []string{"plan", "ship"}, Current: 1},
		Running: 2, Idle: 1, PRs: core.PRCount{Open: 2, Green: 1, Merged: 1}, Spend: &spend, Done: &done}
	goal := mission
	goal.Progress, goal.ProgressAt, goal.Started = "parser lands", at, at.Add(-time.Hour)
	raw, err := json.Marshal(boardJSON{
		Sessions:  []sessionJSON{{Harness: "claude", ID: "lead", Status: "idle", Last: "Ship it?", LastAt: ts(at)}},
		Missions:  map[string]core.Stats{"lead": mission},
		GoalStats: map[string]core.Stats{"mgr1": goal},
	})
	if err != nil {
		t.Fatal(err)
	}
	var j boardJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	r := remoteBoard("box", j)
	if s := r.Sessions[0]; s.LastLine() != "Ship it?" || !s.ResultAt.Equal(at) {
		t.Errorf("session last line %q at %v", s.LastLine(), s.ResultAt)
	}
	if !reflect.DeepEqual(r.Missions["lead"], mission) || !reflect.DeepEqual(r.GoalStats["mgr1"], goal) {
		t.Errorf("stats %+v %+v, want %+v %+v", r.Missions["lead"], r.GoalStats["mgr1"], mission, goal)
	}
}
