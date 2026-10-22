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
	if m.mode != modeChief || len(m.rows()) != 2 || !strings.Contains(m.View().Content, "REPORTS (2)") {
		t.Fatalf("enter on the chief should list its two goals, mode %v:\n%s", m.mode, m.View().Content)
	}
	m = press(t, to(t, m, isNode(ms[1].ID)), "enter")
	if m.mode != modeGoal || !strings.Contains(m.View().Content, "rotate the signing keys") || !strings.Contains(m.View().Content, ms[1].ID) {
		t.Fatalf("enter on a report should open its goal, mode %v:\n%s", m.mode, m.View().Content)
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
	if shown.mode != modeScreen || !strings.Contains(shown.View().Content, "CSV parser merged; TSV next.") {
		t.Fatalf("B should show the brief, mode %v:\n%s", shown.mode, shown.View().Content)
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
		t.Fatalf("the new goal is not on the board:\n%s", m.View().Content)
	}
}

func TestADocShowsOneRowForItsThreadsOpensThePageAndDismisses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		threads int
		head    string
		opens   string
	}{
		{"one thread", 1, "Alice: @Rajat is PR 7 the final cut?", "https://www.notion.so/Launch-plan-a0?d=d1"},
		{"two threads", 2, "Launch plan 2 open threads", "https://www.notion.so/Launch-plan-a0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c, _ := goalSetup(t)
			asks := []core.DocAsk{{Key: "notion:d1", Kind: "mention", URL: "https://www.notion.so/Launch-plan-a0?d=d1", Page: "a0", Title: "Launch plan",
				Asker: "Alice", Text: "@Rajat is PR 7 the final cut?", Evidence: "Alice: @Rajat is PR 7 the final cut?", Version: "c1", At: "2026-10-22T10:00:00Z"},
				{Key: "notion:d2", Kind: "reply", URL: "https://www.notion.so/Launch-plan-a0?d=d2", Page: "a0", Title: "Launch plan",
					Asker: "Bo", Text: "lgtm", Evidence: "you: ok?\nBo: lgtm", Version: "c2", At: "2026-10-21T10:00:00Z"}}[:tc.threads]
			if err := c.Store.SyncDocPage("notion", "a0", "e1", asks); err != nil {
				t.Fatal(err)
			}
			b, err := c.Board()
			if err != nil {
				t.Fatal(err)
			}
			if err := c.RouteDocs(b); err != nil {
				t.Fatal(err)
			}
			var opened []string
			m.openURL = func(u string) error { opened = append(opened, u); return nil }
			isDoc := func(r row) bool { return r.need != nil && r.need.Doc != nil }
			m = refresh(t, m)
			rows := 0
			for _, r := range m.rows() {
				if isDoc(r) {
					rows++
				}
			}
			if rows != 1 {
				t.Fatalf("%d doc rows, want one for the page", rows)
			}
			for range 30 {
				m = press(t, m, "up")
			}
			m = to(t, m, isDoc)
			v := m.View().Content
			if !strings.Contains(v, tc.head) {
				t.Fatalf("the doc row does not read %q:\n%s", tc.head, v)
			}
			for _, a := range asks {
				if !strings.Contains(v, a.URL) || !strings.Contains(v, a.Asker+": "+a.Text) {
					t.Fatalf("the open row hides thread %s:\n%s", a.Key, v)
				}
			}
			press(t, m, "enter")
			if !slices.Equal(opened, []string{tc.opens}) {
				t.Fatalf("enter opened %v, want %s", opened, tc.opens)
			}
			m = refresh(t, press(t, m, "x"))
			if slices.ContainsFunc(m.rows(), isDoc) {
				t.Fatal("a dismissed doc still shows")
			}
		})
	}
}

func TestAGoalRowShowsUnreadNewsAndEnterOpensTheFeedAndReadsIt(t *testing.T) {
	m, c, ms := goalSetup(t, "make the importer handle CSV")
	g := ms[0].GoalID
	for _, e := range []core.GoalEvent{
		{Goal: g, Kind: core.EventFinished, Payload: "agent abc is idle"},
		{Goal: g, Kind: core.EventPROpened, Payload: "agent abc opened #7"},
		{Goal: g, Kind: core.EventUpdate, Payload: "parser merged; TSV next"},
	} {
		if _, _, err := c.Store.AddGoalEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	m = refresh(t, m)
	if v := m.View().Content; !strings.Contains(v, "2 new") || !strings.Contains(v, "parser merged; TSV next") {
		t.Fatalf("the goal row should show 2 unread and the newest line:\n%s", v)
	}
	m = press(t, to(t, m, isNode(ms[0].ID)), "enter")
	v := m.View().Content
	if m.mode != modeGoal || !strings.Contains(v, "FEED") || !strings.Contains(v, "agent abc opened #7") || !strings.Contains(v, "agent abc is idle") {
		t.Fatalf("enter should open the goal with its feed:\n%s", v)
	}
	feeds, err := c.Store.Feeds()
	if err != nil {
		t.Fatal(err)
	}
	if feeds[g].Unread != 0 {
		t.Fatalf("opening the feed left %d unread", feeds[g].Unread)
	}
	m = refresh(t, press(t, m, "esc"))
	if strings.Contains(m.View().Content, "2 new") {
		t.Fatalf("the row still shows unread news after reading it:\n%s", m.View().Content)
	}
}

func TestAGoalRowFlagsAnUnadoptedSteerAndTheSelectedRowItsBrief(t *testing.T) {
	m, c, ms := goalSetup(t, "make the importer handle CSV")
	m = to(t, m, isNode(ms[0].ID))
	if v := m.View().Content; !strings.Contains(v, "no brief yet") || strings.Contains(v, "not adopted") {
		t.Fatalf("a fresh goal row should say it has no brief and no steer:\n%s", v)
	}
	st, err := c.SteerContext(m.board, ms[0].ID, "CSV and TSV", core.Owner)
	if err != nil {
		t.Fatal(err)
	}
	if v := refresh(t, m).View().Content; !strings.Contains(v, "steer r1 not adopted") {
		t.Fatalf("the row does not flag the unadopted steer:\n%s", v)
	}
	if _, err := c.Adopt(ms[0].Session, "", st.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WriteBrief(ms[0].Session, core.Brief{Assessment: "TSV next", Changed: "adopted r1", Uncertainty: "none", Next: "TSV", WaitingOn: "none"}); err != nil {
		t.Fatal(err)
	}
	if v := refresh(t, m).View().Content; strings.Contains(v, "not adopted") || !strings.Contains(v, "brief v1") {
		t.Fatalf("after adopting and briefing, the row should show brief v1 and no steer flag:\n%s", v)
	}
}
