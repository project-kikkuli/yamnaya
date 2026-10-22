package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func pr(n int, st core.PRStatus) core.PR {
	return core.PR{URL: "https://github.com/o/august/pull/" + string(rune('0'+n%10)) + string(rune('a'+n/10)), Repo: "o/august", Number: n, Status: &st}
}

// workBoard is one session the owner started in august with a crew and pull requests, one goal in august run by a
// manager with an agent of its own, and a mission that manager's agent runs, which belongs to the goal instead.
func workBoard() core.Board {
	lead := core.Live{Session: core.Session{Harness: "claude", ID: "lead", Title: "Arbiter rollout", Status: core.Idle, Repo: "august",
		Crew: []core.Member{
			{ID: "c1", Name: "hchb-readback", Status: core.Busy},
			{ID: "c2", Name: "fix-18581", Status: core.Waiting},
			{ID: "c3", Name: "free-disk", Status: core.Idle, Teammate: true},
			{ID: "c4", Name: "old research", Status: core.Done},
		},
		Facts: core.Facts{PRs: []core.PR{
			pr(11, core.PRStatus{State: "OPEN", Failed: 1}),
			pr(12, core.PRStatus{State: "OPEN", Passed: 3}),
			pr(13, core.PRStatus{State: "MERGED"}),
			pr(14, core.PRStatus{State: "CLOSED"}),
		}}}}
	agent := core.Live{Session: core.Session{Harness: "claude", ID: "importer-agent", Title: "write the CSV reader", Status: core.Busy}}
	goal := core.Node{Manager: core.Manager{ID: "mgr", Name: "importer"}, Goal: &core.Goal{ID: "g", Ask: "make the importer handle CSV", Repo: "/src/august", ManagerID: "mgr"}, Agents: []core.Live{agent}}
	return core.Board{
		Tree: []core.Node{goal},
		Projects: []core.Project{
			{Key: "lead", Name: "Arbiter rollout", RepoName: "august", Sessions: []core.Live{lead}},
			{Key: "importer-agent", Name: "write the CSV reader", RepoName: "august", Sessions: []core.Live{agent}, Manager: "mgr"},
		},
		Needs: []core.Need{{Project: "lead", Session: &lead, Reply: "keep or switch?", Line: "keep or switch?"}},
	}
}

func TestTheMountainsAreTheOwnersWork(t *testing.T) {
	m := New(&core.Core{})
	m.board, m.loaded = workBoard(), true
	s := m.sceneOf()
	if len(s.Ranges) != 1 || s.Ranges[0].Name != "august" {
		t.Fatalf("ranges %+v, want one for august", s.Ranges)
	}
	peaks := s.Ranges[0].Peaks
	if len(peaks) != 2 || peaks[0].ID != "mgr" || peaks[1].ID != "lead" {
		t.Fatalf("peaks %+v, want the goal then the owner's session, and no peak for the goal's own agent", peaks)
	}
	goal, mission := peaks[0], peaks[1]
	if len(goal.Team) != 1 || goal.Team[0].Name != "write the CSV reader" || goal.Team[0].Stance != scene.Climbing {
		t.Errorf("goal team %+v, want its busy agent climbing", goal.Team)
	}
	want := []scene.Climber{{Name: "hchb-readback", Stance: scene.Climbing}, {Name: "fix-18581", Stance: scene.Waiting}, {Name: "free-disk", Stance: scene.Resting}}
	if !slices.Equal(mission.Team, want) {
		t.Errorf("mission team %+v, want %+v: finished crew leave, the idle lead stays off the slope", mission.Team, want)
	}
	camps := map[string]scene.CampState{}
	for _, c := range mission.Camps {
		camps[c.Label] = c.State
	}
	if len(camps) != 3 || camps["#11"] != scene.Failing || camps["#12"] != scene.Green || camps["#13"] != scene.Merged {
		t.Errorf("camps %+v, want #11 failing, #12 green, #13 merged and the closed one gone", camps)
	}
	if !mission.NeedsYou || goal.NeedsYou {
		t.Errorf("needs you: mission %v goal %v", mission.NeedsYou, goal.NeedsYou)
	}
	if mission.Weather != scene.Storm {
		t.Errorf("a failing pull request gives weather %v, want a storm", mission.Weather)
	}
	if mission.Progress <= 0 || mission.Progress >= 1 {
		t.Errorf("mission progress %v, want measured between foot and top", mission.Progress)
	}
}

func TestTheSelectedRowLightsItsPeakAndAClickOnAPeakSelectsItsRow(t *testing.T) {
	m := New(&core.Core{})
	b := workBoard()
	b.Tree = nil
	m.board, m.loaded = b, true
	m.width, m.height = 174, 72
	pal.color = true
	for i, r := range m.rows() {
		if r.project != nil && r.project.Key == "lead" || r.need != nil {
			m.cursor = i
			want := m.peakOfRow(r)
			if got := m.sceneOf().Selected; got != want || got == "" {
				t.Fatalf("row %d selects peak %q, want %q", i, got, want)
			}
		}
	}
	m.cursor, m.page = -1, nil
	frame := strings.Split(ansi.Strip(m.View()), "\n")
	for y, line := range frame {
		if x := strings.Index(line, "hchb-readback"); x >= 0 {
			m.pick(ansi.StringWidth(line[:x]), y)
			if r, ok := m.selected(); !ok || m.peakOfRow(r) != "lead" {
				t.Fatalf("clicking the name of the lead's climber selected %+v", r)
			}
			return
		}
	}
	t.Fatal("the lead's climber is not drawn")
}

func TestKeysComeBeforeTheMountainsMove(t *testing.T) {
	m := New(&core.Core{})
	b := workBoard()
	b.Tree = nil
	m.board, m.loaded = b, true
	m.width, m.height = 174, 72
	m.peaks.start = time.Now().Add(-time.Hour)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	next, cmd := next.(Model).Update(mountainFrame{})
	if f := next.(Model).peaks.frame; f != 0 || cmd == nil {
		t.Fatalf("a frame right after a key advanced the mountains to %d (next tick %v)", f, cmd != nil)
	}
	held := next.(Model)
	held.peaks.quiet = time.Now().Add(-time.Millisecond)
	next, _ = held.Update(mountainFrame{})
	if next.(Model).peaks.frame == 0 {
		t.Fatal("the mountains stayed still after the keys stopped")
	}
}
