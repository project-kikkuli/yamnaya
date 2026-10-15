package core

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestVibesLandOnTheMissionOfWhoeverSetsThem(t *testing.T) {
	w := newWorld(t)
	w.run(10, 1, "/dev/t1", "claude", "lead", Busy)
	w.run(11, 10, "/dev/t1", "codex", "worker", Busy)
	w.run(20, 1, "/dev/t2", "claude", "solo", Idle)
	w.run(30, 1, "/dev/t3", "claude", "other", Idle)
	if err := w.core.Store.Merge("solo", "lead"); err != nil {
		t.Fatal(err)
	}
	for _, v := range []Vibe{
		{Place: "North  Dakota", Why: "tests\n red", Session: "lead", Harness: "claude"},
		{Place: "Colorado", Session: "worker", Harness: "codex"},
		{Place: "Hawaii", Why: "landed", Session: "solo", Harness: "claude"},
		{Place: "Utah", Session: "", Target: "other"},
	} {
		if _, err := w.core.Store.AddVibe(v); err != nil {
			t.Fatal(err)
		}
	}
	b := w.board(t)
	for _, tc := range []struct {
		project string
		places  []string
	}{
		{"lead", []string{"Hawaii", "Colorado", "North Dakota"}},
		{"other", []string{"Utah"}},
	} {
		p, _ := b.Project(tc.project)
		var got []string
		for _, v := range p.Vibes {
			got = append(got, v.Place)
		}
		if len(got) != len(tc.places) {
			t.Fatalf("%s vibes = %v, want %v", tc.project, got, tc.places)
		}
		for i := range got {
			if got[i] != tc.places[i] {
				t.Fatalf("%s vibes = %v, want %v", tc.project, got, tc.places)
			}
		}
	}
	p, _ := b.Project("lead")
	if v, _ := p.Vibe(); v.Why != "landed" || v.Harness != "claude" || v.Session != "solo" {
		t.Errorf("latest = %+v", v)
	}
	if p.Vibes[2].Why != "tests red" {
		t.Errorf("why kept as %q", p.Vibes[2].Why)
	}
	for _, bad := range []Vibe{{Place: "  ", Session: "lead"}, {Place: "Ohio"}} {
		if _, err := w.core.Store.AddVibe(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestVibeGoesStaleWithAgeOrWhenEverySessionEnded(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		age      time.Duration
		statuses []Status
		stale    bool
	}{
		{"fresh and running", time.Hour, []Status{Busy}, false},
		{"fresh with one session left", time.Hour, []Status{Done, Idle}, false},
		{"old", 3 * time.Hour, []Status{Busy}, true},
		{"every session done", time.Minute, []Status{Done, Done}, true},
		{"no session left", time.Minute, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Project{Vibes: []Vibe{{Place: "Colorado", At: now.Add(-tc.age)}}}
			for _, st := range tc.statuses {
				p.Sessions = append(p.Sessions, Live{Session: Session{Status: st}})
			}
			if got := p.VibeStale(now); got != tc.stale {
				t.Fatalf("stale = %v, want %v", got, tc.stale)
			}
		})
	}
}

func TestPlacesKeepOnlyChanges(t *testing.T) {
	var p Project
	for _, place := range []string{"Hawaii", "hawaii", "Colorado", "Colorado", "North Dakota", "Colorado", "Utah"} {
		p.Vibes = append(p.Vibes, Vibe{Place: place})
	}
	for _, tc := range []struct {
		n    int
		want []string
	}{
		{5, []string{"Hawaii", "Colorado", "North Dakota", "Colorado", "Utah"}},
		{2, []string{"Hawaii", "Colorado"}},
	} {
		got := p.Places(tc.n)
		if len(got) != len(tc.want) {
			t.Fatalf("Places(%d) = %v", tc.n, got)
		}
		for i := range got {
			if got[i].Place != tc.want[i] {
				t.Fatalf("Places(%d) = %v", tc.n, got)
			}
		}
	}
}

func TestVibeTableMigratesOnceAndKeepsHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := OpenStore(path)
			if err == nil {
				_, err = st.AddVibe(Vibe{Place: "Colorado", Session: "s"})
				st.Close()
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if vs, err := st.Vibes(); err != nil || len(vs) != len(errs) {
		t.Fatalf("vibes = %d, %v", len(vs), err)
	}
}

func TestVibeNudgeAsksUntilTheMissionHasAFreshVibe(t *testing.T) {
	const run = `run ym vibe "<place>" --why "..."`
	for _, tc := range []struct {
		name    string
		vibe    bool
		later   time.Duration
		session string
		want    string
	}{
		{"none yet", false, 0, "lead", "none yet, " + run},
		{"fresh", true, time.Hour, "lead", ""},
		{"fresh on the mission, asked by its worker", true, time.Hour, "worker", ""},
		{"hours old", true, 3 * time.Hour, "lead", "3h old, " + run},
		{"days old", true, 50 * time.Hour, "worker", "2d old, " + run},
		{"other mission's vibe does not count", true, time.Hour, "other", "none yet, " + run},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.run(10, 1, "/dev/t1", "claude", "lead", Busy)
			w.run(11, 10, "/dev/t1", "codex", "worker", Busy)
			w.run(30, 1, "/dev/t3", "claude", "other", Idle)
			if tc.vibe {
				if _, err := w.core.Store.AddVibe(Vibe{Place: "Colorado", Session: "lead", Harness: "claude"}); err != nil {
					t.Fatal(err)
				}
			}
			w.core.Now = func() time.Time { return time.Now().Add(tc.later) }
			for range 2 {
				got, err := w.core.VibeNudge(tc.session)
				if err != nil {
					t.Fatal(err)
				}
				if got != tc.want {
					t.Fatalf("nudge = %q, want %q", got, tc.want)
				}
			}
			if all, _ := w.core.Store.Vibes(); len(all) != map[bool]int{true: 1}[tc.vibe] {
				t.Errorf("nudging stored vibes: %+v", all)
			}
		})
	}
}

func TestVibeTargetTakesWhatItemAddTakesFromASession(t *testing.T) {
	w := newWorld(t)
	w.run(10, 1, "/dev/t1", "claude", "lead", Busy)
	w.run(30, 1, "/dev/t3", "claude", "other", Idle)
	b := w.board(t)
	for _, tc := range []struct {
		ref, caller, want string
		fails             bool
	}{
		{"other", "lead", "other", false},
		{"title other", "lead", "other", false},
		{"yamnaya", "lead", "", false},
		{"", "lead", "", false},
		{"other", "", "other", false},
		{"yamnaya", "", "", true},
	} {
		got, err := b.VibeTarget(tc.ref, tc.caller)
		if (err != nil) != tc.fails || got != tc.want {
			t.Errorf("VibeTarget(%q, %q) = %q, %v; want %q, fails %v", tc.ref, tc.caller, got, err, tc.want, tc.fails)
		}
	}
}
