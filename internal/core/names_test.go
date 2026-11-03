package core

import (
	"strings"
	"testing"
)

func TestAGoalIsNamedByItsNamerElseByItsAsksFirstWords(t *testing.T) {
	for _, tc := range []struct {
		name, ask, namer, want string
	}{
		{name: "the namer's slug", ask: "make the export endpoint stream", namer: "Stream-Export Endpoint\n", want: "stream-export-endpoint"},
		{name: "no namer reply", ask: "Make the export endpoint stream instead of buffering", want: "make-the-export-endpoint"},
		{name: "a namer reply with nothing usable", ask: "fix the board's flicker", namer: "¯\\_(ツ)_/¯", want: "fix-the-board-s"},
		{name: "an ask with nothing usable", ask: "¿¡…!?", want: "goal"},
		{name: "a namer reply shaped like an id", ask: "ship it now", namer: "goal7", want: "ship-it-now"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Namer = func(string) string { return tc.namer }
			g, m := w.goal(t, tc.ask, "/repo/august")
			if g.Name != tc.want || m.Name != tc.want {
				t.Fatalf("goal %q, manager %q, want both %q", g.Name, m.Name, tc.want)
			}
		})
	}
}

func TestANameAlreadyUsedHereGetsTheFirstFreeNumericSuffix(t *testing.T) {
	w := newOrgWorld(t)
	w.core.Namer = func(string) string { return "fix-export" }
	w.chief(t, "fix-export-2")
	var got []string
	for range 3 {
		g, _ := w.goal(t, "anything", "/repo/august")
		got = append(got, g.Name)
	}
	if want := "fix-export fix-export-3 fix-export-4"; strings.Join(got, " ") != want {
		t.Fatalf("names %q, want %q", got, want)
	}
	if _, _, err := w.core.NewGoalNamed("fix-export-3", "again", "/repo/august", "", ""); err == nil {
		t.Fatal("a --name already in use was accepted")
	}
}

func TestEveryRefNamesAGoalByItsNameOrEitherId(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "make the export endpoint stream", "/repo/august")
	boss := w.chief(t, "arbitrator", m.ID)
	if err := w.core.Attach(g.ID, ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ ref, want string }{
		{g.Name, m.ID}, {strings.ToUpper(g.Name), m.ID}, {g.ID, m.ID}, {m.ID, m.ID}, {"arbitrator", boss.ID}, {boss.ID, boss.ID},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			o, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			got, err := o.Lookup(tc.ref)
			if err != nil || got.ID != tc.want {
				t.Fatalf("Lookup(%q) = %s, %v; want %s", tc.ref, got.ID, err, tc.want)
			}
		})
	}
}

func TestRenameTakesAValidFreeNameAndKeepsTheOldRefsWorking(t *testing.T) {
	for _, tc := range []struct {
		name, ref, to, err string
	}{
		{name: "a goal by its name", ref: "stream-export", to: "export-streaming"},
		{name: "a goal by its id", ref: "goal1", to: "export-streaming"},
		{name: "a chief", ref: "arbitrator", to: "arbiter-chief"},
		{name: "taken by a chief", ref: "stream-export", to: "arbitrator", err: "already names"},
		{name: "taken by another goal", ref: "arbitrator", to: "other-goal", err: "already names"},
		{name: "not kebab-case", ref: "stream-export", to: "Export Streaming", err: "kebab-case"},
		{name: "too short", ref: "stream-export", to: "x", err: "2 to 40"},
		{name: "shaped like an id", ref: "stream-export", to: "mgr9", err: "reads as an id"},
		{name: "its own name again", ref: "stream-export", to: "stream-export"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Namer = func(string) string { return "stream-export" }
			g, m := w.goal(t, "make the export endpoint stream", "/repo/august")
			w.core.Namer = func(string) string { return "other-goal" }
			w.goal(t, "another", "/repo/august")
			boss := w.chief(t, "arbitrator")
			o, _ := w.core.LoadOrg()
			before, _ := o.Lookup(tc.ref)
			_, err := w.core.Rename(tc.ref, tc.to)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("Rename(%q, %q) = %v, want an error saying %q", tc.ref, tc.to, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			o, _ = w.core.LoadOrg()
			for _, ref := range []string{tc.to, before.ID} {
				if got, err := o.Lookup(ref); err != nil || got.ID != before.ID || got.Shown() != tc.to {
					t.Fatalf("Lookup(%q) = %s shown %q, %v; want %s shown %q", ref, got.ID, got.Shown(), err, before.ID, tc.to)
				}
			}
			if before.ID == m.ID {
				if got := o.Goals[g.ID].Shown(); got != tc.to {
					t.Fatalf("goal shows %q, want %q", got, tc.to)
				}
			} else if before.ID != boss.ID {
				t.Fatalf("renamed %s, want %s", before.ID, boss.ID)
			}
		})
	}
}

