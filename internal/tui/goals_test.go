package tui

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

func TestTheTreeNestsEveryReportUnderItsChiefWithItsBriefAndEnterDrillsIn(t *testing.T) {
	m, c, ms := goalSetup(t, "make the importer handle CSV", "rotate the signing keys", "page on call")
	platform, err := c.NewChief("platform", "", t.TempDir(), "", []string{ms[0].ID, ms[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	lead, err := c.NewChief("lead", "", t.TempDir(), "", []string{platform.ID, ms[2].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.WriteBrief(ms[0].Session, core.Brief{Assessment: "CSV parser merged", Changed: "parser", Uncertainty: "none", Next: "TSV", WaitingOn: "none"}); err != nil {
		t.Fatal(err)
	}
	m = refresh(t, m)
	depth := map[string]int{}
	lines := map[string]string{}
	for _, r := range m.rows() {
		if r.node != nil {
			depth[r.node.Manager.ID] = r.depth
			lines[r.node.Manager.ID] = ansi.Strip(m.nodeLines(-1, *r.node, r.depth))
		}
	}
	want := map[string]int{lead.ID: 0, platform.ID: 1, ms[0].ID: 2, ms[1].ID: 2, ms[2].ID: 1}
	if !maps.Equal(depth, want) {
		t.Fatalf("tree depths %v, want %v", depth, want)
	}
	for id, tag := range map[string]string{ms[0].ID: "brief now", ms[1].ID: "no brief", lead.ID: "no brief"} {
		if !strings.Contains(lines[id], tag) || !strings.Contains(lines[id], standingOf(t, m, id)) {
			t.Fatalf("row %s lacks its standing or %q:\n%s", id, tag, lines[id])
		}
	}
	m = press(t, to(t, m, isNode(platform.ID)), "enter")
	if m.mode != modeChief || len(m.rows()) != 2 || !strings.Contains(m.View().Content, "REPORTS (2)") {
		t.Fatalf("enter on the chief should list its two goals, mode %v:\n%s", m.mode, m.View().Content)
	}
	m = press(t, to(t, m, isNode(ms[1].ID)), "enter")
	if m.mode != modeGoal || !strings.Contains(m.View().Content, "rotate the signing keys") {
		t.Fatalf("enter on a report should open its goal, mode %v:\n%s", m.mode, m.View().Content)
	}
	if m = press(t, m, "esc", "esc"); m.mode != modeList {
		t.Fatalf("esc twice went to mode %v, want the board", m.mode)
	}
}

func standingOf(t *testing.T, m Model, id string) string {
	t.Helper()
	n, ok := m.board.Node(id)
	if !ok {
		t.Fatalf("%s is not on the board", id)
	}
	return n.Standing
}

// toOpt moves the picker's cursor to id.
func toOpt(t *testing.T, m Model, id string) Model {
	t.Helper()
	for range len(m.opts) {
		m = press(t, m, "up")
	}
	for range len(m.opts) {
		if m.opts[m.optCur].id == id {
			return m
		}
		m = press(t, m, "down")
	}
	t.Fatalf("%q is not in the picker:\n%s", id, m.View().Content)
	return m
}

func parentOf(t *testing.T, c *core.Core, id string) string {
	t.Helper()
	mg, err := c.Store.Manager(id)
	if err != nil {
		t.Fatal(err)
	}
	return mg.ParentID
}

func TestCPicksTheNewChiefsReportsThenItsNameAndRemit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		selects []int
		checks  []int
		remit   string
		want    []int
	}{
		{"selected rows start checked", []int{0, 2}, nil, "ship the importer", []int{0, 2}},
		{"space checks and unchecks in the picker", []int{0}, []int{0, 1}, "", []int{1}},
		{"over none", nil, nil, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c, ms := goalSetup(t, "goal one", "goal two", "goal three")
			for _, i := range tc.selects {
				m = press(t, to(t, m, isNode(ms[i].ID)), " ")
			}
			m = press(t, m, "C")
			for _, i := range tc.checks {
				m = press(t, toOpt(t, m, ms[i].ID), " ")
			}
			m = press(t, m, "enter")
			m = press(t, m, strings.Split("data team", "")...)
			m = press(t, m, "enter")
			m = press(t, m, strings.Split(tc.remit, "")...)
			m = press(t, m, "enter")
			m = refresh(t, m)
			var chief core.Node
			for _, n := range m.board.Tree {
				if n.Goal == nil {
					chief = n
				}
			}
			if chief.Manager.Repo != "" || chief.Manager.Directory == "" {
				t.Fatalf("chief inherited a repository: %+v", chief.Manager)
			}
			if chief.Manager.Name != "data team" {
				t.Fatalf("no chief named data team on the board; message %q", m.message)
			}
			for i, mg := range ms {
				if got, want := parentOf(t, c, mg.ID), slices.Contains(tc.want, i); (got == chief.Manager.ID) != want {
					t.Fatalf("%s reports to %q, want under the chief %v", mg.ID, got, want)
				}
			}
			brief, err := os.ReadFile(chief.Manager.BriefPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(brief), "# data team\n\n"+tc.remit+"\n") {
				t.Fatalf("the chief's brief does not hold its remit %q:\n%s", tc.remit, brief)
			}
		})
	}
}

func TestCPicksTheChiefToReportToAndDDetaches(t *testing.T) {
	for _, tc := range []struct {
		name   string
		row    func(ms []core.Manager, x, y core.Manager) string
		keys   func(ms []core.Manager, x, y core.Manager) (string, []string)
		parent func(ms []core.Manager, x, y core.Manager) string
	}{
		{"a goal under a chief",
			func(ms []core.Manager, x, y core.Manager) string { return ms[1].ID },
			func(ms []core.Manager, x, y core.Manager) (string, []string) { return x.ID, []string{"enter"} },
			func(ms []core.Manager, x, y core.Manager) string { return x.ID }},
		{"a chief under another",
			func(ms []core.Manager, x, y core.Manager) string { return x.ID },
			func(ms []core.Manager, x, y core.Manager) (string, []string) { return y.ID, []string{"enter"} },
			func(ms []core.Manager, x, y core.Manager) string { return y.ID }},
		{"you detaches",
			func(ms []core.Manager, x, y core.Manager) string { return ms[0].ID },
			func(ms []core.Manager, x, y core.Manager) (string, []string) { return "", []string{"enter"} },
			func(ms []core.Manager, x, y core.Manager) string { return "" }},
		{"esc keeps it",
			func(ms []core.Manager, x, y core.Manager) string { return ms[0].ID },
			func(ms []core.Manager, x, y core.Manager) (string, []string) { return y.ID, []string{"esc"} },
			func(ms []core.Manager, x, y core.Manager) string { return x.ID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c, ms := goalSetup(t, "goal one", "goal two")
			x, err := c.NewChief("x", "", t.TempDir(), "", []string{ms[0].ID})
			if err != nil {
				t.Fatal(err)
			}
			y, err := c.NewChief("y", "", t.TempDir(), "", nil)
			if err != nil {
				t.Fatal(err)
			}
			z, err := c.NewChief("z", "", t.TempDir(), "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Attach(z.ID, x.ID); err != nil {
				t.Fatal(err)
			}
			id := tc.row(ms, x, y)
			m = press(t, to(t, refresh(t, m), isNode(id)), "c", "a")
			for _, o := range m.opts {
				if n, ok := m.board.Node(o.id); o.id == id || ok && n.Goal != nil || o.id == z.ID && id == x.ID {
					t.Fatalf("the picker for %s offers %s, which is itself, a goal or below it", id, o.id)
				}
			}
			title := ansi.Strip(strings.SplitN(m.optsView(), "\n", 2)[0])
			if strings.Contains(title, "PUT mgr") || !strings.Contains(title, "UNDER  "+id) {
				t.Fatalf("picker title %q should name %s as the owner knows it, its manager id after", title, id)
			}
			t.Log(title)
			pick, keys := tc.keys(ms, x, y)
			m = press(t, toOpt(t, m, pick), keys...)
			if got, want := parentOf(t, c, id), tc.parent(ms, x, y); got != want {
				t.Fatalf("%s reports to %q, want %q", id, got, want)
			}
		})
	}
	for _, tc := range []struct {
		keys   []string
		answer string
		parent bool
	}{{[]string{"D"}, "y", false}, {[]string{"D"}, "n", true}, {[]string{"c", "d"}, "y", false}, {[]string{"c", "down", "enter"}, "y", false}} {
		t.Run(strings.Join(tc.keys, " ")+" answered "+tc.answer, func(t *testing.T) {
			m, c, ms := goalSetup(t, "goal one")
			x, err := c.NewChief("x", "", t.TempDir(), "", []string{ms[0].ID})
			if err != nil {
				t.Fatal(err)
			}
			m = press(t, to(t, refresh(t, m), isNode(ms[0].ID)), tc.keys...)
			if m.mode != modeConfirm || !strings.Contains(m.ask, "detach") {
				t.Fatalf("%v should ask before detaching, mode %v ask %q", tc.keys, m.mode, m.ask)
			}
			press(t, m, tc.answer)
			if got := parentOf(t, c, ms[0].ID); (got == x.ID) != tc.parent {
				t.Fatalf("after %s, %s reports to %q", tc.answer, ms[0].ID, got)
			}
		})
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
			chief, err := c.NewChief("lead", "", t.TempDir(), "", []string{ms[0].ID, ms[1].ID})
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
	shown := press(t, m, "b")
	if shown.mode != modeScreen || !strings.Contains(shown.View().Content, "CSV parser merged; TSV next.") {
		t.Fatalf("b should show the brief, mode %v:\n%s", shown.mode, shown.View().Content)
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
	m = press(t, m, "N")
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

func TestTabPicksTheHarnessOfAGoalOrChiefAndTheNextOneStartsOnIt(t *testing.T) {
	goal, chief := []string{"N"}, []string{"c", "n", "enter"}
	for _, c := range []struct {
		name         string
		open, finish []string
		tabs         int
		want         string
	}{
		{"goal on claude", goal, nil, 0, "claude"},
		{"goal on codex", goal, nil, 1, "codex"},
		{"goal picked back to claude", goal, nil, 2, "claude"},
		{"chief on codex", chief, []string{"enter"}, 1, "codex"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, k, _ := setup(t)
			l := &launches{}
			k.Launches = l
			m = to(t, place(t, m, k), mission("lead"))
			m = press(t, m, c.open...)
			for range c.tabs {
				m = press(t, m, "tab")
			}
			if v := ansi.Strip(strings.Join(m.footer(), "\n")); !strings.Contains(v, "new "+c.want+" ") || !strings.Contains(v, "[tab: "+core.Other(c.want)+"]") {
				t.Fatalf("prompt does not show %s and the switch:\n%s", c.want, v)
			}
			m = press(t, m, strings.Split("ship it", "")...)
			m = press(t, m, append([]string{"enter"}, c.finish...)...)
			if len(l.started) != 1 || l.started[0].Harness != c.want {
				t.Fatalf("started %s, want one on %s", fmtLaunches(l.started), c.want)
			}
			if m = press(t, refresh(t, m), c.open...); m.newL.Harness != c.want {
				t.Fatalf("the next %s starts on %s, want %s as last time", c.open, m.newL.Harness, c.want)
			}
		})
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
	next, _ := refresh(t, m).Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)
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

func TestADoneGoalStaysOffNeedsYouAndReportsOnTheTreeUntilXClearsIt(t *testing.T) {
	m, c, ms := goalSetup(t, "make the importer handle CSV")
	g, err := c.Store.Goal(ms[0].GoalID)
	if err != nil {
		t.Fatal(err)
	}
	g.State, g.Summary, g.DoneAt = core.GoalDone, "importer reads CSV, PR 7 merged", time.Now()
	if err := c.Store.SaveGoal(g); err != nil {
		t.Fatal(err)
	}
	m = refresh(t, m)
	if slices.ContainsFunc(m.rows(), func(r row) bool { return r.need != nil && r.need.Goal != nil }) {
		t.Fatalf("a done goal waits in NEEDS YOU:\n%s", m.View().Content)
	}
	m = to(t, m, isNode(ms[0].ID))
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, "done: importer reads CSV, PR 7 merged") || !strings.Contains(v, "x clear") {
		t.Fatalf("the done goal's row hides its summary or how to clear it:\n%s", v)
	}
	m = refresh(t, press(t, m, "x"))
	if slices.ContainsFunc(m.rows(), isNode(ms[0].ID)) {
		t.Fatal("x left the done goal on the tree")
	}
}

func TestAReviewRowNamesThePullRequestAndWhoOpenedIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  core.ReviewRequest
		want string
	}{
		{"titled", core.ReviewRequest{URL: "https://github.com/acme/august/pull/7", Updated: "1", Title: "Read CSV", Author: "mira"},
			"Read CSV by mira https://github.com/acme/august/pull/7"},
		{"read before titles were kept", core.ReviewRequest{URL: "https://github.com/acme/august/pull/7", Updated: "1"}, "https://github.com/acme/august/pull/7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c, _ := goalSetup(t)
			if err := c.Store.SyncReviews([]core.ReviewRequest{tc.req}); err != nil {
				t.Fatal(err)
			}
			m = to(t, press(t, refresh(t, m), slices.Repeat([]string{"up"}, 30)...), func(r row) bool { return r.need != nil && r.need.Review != nil })
			if v := ansi.Strip(m.View().Content); !strings.Contains(v, tc.want) {
				t.Fatalf("the review row does not read %q:\n%s", tc.want, v)
			}
		})
	}
}

func TestAQueueRowForAManagersDutyNamesItsGoal(t *testing.T) {
	m := New(&core.Core{})
	m.board = core.Board{Tree: []core.Node{{Manager: core.Manager{ID: "mgr1"}, Goal: &core.Goal{ID: "goal1", Ask: "make the importer handle CSV"}}}}
	m.loaded, m.width = true, 200
	pr := core.PR{URL: "https://github.com/acme/august/pull/7", Status: &core.PRStatus{Title: "Read CSV"}}
	for _, tc := range []struct {
		name string
		need core.Need
		want string
	}{
		{"merge by a manager", core.Need{PR: &pr, Manager: "mgr1"}, "goal1 (make the importer handle CSV): Read CSV"},
		{"merge by the owner's session", core.Need{PR: &pr}, "merge    Read CSV"},
		{"unadopted steer", core.Need{Steer: &core.Steer{Manager: "mgr1", Revision: 2, Nudges: 2, Text: "ship TSV too"}, Manager: "mgr1"},
			"goal1 (make the importer handle CSV) has not adopted r2 after 2 nudges: ship TSV too"},
		{"steer of a manager gone from the tree", core.Need{Steer: &core.Steer{Manager: "mgr9", Revision: 1, Text: "stop"}, Manager: "mgr9"}, "mgr9 has not adopted r1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, ok := m.queueLines(1, tc.need)
			if line = ansi.Strip(line); !ok || !strings.Contains(line, tc.want) {
				t.Fatalf("row %q, want it to read %q", line, tc.want)
			}
		})
	}
}

