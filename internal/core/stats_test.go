package core

import (
	"reflect"
	"testing"
	"time"
)

type fixedMeter struct{ f Fleet }

func (m fixedMeter) Fleet(time.Time) Fleet { return m.f }

func spendOf(usd float64) Spend { return Spend{USD: usd} }

// statsWorld is a goal whose manager runs one busy and one idle agent, with a vibe, phases, a progress line, two pull requests
// opened for it and every session's spend read.
func statsWorld(t *testing.T) (*orgWorld, Manager) {
	t.Helper()
	w := newOrgWorld(t)
	g, m := w.goal(t, "stream the export", "/repo/august")
	w.spawn("busy-agent", m.Session)
	w.set("busy-agent", func(s *Session) { s.Status = Busy })
	w.spawn("idle-agent", m.Session)
	if _, err := w.core.Store.AddVibe(Vibe{Place: "Reykjavik", Why: "steady", Session: m.Session, Harness: "claude", At: w.clock}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.Store.SetPhases(m.Session, []string{"plan", "ship"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.UpdateGoal(m.Session, "parser lands, two PRs up"); err != nil {
		t.Fatal(err)
	}
	for url, st := range map[string]PRStatus{
		"https://github.com/o/app/pull/1": {State: "OPEN", Passed: 3},
		"https://github.com/o/app/pull/2": {State: "MERGED", Passed: 3},
	} {
		if err := w.core.Store.SavePRStatus(url, st); err != nil {
			t.Fatal(err)
		}
		if _, _, err := w.core.RecordGoalEvent(g.ID, EventPROpened, "opened "+url, g.ID+":pr:"+url); err != nil {
			t.Fatal(err)
		}
	}
	sessions := map[string]SessionSpend{}
	for id, usd := range map[string]float64{m.Session: 2, "busy-agent": 3, "idle-agent": 1} {
		key := Live{Session: Session{Harness: "claude", ID: id}}.UsageKey()
		sessions[key] = SessionSpend{Harness: "claude", ID: id, Total: spendOf(usd)}
	}
	w.core.Meter = fixedMeter{Fleet{Ready: true, Sessions: sessions}}
	return w, m
}

func TestAGoalRowsStatsComeFromItsAgentsPullRequestsVibeSpendAndProgress(t *testing.T) {
	w, m := statsWorld(t)
	st := node(t, w.board(t), m.ID).Stats
	if st.Running != 1 || st.Idle != 1 {
		t.Errorf("agents running %d idle %d, want 1 and 1", st.Running, st.Idle)
	}
	if want := (PRCount{Open: 1, Green: 1, Merged: 1}); st.PRs != want || st.PRs.CI() != "green" {
		t.Errorf("prs %+v ci %q, want %+v green", st.PRs, st.PRs.CI(), want)
	}
	if st.Vibe == nil || st.Vibe.Place != "Reykjavik" {
		t.Errorf("vibe %+v, want Reykjavik", st.Vibe)
	}
	if st.Spend == nil || st.Spend.USD != 6 {
		t.Errorf("spend %+v, want $6 over the manager and its agents", st.Spend)
	}
	if st.Progress != "parser lands, two PRs up" || !st.ProgressAt.Equal(w.clock) || st.Started.IsZero() {
		t.Errorf("progress %q at %v, started %v", st.Progress, st.ProgressAt, st.Started)
	}
}

func TestAnotherHostsMissionAndGoalShowTheStatsThatHostComputed(t *testing.T) {
	far, m := statsWorld(t)
	fb := far.board(t)
	r := RemoteBoard{Host: "far", At: far.clock, Missions: map[string]Stats{}, GoalStats: map[string]Stats{}, ManagerSessions: fb.Org.Sessions}
	for _, s := range fb.Sessions {
		s.Remote, s.Crew, s.PRs = "far", nil, nil
		r.Sessions = append(r.Sessions, s)
	}
	for _, p := range fb.Projects {
		r.Missions[p.Key] = p.Stats
	}
	for _, mg := range fb.Org.Managers {
		r.Managers = append(r.Managers, mg)
	}
	for _, g := range fb.Org.Goals {
		r.Goals = append(r.Goals, g)
	}
	r.GoalStats[m.ID] = node(t, fb, m.ID).Stats
	near := newWorld(t)
	near.core.Meter = fixedMeter{Fleet{Ready: true}}
	near.core.Remotes, near.core.ShowRemotes = &fakeRemotes{boards: []RemoteBoard{r}}, true
	nb := near.board(t)
	want, _ := fb.Project(m.Session)
	got, ok := nb.Project(m.Session)
	if !ok || !reflect.DeepEqual(got.Stats, want.Stats) {
		t.Fatalf("remote mission stats %+v, want %+v as far computed them", got.Stats, want.Stats)
	}
	if v, ok := got.Vibe(); !ok || v.Place != "Reykjavik" || !reflect.DeepEqual(got.Phases, want.Phases) || got.Spend.USD != 6 {
		t.Errorf("remote mission vibe %+v phases %+v spend %+v", v, got.Phases, got.Spend)
	}
	if n := node(t, nb, RemoteKey("far", m.ID)); !reflect.DeepEqual(n.Stats, r.GoalStats[m.ID]) {
		t.Errorf("remote goal stats %+v, want %+v", n.Stats, r.GoalStats[m.ID])
	}
}