func TestNamingUnnamedGoalsAndChiefsLeavesNamedOnesAlone(t *testing.T) {
	w := newOrgWorld(t)
	w.core.Namer = func(string) string { return "kept-name" }
	kept, _ := w.goal(t, "already named", "/repo/august")
	old, oldM := w.goal(t, "Fix flaky CI on main", "/repo/august")
	old.Name, oldM.Name = "", "Fix flaky CI on main"
	if err := w.core.Store.SaveGoal(old); err != nil {
		t.Fatal(err)
	}
	if err := w.core.Store.SaveManager(oldM); err != nil {
		t.Fatal(err)
	}
	boss := w.chief(t, "yamnaya-manager")
	boss.Name = "Yamnaya Manager"
	if err := w.core.Store.SaveManager(boss); err != nil {
		t.Fatal(err)
	}
	w.core.Namer = func(string) string { return "" }
	n, err := w.core.NameUnnamed()
	if err != nil || n != 2 {
		t.Fatalf("named %d, %v; want 2", n, err)
	}
	o, _ := w.core.LoadOrg()
	for _, tc := range []struct{ id, want string }{{kept.ManagerID, "kept-name"}, {oldM.ID, "fix-flaky-ci-on"}, {boss.ID, "yamnaya-manager"}} {
		if got := o.Managers[tc.id].Shown(); got != tc.want {
			t.Errorf("%s shows %q, want %q", tc.id, got, tc.want)
		}
	}
	if got := o.Goals[old.ID].Name; got != "fix-flaky-ci-on" {
		t.Errorf("goal %s named %q, want fix-flaky-ci-on", old.ID, got)
	}
}

func TestAnotherHostsGoalIsRenamedOnItsHostAndShowsByTheNameItsHostReports(t *testing.T) {
	for _, tc := range []struct {
		name, to string
		ran      []string
		err      string
	}{
		{name: "a valid name runs on its host", to: "faster-login", ran: []string{"goal", "rename", "mgr3", "faster-login"}},
		{name: "an invalid name never leaves", to: "Faster Login", err: "kebab-case"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			r := &fakeRemotes{boards: []RemoteBoard{{Host: "box",
				Managers: []Manager{{ID: "mgr3", Role: RoleManager, Name: "make-login-faster", GoalID: "goal2"}},
				Goals:    []Goal{{ID: "goal2", Name: "make-login-faster", Ask: "make login faster", ManagerID: "mgr3"}}}}}
			w.core.Remotes, w.core.ShowRemotes = r, true
			w.core.Namer = func(string) string { return "make-login-faster" }
			w.goal(t, "make login faster here too", "/repo/august")
			b := w.board(t)
			o := b.Org
			for ref, want := range map[string]string{"box/make-login-faster": "box/mgr3", "make-login-faster": "mgr1"} {
				if got, err := o.Lookup(ref); err != nil || got.ID != want {
					t.Fatalf("Lookup(%q) = %s, %v; want %s", ref, got.ID, err, want)
				}
			}
			n := node(t, b, "box/mgr3")
			if n.Goal.Ref() != "box/make-login-faster" || n.Manager.Ref() != "box/make-login-faster" {
				t.Fatalf("another host's goal shows as %q, its manager %q; want both box/make-login-faster", n.Goal.Ref(), n.Manager.Ref())
			}
			_, err := w.core.Rename("box/mgr3", tc.to)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) || len(r.ran) != 0 {
					t.Fatalf("Rename = %v, ran %q; want an error saying %q and nothing run", err, r.ran, tc.err)
				}
				return
			}
			if err != nil || len(r.ran) != 1 || strings.Join(r.ran[0], " ") != "box "+strings.Join(tc.ran, " ") {
				t.Fatalf("Rename = %v, ran %q; want %q on box", err, r.ran, tc.ran)
			}
		})
	}
}