func TestAGoalRowAlwaysShowsItsVibeAndDropsElapsedThenSpendWhenNarrow(t *testing.T) {
	for _, width := range []int{80, 120, 140, 240, 320} {
		for _, mountains := range []bool{true, false} {
			t.Run(fmt.Sprintf("%d wide, mountains %v", width, mountains), func(t *testing.T) {
				m, _, ms := goalSetup(t, "make the importer handle CSV")
				next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 50})
				m = next.(Model)
				m.peaks.hidden = !mountains
				spend := core.Spend{USD: 6.5}
				b := m.board
				b.Tree = slices.Clone(b.Tree)
				for i := range b.Tree {
					if b.Tree[i].Manager.ID == ms[0].ID {
						b.Tree[i].Stats = core.Stats{Vibe: &core.Vibe{Place: "Reykjavik", At: b.At}, Running: 1, Spend: &spend, Started: b.At.Add(-3 * time.Hour)}
					}
				}
				b.Gen++
				next, _ = m.Update(boardMsg{board: b})
				m = next.(Model)
				row := ""
				for _, l := range strings.Split(ansi.Strip(m.View().Content), "\n") {
					if strings.Contains(l, "importer") {
						row = l
					}
				}
				if !strings.Contains(row, "Reykjavik") {
					t.Fatalf("goal row %q lacks its vibe", row)
				}
				if width >= 240 && !strings.Contains(row, "$6.50") {
					t.Errorf("wide goal row %q lacks its spend", row)
				}
				if width >= 240 && !mountains && !strings.Contains(row, "3h") {
					t.Errorf("wide goal row without mountains %q lacks its elapsed time", row)
				}
			})
		}
	}
}

