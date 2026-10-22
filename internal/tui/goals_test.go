package tui

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func goalSetup(t *testing.T, asks ...string) (Model, *core.Core, []core.Manager) {
	t.Helper()
	m, c, _ := setup(t)
	c.Launches = &launches{}
	var ms []core.Manager
	for _, ask := range asks {
		_, mg, err := c.NewGoal(ask, t.TempDir(), "")
		if err != nil {
			t.Fatal(err)
		}
		ms = append(ms, mg)
	}
	return refresh(t, m), c, ms
}

func isNode(id string) func(row) bool {
	return func(r row) bool { return r.node != nil && r.session == nil && r.node.Manager.ID == id }
}

// told lists the sessions that received text, among the given managers'.
func told(t *testing.T, c *core.Core, text string, ms ...core.Manager) []string {
	t.Helper()
	var out []string
	for _, mg := range ms {
		latest, err := c.Store.Manager(mg.ID)
		if err != nil {
			t.Fatal(err)
		}
		msgs, err := c.Store.Messages(latest.Session)
		if err != nil {
			t.Fatal(err)
		}
		for _, msg := range msgs {
			if strings.Contains(msg.Text, text) {
				out = append(out, mg.ID)
			}
		}
	}
	return out
}

func TestAChiefCollapsesToOneRowAndDrillsIntoItsGoals(t *testing.T) {
	m, c, ms := goalSetup(t, "make the importer handle CSV", "rotate the signing keys")
	chief, err := c.NewChief("platform", "", t.TempDir(), []string{ms[0].ID, ms[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	m = refresh(t, m)
	var nodes []string
	for _, r := range m.rows() {
		if r.node != nil {
			nodes = append(nodes, r.node.Manager.ID)
		}
	}
	if !slices.Equal(nodes, []string{chief.ID}) {
		t.Fatalf("goal rows %v, want the chief alone standing for both goals", nodes)
	}
	m = press(t, to(t, m, isNode(chief.ID)), "enter")
	if m.mode != modeChief || len(m.rows()) != 2 || !strings.Contains(m.View(), "REPORTS (2)") {
		t.Fatalf("enter on the chief should list its two goals, mode %v:\n%s", m.mode, m.View())
	}
	m = press(t, to(t, m, isNode(ms[1].ID)), "enter")
	if m.mode != modeGoal || !strings.Contains(m.View(), "rotate the signing keys") || !strings.Contains(m.View(), ms[1].ID) {
		t.Fatalf("enter on a report should open its goal, mode %v:\n%s", m.mode, m.View())
	}
	m = press(t, m, "esc")
	if m.mode != modeChief {
		t.Fatalf("esc from the goal went to mode %v, want the chief", m.mode)
	}
	if m = press(t, m, "esc"); m.mode != modeList {
		t.Fatalf("esc from the chief went to mode %v, want the board", m.mode)
	}
}

func TestSendReachesEverySelectedManagerAndTabWidensAChiefToItsSubtree(t *testing.T) {
	t.Run("selected goals", func(t *testing.T) {
		m, c, ms := goalSetup(t, "goal one", "goal two", "goal three")
		m = press(t, to(t, m, isNode(ms[0].ID)), " ")
		m = press(t, to(t, m, isNode(ms[2].ID)), " ")
		m = press(t, m, "s")
		m = press(t, m, strings.Split("focus on latency", "")...)
		press(t, m, "enter")
		if got := told(t, c, "focus on latency", ms...); !slices.Equal(got, []string{ms[0].ID, ms[2].ID}) {
			t.Fatalf("sent to %v, want the two selected managers", got)
		}
	})
	for _, tc := range []struct {
		name string
		keys []string
		want int
	}{
		{"chief alone", nil, 1},
		{"chief and subtree", []string{"tab"}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c, ms := goalSetup(t, "goal one", "goal two")
			chief, err := c.NewChief("lead", "", t.TempDir(), []string{ms[0].ID, ms[1].ID})
			if err != nil {
				t.Fatal(err)
			}
			m = press(t, to(t, refresh(t, m), isNode(chief.ID)), "s")
			m = press(t, m, tc.keys...)
			m = press(t, m, strings.Split("ship by friday", "")...)
			press(t, m, "enter")
			if got := told(t, c, "ship by friday", append(ms, chief)...); len(got) != tc.want {
				t.Fatalf("sent to %v, want %d", got, tc.want)
			}
		})
	}
}

func TestBShowsTheBriefAndVOpensTheArtifact(t *testing.T) {
	m, c, ms := goalSetup(t, "make the importer handle CSV")
	g, err := c.Store.Goal(ms[0].GoalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g.BriefPath, []byte("# Importer\n\nCSV parser merged; TSV next.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.SetArtifact(ms[0].Session, "https://claude.ai/artifact/importer"); err != nil {
		t.Fatal(err)
	}
	var opened []string
	m.openURL = func(url string) error { opened = append(opened, url); return nil }
	m = to(t, refresh(t, m), isNode(ms[0].ID))
	shown := press(t, m, "B")
	if shown.mode != modeScreen || !strings.Contains(shown.View(), "CSV parser merged; TSV next.") {
		t.Fatalf("B should show the brief, mode %v:\n%s", shown.mode, shown.View())
	}
	press(t, m, "V")
	if !slices.Equal(opened, []string{"https://claude.ai/artifact/importer"}) {
		t.Fatalf("V opened %v, want the registered artifact", opened)
	}
}

func TestNHandsAGoalToAManagerInTheRowsRepository(t *testing.T) {
	m, c, _ := setup(t)
	l := &launches{}
	c.Launches = l
	m = to(t, place(t, m, c), mission("lead"))
	m = press(t, m, "n")
	m = press(t, m, strings.Split("make login faster", "")...)
	m = press(t, m, "enter")
	if len(l.started) != 1 || l.started[0].Repo != "/src/app" || !strings.Contains(l.started[0].Prompt, "make login faster") {
		t.Fatalf("started %s, want one manager in /src/app on the goal", fmtLaunches(l.started))
	}
	if !strings.HasPrefix(m.message, "goal1 started") {
		t.Fatalf("message %q", m.message)
	}
	if m = refresh(t, m); !slices.ContainsFunc(m.rows(), func(r row) bool { return r.node != nil && r.node.Goal.Ask == "make login faster" }) {
		t.Fatalf("the new goal is not on the board:\n%s", m.View())
	}
}
