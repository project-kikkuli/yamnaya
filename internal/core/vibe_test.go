package core

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
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

type discoverCount struct {
	Harness
	n *int
}

func (d discoverCount) Discover(procs []proc.Proc) []Session {
	*d.n++
	return d.Harness.Discover(procs)
}

func TestVibeNudgeAsksUntilTheMissionHasAFreshVibe(t *testing.T) {
	const run = `run ym vibe "<place>" --why "..."`
	pids := map[string]int{"lead": 10, "worker": 11, "other": 30}
	for _, tc := range []struct {
		name    string
		setter  string
		later   time.Duration
		session string
		want    string
		scans   bool
	}{
		{"none yet", "", 0, "lead", "none yet, " + run, true},
		{"fresh", "lead", time.Hour, "lead", "", false},
		{"fresh on the mission, asked by its worker", "lead", time.Hour, "worker", "", false},
		{"fresh from the worker, asked by its lead", "worker", time.Hour, "lead", "", true},
		{"hours old", "lead", 3 * time.Hour, "lead", "3h old, " + run, true},
		{"days old", "lead", 50 * time.Hour, "worker", "2d old, " + run, true},
		{"other mission's vibe does not count", "lead", time.Hour, "other", "none yet, " + run, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.run(10, 1, "/dev/t1", "claude", "lead", Busy)
			w.run(11, 10, "/dev/t1", "codex", "worker", Busy)
			w.run(30, 1, "/dev/t3", "claude", "other", Idle)
			scans := 0
			w.core.Harnesses = []Harness{discoverCount{w.claude, &scans}, discoverCount{w.codex, &scans}}
			if tc.setter != "" {
				if _, err := w.core.Store.AddVibe(Vibe{Place: "Colorado", Session: tc.setter, Harness: "claude"}); err != nil {
					t.Fatal(err)
				}
			}
			w.core.Now = func() time.Time { return time.Now().Add(tc.later) }
			for range 2 {
				got, err := w.core.VibeNudge(Caller{Session: tc.session, PID: pids[tc.session]})
				if err != nil {
					t.Fatal(err)
				}
				if got != tc.want {
					t.Fatalf("nudge = %q, want %q", got, tc.want)
				}
			}
			if (scans > 0) != tc.scans {
				t.Errorf("board scans = %d, want a scan %v", scans, tc.scans)
			}
			if all, _ := w.core.Store.Vibes(); len(all) != map[bool]int{true: 1}[tc.setter != ""] {
				t.Errorf("nudging stored vibes: %+v", all)
			}
		})
	}
}

func TestVibeWithAProjectFlagLandsOnTheMissionItNamesOrFails(t *testing.T) {
	w := newWorld(t)
	repos := map[string]GitInfo{
		"/src/app":          {Top: "/src/app", Common: "/src/app/.git", Branch: "main"},
		"/src/app-wt-login": {Top: "/src/app-wt-login", Common: "/src/app/.git", Branch: "login"},
		"/src/tool":         {Top: "/src/tool", Common: "/src/tool/.git", Branch: "main"},
	}
	w.core.Git = func(dir string) GitInfo { return repos[dir] }
	for pid, s := range map[int]Session{
		10: {ID: "main", Cwd: "/src/app", Status: Busy},
		11: {ID: "login", Cwd: "/src/app-wt-login", Status: Idle},
		12: {ID: "tool", Cwd: "/src/tool", Status: Idle},
	} {
		w.procs = append(w.procs, procOf(pid, "claude"))
		w.claude.sessions[pid] = s
	}
	for _, tc := range []struct {
		name, ref, setter, want, fails string
	}{
		{"no -p lands on the setter's mission", "", "main", "main", ""},
		{"a session id", "tool", "main", "tool", ""},
		{"a repo with one mission, from another repo", "tool", "login", "tool", ""},
		{"a repo the setter works in", "app", "login", "login", ""},
		{"a repo with several missions, from outside it", "app", "tool", "", "has 2 missions"},
		{"a mistyped ref", "appx", "main", "", `project "appx": not found`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := w.board(t)
			target, err := b.VibeTarget(tc.ref, tc.setter)
			if tc.fails != "" {
				if err == nil || !strings.Contains(err.Error(), tc.fails) {
					t.Fatalf("VibeTarget(%q) = %q, %v; want an error with %q", tc.ref, target, err, tc.fails)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			v, err := w.core.Store.AddVibe(Vibe{Place: "Ohio", Session: tc.setter, Harness: "claude", Target: target})
			if err != nil {
				t.Fatal(err)
			}
			if got := b.ProjectOf(v.on()); got != tc.want {
				t.Errorf("vibe landed on %q, want %q", got, tc.want)
			}
		})
	}
}