func TestAChiefRowLeadsWithItsNameAndShowsItsOwnSpendPlusItsReports(t *testing.T) {
	spent := func(usd float64) *core.Spend { return &core.Spend{USD: usd} }
	goal := func(id string, usd float64) core.Node {
		return core.Node{Manager: core.Manager{ID: "m" + id, Role: core.RoleManager}, Goal: &core.Goal{ID: id, Ask: "ship " + id}, Standing: core.StandMoving, Stats: core.Stats{Spend: spent(usd)}}
	}
	for _, tc := range []struct {
		name      string
		reports   []core.Node
		own, want string
	}{
		{"no reports, own spend read", nil, "$2.00", "$2.00"},
		{"no reports, own spend unread", nil, "", "$?"},
		{"one goal, own spend read", []core.Node{goal("goal1", 3)}, "$2.00", "$5.00"},
		{"one goal, own spend unread", []core.Node{goal("goal1", 3)}, "", "$3.00"},
		{"nested chief, own spend read", []core.Node{goal("goal1", 3), {Manager: core.Manager{ID: "mgr7", Role: core.RoleChief, Name: "infra"}, Standing: core.StandMoving, Stats: core.Stats{Spend: spent(1)}, Children: []core.Node{goal("goal2", 4)}}}, "$2.00", "$10.00"},
		{"nested chief, own spend unread", []core.Node{goal("goal1", 3), {Manager: core.Manager{ID: "mgr7", Role: core.RoleChief, Name: "infra"}, Standing: core.StandMoving, Stats: core.Stats{Spend: spent(1)}, Children: []core.Node{goal("goal2", 4)}}}, "", "$8.00"},
	} {
		for _, width := range []int{80, 160} {
			t.Run(fmt.Sprintf("%s, %d wide", tc.name, width), func(t *testing.T) {
				m, _, _ := goalSetup(t)
				next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 50})
				m = next.(Model)
				m.peaks.hidden = true
				b := m.board
				chief := core.Node{
					Manager:  core.Manager{ID: "mgr9", Role: core.RoleChief, Name: "harnesser"},
					Standing: core.StandMoving,
					Fresh:    core.Freshness{Brief: &core.Brief{Assessment: "grokking the harness design", CreatedAt: b.At}},
					Stats:    core.Stats{Vibe: &core.Vibe{Place: "Kyoto", At: b.At}, Running: 1, Started: b.At.Add(-3 * time.Hour)},
					Children: tc.reports,
				}
				if tc.own != "" {
					chief.Stats.Spend = spent(2)
				}
				b.Tree = []core.Node{chief}
				b.Gen++
				next, _ = m.Update(boardMsg{board: b})
				m = next.(Model)
				row := ""
				for _, l := range strings.Split(ansi.Strip(m.View().Content), "\n") {
					if strings.Contains(l, "harnesser") {
						row = l
						break
					}
				}
				if fields := strings.Fields(row); len(fields) == 0 || fields[0] != "harnesser" {
					t.Fatalf("chief row %q does not lead with its name", row)
				}
				if !strings.Contains(row, "Kyoto") || ansi.StringWidth(row) > width {
					t.Errorf("chief row %q lacks its vibe or overflows %d columns", row, width)
				}
				if width < 160 {
					return
				}
				for _, want := range []string{tc.want, "1▶", "3h", plural(chief.Count(), "goal"), "grokking"} {
					if !strings.Contains(row, want) {
						t.Errorf("chief row %q lacks %q", row, want)
					}
				}
			})
		}
	}
}

