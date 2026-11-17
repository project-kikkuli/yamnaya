package core

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueuedWorkerRetainsItsAssignmentAndStartsOnlyOnce(t *testing.T) {
	for _, managed := range []bool{true, false} {
		t.Run(map[bool]string{true: "managed", false: "unmanaged"}[managed], func(t *testing.T) {
			w := newOrgWorld(t)
			g, manager := w.goal(t, "work", "/repo/app")
			before := len(w.launches.starts)
			w.core.LaunchAdmission = func(NewLaunch) error { return &LaunchDeferred{Reason: "memory pressure"} }
			n := NewLaunch{Repo: "/repo/app", Prompt: "implement exactly this", Session: "planned-worker", Source: "stable-work"}
			if managed {
				n.Parent = manager.Session
			}
			for range 2 {
				message, err := w.core.StartNew(n)
				if err != nil || !strings.Contains(message, "queued") {
					t.Fatalf("request: %q, %v", message, err)
				}
			}
			pending, err := w.core.Store.PendingLaunches()
			if err != nil || len(pending) != 1 || pending[0].Launch.Session != n.Session || pending[0].Launch.Source != n.Source || pending[0].Launch.Prompt == "" {
				t.Fatalf("pending %+v, %v", pending, err)
			}
			if len(w.launches.starts) != before {
				t.Fatal("pressure created a native agent")
			}
			if err := w.core.DrainLaunches(); err != nil {
				t.Fatal(err)
			}
			if len(w.launches.starts) != before {
				t.Fatal("drain ignored pressure")
			}
			w.core.LaunchAdmission = nil
			for range 2 {
				if err := w.core.DrainLaunches(); err != nil {
					t.Fatal(err)
				}
			}
			if len(w.launches.starts) != before+1 {
				t.Fatalf("launched %d workers", len(w.launches.starts)-before)
			}
			if got := w.launches.starts[before]; got.Session != n.Session || got.Source != n.Source || got.Request != pending[0].ID {
				t.Fatalf("request changed: %+v", got)
			}
			if managed {
				assignments, err := w.core.Store.Assignments(g.ID)
				if err != nil || len(assignments) != 1 || assignments[0].ID != pending[0].Launch.Assignment || assignments[0].Session != n.Session {
					t.Fatalf("assignments %+v, %v", assignments, err)
				}
			}
		})
	}
}

func TestDeferredManagerDoesNotAccumulateLaunchFailures(t *testing.T) {
	w := newOrgWorld(t)
	w.core.LaunchAdmission = func(NewLaunch) error { return &LaunchDeferred{Reason: "processor load"} }
	g, m, err := w.core.NewGoalNamed("queued", "work", "/repo/app", "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		w.clock = w.clock.Add(ResumePace)
		w.supervise(t)
	}
	stored, err := w.core.Store.Manager(m.ID)
	if err != nil || stored.Session != "" || stored.Failures != 0 || !strings.Contains(stored.LastError, "queued") {
		t.Fatalf("manager %+v, %v", stored, err)
	}
	goal, err := w.core.Store.Goal(g.ID)
	if err != nil || goal.State != GoalActive {
		t.Fatalf("goal %+v, %v", goal, err)
	}
	if len(w.launches.starts) != 0 {
		t.Fatal("deferred manager launched")
	}
}

func TestConfiguredWorkerHostNeverFallsBackToALocalCheckout(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "available", true: "unavailable"}[unavailable], func(t *testing.T) {
			w := newOrgWorld(t)
			repo := t.TempDir()
			r := &admissionRemotes{fakeRemotes: fakeRemotes{out: "queued remote launch", boards: []RemoteBoard{{Host: "workstation", At: w.clock}}}, offline: unavailable}
			w.core.Remotes = r
			before := len(w.launches.starts)
			_, err := w.core.StartNew(NewLaunch{Host: "workstation", Repo: repo, Prompt: "remote work", Session: "planned", Source: "remote-work"})
			if err != nil && !errors.Is(err, ErrUnreachable) {
				t.Fatal(err)
			}
			if len(w.launches.starts) != before {
				t.Fatal("configured remote work ran locally")
			}
			if unavailable {
				relays, err := w.core.Store.PendingRelays()
				if err != nil || len(relays) == 0 {
					t.Fatalf("remote intent lost: %+v, %v", relays, err)
				}
			} else if len(r.ran) == 0 {
				t.Fatal("workstation was not used")
			}
			if got := w.core.RepoForHost(repo); got != filepath.Base(repo) {
				t.Fatalf("machine path leaked as repository: %s", got)
			}
		})
	}
}

type admissionRemotes struct {
	fakeRemotes
	offline bool
}

func (r *admissionRemotes) Run(host string, args ...string) (string, error) {
	if r.offline {
		return "", ErrUnreachable
	}
	return r.fakeRemotes.Run(host, args...)
}

func TestClosingAGoalCancelsItsQueuedWorkerBeforeCapacityReturns(t *testing.T) {
	w := newOrgWorld(t)
	g, manager := w.goal(t, "work", "/repo/app")
	w.core.LaunchAdmission = func(NewLaunch) error { return &LaunchDeferred{Reason: "memory pressure"} }
	if _, err := w.core.StartNew(NewLaunch{Parent: manager.Session, Repo: "/repo/app", Prompt: "work", Source: "queued-worker", Session: "worker"}); err != nil {
		t.Fatal(err)
	}
	g.State = GoalDropped
	if err := w.core.Store.SaveGoal(g); err != nil {
		t.Fatal(err)
	}
	before := len(w.launches.starts)
	w.core.LaunchAdmission = nil
	if err := w.core.DrainLaunches(); err != nil {
		t.Fatal(err)
	}
	pending, err := w.core.Store.PendingLaunches()
	if err != nil || len(pending) != 0 || len(w.launches.starts) != before {
		t.Fatalf("cancelled work launched: pending %+v, %v", pending, err)
	}
}
