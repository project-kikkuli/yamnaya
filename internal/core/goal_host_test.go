package core

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// runGoal is ym goal as a relayed command runs it on w: the repository named by --repo resolved there, under --under.
func runGoal(w *orgWorld) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		var repo, under string
		for i := 1; i < len(args)-1; i++ {
			switch args[i] {
			case "--repo":
				repo = args[i+1]
			case "--under":
				under = args[i+1]
			}
		}
		dir, err := w.core.ResolveRepo(repo)
		if err != nil {
			return "", err
		}
		g, _, err := w.core.NewGoalUnder(args[len(args)-1], dir, "", under)
		return g.ID, err
	}
}

// syncRefusing is fleet.sync for relays a host may refuse for good.
func syncRefusing(t *testing.T, f *fleet) {
	t.Helper()
	if err := f.a.core.SyncRelays([]string{"b", "c"}, f.net); err != nil && !errors.Is(err, ErrUnreachable) && !errors.Is(err, ErrFinal) {
		t.Fatal(err)
	}
}

func TestAGoalStartedOnAHostReachedOnlyByRelayRunsThereUnderTheRequesterOrTellsItWhyNot(t *testing.T) {
	for _, tc := range []struct {
		name, repo string
		// chiefAsks files the request as the chief itself; otherwise the owner asks from outside any session.
		chiefAsks bool
		started   bool
	}{
		{name: "a repository the host has, by name", repo: "app", chiefAsks: true, started: true},
		{name: "a repository the host does not have", repo: "nowhere", chiefAsks: true},
		{name: "a repository the host does not have, asked from outside a session", repo: "nowhere"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			roots, _ := filepath.EvalSymlinks(t.TempDir())
			app := filepath.Join(roots, "app")
			if err := os.MkdirAll(filepath.Join(app, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			f.a.core.Roots = func() []string { return []string{roots} }
			runs := 0
			start := runGoal(f.a)
			f.a.core.RunSelf = func(args ...string) (string, error) {
				runs++
				return start(args...)
			}
			chief := f.b.chief(t, "platform")
			requester := ""
			if tc.chiefAsks {
				requester = chief.Session
			}
			host, err := f.b.core.Store.RouteHost("@a")
			if err != nil {
				t.Fatal(err)
			}
			g, err := f.b.core.NewGoalOn(host, "", "ship the fix", tc.repo, "", chief.ID, requester)
			if err != nil || !strings.Contains(g.Evidence, "queued for a") {
				t.Fatalf("NewGoalOn = %+v, %v; want the goal queued for a", g, err)
			}
			for range 3 {
				syncRefusing(t, f)
			}
			if runs != 1 {
				t.Errorf("a ran the goal %d times, want once", runs)
			}
			if pending, _ := f.b.core.Store.PendingRelays(); len(pending) != 0 {
				t.Errorf("b still holds %+v", pending)
			}
			o, err := f.a.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			var started []Manager
			for _, m := range o.Managers {
				if m.Role == RoleManager {
					started = append(started, m)
				}
			}
			items, err := f.b.core.Store.Items(ItemFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if !tc.started {
				if len(started) != 0 {
					t.Errorf("a started %+v for a repository it does not have", started)
				}
				want := Owner
				if tc.chiefAsks {
					want = chief.ID
				}
				if len(items) != 1 || items[0].Recipient != want || !strings.Contains(items[0].Question, `no repository matches "nowhere"`) {
					t.Fatalf("b's items %+v; want one to %q saying a has no such repository", items, want)
				}
				if tc.chiefAsks && !slices.ContainsFunc(f.b.handedTo(chief.Session), func(s string) bool { return strings.Contains(s, items[0].Key()) }) {
					t.Errorf("chief was handed %q, want the item %s", f.b.handedTo(chief.Session), items[0].Key())
				}
				return
			}
			if len(items) != 0 {
				t.Errorf("b has items %+v after the goal started", items)
			}
			if len(started) != 1 || started[0].Repo != app || started[0].ParentID != RemoteKey("b", chief.ID) {
				t.Fatalf("a started %+v; want one manager in a's app under b's chief", started)
			}
			cycle := func() {
				t.Helper()
				if err := f.a.core.Loop(f.a.board(t)); err != nil {
					t.Fatal(err)
				}
				syncRefusing(t, f)
			}
			cycle()
			if _, _, err := f.a.core.RecordGoalEvent(started[0].GoalID, EventUpdate, "first PR up", ""); err != nil {
				t.Fatal(err)
			}
			cycle()
			cycle()
			lines, err := f.b.core.Digest(chief.Session)
			if err != nil || !slices.Contains(lines, RemoteKey("a", started[0].GoalID)+": 1 update") {
				t.Errorf("ym digest on b printed %q, err %v; want a's goal's update", lines, err)
			}
		})
	}
}