func TestXOnAGoalUntiesTheChosenPullRequest(t *testing.T) {
	const ours, theirs = "https://github.com/acme/august/pull/7", "https://github.com/acme/august/pull/8"
	for _, tc := range []struct {
		name, pick, drops string
	}{
		{"by number", "2", theirs},
		{"by link", ours, ours},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c, ms := goalSetup(t, "make the importer handle CSV")
			g := ms[0].GoalID
			if err := c.Store.SavePRStatus(theirs, core.PRStatus{State: "OPEN", Merge: core.PRMergeFacts{Author: "ryan", Viewer: "me"}}); err != nil {
				t.Fatal(err)
			}
			for _, e := range []core.GoalEvent{
				{Goal: g, Kind: core.EventPROpened, Key: g + ":pr:" + ours},
				{Goal: g, Kind: core.EventPRTouched, Key: g + ":touched:" + theirs},
			} {
				if _, _, err := c.Store.AddGoalEvent(e); err != nil {
					t.Fatal(err)
				}
			}
			next, _ := refresh(t, m).Update(tea.WindowSizeMsg{Width: 140, Height: 50})
			m = press(t, to(t, next.(Model), isNode(ms[0].ID)), "enter")
			if v := m.View().Content; !strings.Contains(v, "PULL REQUESTS (1 to land)") || !strings.Contains(v, "2. "+theirs+"  someone else's") {
				t.Fatalf("the goal should list its pull request and the one it touched:\n%s", v)
			}
			m = press(t, m, "X")
			m = press(t, m, append(typed(tc.pick), "enter")...)
			press(t, m, append(typed("not this goal's"), "enter")...)
			events, _ := c.Store.GoalEvents(g)
			if !slices.ContainsFunc(events, func(e core.GoalEvent) bool {
				return e.Kind == core.EventPRDropped && strings.Contains(e.Payload, tc.drops) && strings.Contains(e.Payload, "not this goal's")
			}) {
				t.Fatalf("X did not drop %s: %+v", tc.drops, events)
			}
		})
	}
}
