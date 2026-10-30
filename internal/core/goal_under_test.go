package core

import (
	"reflect"
	"strings"
	"testing"
)

func TestAGoalReportsToTheChiefThatStartsItOrTheOneNamed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		caller    string
		under     func(chief, other, mgr Manager) string
		wantBoss  func(chief, other Manager) string
		wantError string
	}{
		{name: "chief caller", caller: "chief", wantBoss: func(chief, _ Manager) string { return chief.ID }},
		{name: "manager caller", caller: "manager", wantBoss: func(Manager, Manager) string { return "" }},
		{name: "owner", caller: "", wantBoss: func(Manager, Manager) string { return "" }},
		{name: "under overrides the caller", caller: "chief", under: func(_, other, _ Manager) string { return other.ID },
			wantBoss: func(_, other Manager) string { return other.ID }},
		{name: "under by name", caller: "", under: func(_, other, _ Manager) string { return other.Name },
			wantBoss: func(_, other Manager) string { return other.ID }},
		{name: "under a chief on another host", caller: "", under: func(Manager, Manager, Manager) string { return "box/chief4" },
			wantBoss: func(Manager, Manager) string { return "box/chief4" }},
		{name: "under a manager", caller: "chief", under: func(_, _, mgr Manager) string { return mgr.ID }, wantError: "only a chief has reports"},
		{name: "under nobody known", caller: "", under: func(Manager, Manager, Manager) string { return "chief99" }, wantError: "no manager, chief or goal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, _ := hostWorld(t)
			_, mgr := w.goal(t, "ship the importer", "/repo/august")
			chief := w.chief(t, "platform")
			other := w.chief(t, "infra")
			session := map[string]string{"chief": chief.Session, "manager": mgr.Session}[tc.caller]
			under := ""
			if tc.under != nil {
				under = tc.under(chief, other, mgr)
			}
			if under == "" && session != "" {
				under = w.core.ChiefOf(session)
			}
			before := len(w.launches.starts)
			g, m, err := w.core.NewGoalUnder("cut CI to five minutes", "/repo/august", "", under)
			if tc.wantError != "" {
				o, _ := w.core.LoadOrg()
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || len(o.Goals) != 1 || len(w.launches.starts) != before {
					t.Fatalf("err %v, goals %d, starts %d; want %q refused before anything is recorded or started", err, len(o.Goals), len(w.launches.starts)-before, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := tc.wantBoss(chief, other); m.ParentID != want {
				t.Fatalf("%s's manager reports to %q, want %q", g.ID, m.ParentID, want)
			}
			prompt := w.launches.starts[len(w.launches.starts)-1].Prompt
			if boss := "goes to the owner"; m.ParentID == "" && !strings.Contains(prompt, boss) || m.ParentID != "" && strings.Contains(prompt, boss) {
				t.Fatalf("manager under %q was told to escalate elsewhere:\n%s", m.ParentID, prompt)
			}
		})
	}
}

func TestAGoalStartedOnAnotherHostCarriesItsChief(t *testing.T) {
	for _, tc := range []struct {
		name  string
		under func(chief Manager) string
		want  func(c *Core, chief Manager) string
	}{
		{"a chief here", func(chief Manager) string { return chief.ID }, func(c *Core, chief Manager) string { return RemoteKey(c.SelfName(), chief.ID) }},
		{"a chief on that host", func(Manager) string { return "box/chief4" }, func(*Core, Manager) string { return "chief4" }},
		{"no chief", func(Manager) string { return "" }, func(*Core, Manager) string { return "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r := hostWorld(t)
			chief := w.chief(t, "platform")
			if _, _, err := w.core.NewGoalUnder("explore the github org", boxRepo, "", tc.under(chief)); err != nil {
				t.Fatal(err)
			}
			want := []string{"box", "goal", "--repo", boxRepo}
			if u := tc.want(w.core, chief); u != "" {
				want = append(want, "--under", u)
			}
			want = append(want, "--", "explore the github org")
			if !reflect.DeepEqual(r.ran, [][]string{want}) {
				t.Fatalf("ran %v, want %v", r.ran, want)
			}
		})
	}
}

func TestAChiefIsToldHowToStartAGoalUnderItself(t *testing.T) {
	w := newOrgWorld(t)
	w.chief(t, "platform")
	prompt := w.launches.starts[len(w.launches.starts)-1].Prompt
	if !strings.Contains(prompt, `ym goal --repo <repo> "<ask>" starts a goal that reports to you`) {
		t.Fatalf("chief prompt lacks how to start a goal under itself:\n%s", prompt)
	}
}
