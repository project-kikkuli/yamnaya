package tui

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
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
	want := map[string]int{lead.ID: 2, platform.ID: 3, ms[0].ID: 4, ms[1].ID: 4, ms[2].ID: 3}
	if !maps.Equal(depth, want) {
		t.Fatalf("tree depths %v, want %v", depth, want)
	}
	for id, tag := range map[string]string{ms[0].ID: "brief now", ms[1].ID: "no brief", lead.ID: "no brief"} {
		if !strings.Contains(lines[id], tag) || !strings.Contains(lines[id], standingOf(t, m, id)) {
			t.Fatalf("row %s lacks its standing or %q:\n%s", id, tag, lines[id])
		}
	}
	m = press(t, to(t, m, isNode(platform.ID)), "enter")
	if m.mode != modeChief || len(m.rows()) != 3 || !strings.Contains(m.View().Content, "REPORTS (2)") {
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
			if chief.Manager.Name != "data-team" {
				t.Fatalf("no chief named data-team on the board; message %q", m.message)
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
			if !strings.Contains(string(brief), "# data-team\n\n"+tc.remit+"\n") {
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
	if !strings.HasPrefix(m.message, "make-login-faster started") {
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
		{"two threads", 2, "2 open threads", "https://www.notion.so/Launch-plan-a0"},
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
				if isDoc(r) && r.key == "" {
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
			if tc.threads > 1 {
				m = press(t, m, "left")
				folded := m.View().Content
				for _, a := range asks {
					if strings.Contains(folded, a.URL) {
						t.Fatalf("folded bundle still shows thread %s", a.Key)
					}
				}
				m = press(t, m, "right")
				for _, a := range asks {
					if !strings.Contains(m.View().Content, a.URL) {
						t.Fatalf("reopened bundle hides thread %s", a.Key)
					}
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
	next, _ := refresh(t, m).Update(tea.WindowSizeMsg{Width: 140, Height: 40})
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
			"Read CSV by mira"},
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

func TestAQueueRowOpensOnlyWhenSelectedAndKeepsToOneLineAtPhoneWidth(t *testing.T) {
	m := New(&core.Core{})
	m.loaded, m.width = true, 40
	m.board.At = time.Now()
	pr := core.PR{URL: "https://github.com/acme/august/pull/7", Repo: "acme/august", Number: 7, Status: &core.PRStatus{Title: "Read CSV"}}
	for _, tc := range []struct {
		name string
		need core.Need
		want string
	}{
		{"pull request", core.Need{PR: &pr, Manager: "mgr1", Name: "faster-login"}, "faster-login · august#7"},
		{"steer", core.Need{Steer: &core.Steer{Manager: "mgr1", Revision: 2, Nudges: 2, Text: "ship TSV too"}, Manager: "mgr1"}, "not adopted after 2 nudges"},
		{"item", core.Need{Item: &core.Item{ID: "D1", Kind: core.KindDecision, Question: "use CAS?", Rec: "yes", DoLine: "use CAS"}}, "rec: yes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.cursor = 9
			closed := strings.TrimRight(ansi.Strip(m.queueRowLines(1, tc.need, "1", m.width)), "\n")
			if strings.Contains(closed, "\n") || ansi.StringWidth(closed) != 40 {
				t.Fatalf("closed row is not one 40 column line: %q", closed)
			}
			m.cursor = 1
			if open := ansi.Strip(m.queueRowLines(1, tc.need, "1", m.width)); !strings.Contains(open, tc.want) {
				t.Fatalf("open row %q lacks %q", open, tc.want)
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
				if fields := strings.Fields(strings.TrimLeft(row, " >▾▸")); len(fields) == 0 || fields[0] != "harnesser" {
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

func TestDClosesAGoalWithAPickedDispositionAndReason(t *testing.T) {
	for _, tc := range []struct {
		name, pick, wants string
		closes            bool
	}{
		{"superseded by number", "2", core.CloseSuperseded, true},
		{"abandoned by name", "abandoned", core.CloseAbandoned, true},
		{"finished past the verifier", "4", core.CloseFinishedOverride, true},
		{"finished without a pass is refused", "1", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c, ms := goalSetup(t, "make the importer handle CSV")
			g := ms[0].GoalID
			next, _ := refresh(t, m).Update(tea.WindowSizeMsg{Width: 140, Height: 50})
			m = press(t, to(t, next.(Model), isNode(ms[0].ID)), "d")
			m = press(t, m, append(typed(tc.pick), "enter")...)
			m = press(t, m, append(typed("goal27 took it"), "enter")...)
			now, err := c.Store.Goal(g)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.closes {
				if now.State != core.GoalActive || !strings.Contains(m.message, "override") {
					t.Fatalf("state %s, message %q; want refused", now.State, m.message)
				}
				return
			}
			if now.State != core.GoalDropped {
				t.Fatalf("state %s after d", now.State)
			}
			m = press(t, refresh(t, m), "E")
			v := ansi.Strip(m.View().Content)
			for _, want := range []string{"ended goals", now.Name, tc.wants, "the owner", "goal27 took it"} {
				if !strings.Contains(v, want) {
					t.Errorf("E lacks %q:\n%s", want, v)
				}
			}
		})
	}
}

func TestDEndsWhatStartedWhileItsPromptWasOpen(t *testing.T) {
	for _, late := range []string{"pick", "why"} {
		t.Run("started at the "+late+" prompt", func(t *testing.T) {
			m, c, ms := goalSetup(t, "make the importer handle CSV")
			sessions := map[int]core.Session{1: {ID: ms[0].Session, Status: core.Idle}}
			procs := []proc.Proc{{PID: 1, TTY: "/dev/a", Args: []string{"claude"}}}
			c.Harnesses = []core.Harness{harness{sessions}}
			c.Procs = func() ([]proc.Proc, error) { return procs, nil }
			signals(c, false)
			start := func() {
				sessions[2] = core.Session{ID: "late-worker", Status: core.Busy}
				procs = append(procs, proc.Proc{PID: 2, TTY: "/dev/b", Args: []string{"claude"}})
				if err := c.Store.Link("late-worker", ms[0].Session); err != nil {
					t.Fatal(err)
				}
			}
			next, _ := refresh(t, m).Update(tea.WindowSizeMsg{Width: 140, Height: 50})
			m = press(t, to(t, next.(Model), isNode(ms[0].ID)), "d")
			if late == "pick" {
				start()
			}
			m = press(t, m, "3", "enter")
			if late == "why" {
				start()
			}
			press(t, m, append(typed("not needed"), "enter")...)
			if ended := c.Launches.(*launches).landed; !slices.Contains(ended, "late-worker") {
				t.Fatalf("close ended %v; the worker that started during the prompt survives it", ended)
			}
		})
	}
}

func TestNewWorkFromANeutralChiefRequiresAnExplicitRepository(t *testing.T) {
	for _, key := range []string{"n", "+"} {
		t.Run(key, func(t *testing.T) {
			m, c, _ := goalSetup(t, "service goal")
			l := c.Launches.(*launches)
			chief, err := c.NewChief("coordination", "", "", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			before := len(l.started)
			m = to(t, refresh(t, m), isNode(chief.ID))
			m = press(t, m, key)
			if m.inKind != inNewRepo || m.mode != modeInput {
				t.Fatalf("neutral chief inferred a repo: kind %v mode %v", m.inKind, m.mode)
			}
			repo := t.TempDir()
			m = press(t, m, strings.Split(repo, "")...)
			m = press(t, m, "enter")
			m = press(t, m, strings.Split("ship workplanes", "")...)
			m = press(t, m, "enter")
			if len(l.started) != before+1 || l.started[before].Repo != repo {
				t.Fatalf("explicit repo lost: %s; %s", fmtLaunches(l.started), m.message)
			}
		})
	}
}

type mergerStub struct {
	core.PRSource
	merged []string
}

func (s *mergerStub) PRStatus(string) (core.PRStatus, error) { return core.PRStatus{}, nil }

func (s *mergerStub) Merge(url, head string) error {
	s.merged = append(s.merged, url+"@"+head)
	return nil
}

func TestTheAcceptKeyAndLaterActOnPullRequestRowsAsTheyRead(t *testing.T) {
	url := "https://github.com/acme/august/pull/7"
	ready := core.PRStatus{State: "OPEN", Title: "Read CSV", Review: "APPROVED", Passed: 1, Merge: core.PRMergeFacts{Viewer: "me", OID: "abc", Mergeable: "MERGEABLE", MergeState: "CLEAN"}}
	isPR := func(r row) bool {
		return r.need != nil && (r.need.PR != nil || r.need.Review != nil)
	}
	for _, tc := range []struct {
		name, author, verb string
		review             bool
		merged             int
	}{
		{"my ready pull request merges", "me", "mrg", false, 1},
		{"a teammate's review request never merges", "teammate", "rev", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c, _ := goalSetup(t)
			stub := &mergerStub{}
			c.PRs = stub
			st := ready
			st.Fetched = time.Now()
			st.Merge.Author = tc.author
			if err := c.Store.SavePRStatus(url, st); err != nil {
				t.Fatal(err)
			}
			if tc.review {
				err := c.Store.SyncReviews([]core.ReviewRequest{{URL: url, Updated: "2026-11-01T00:00:00Z", Title: "Read CSV", Author: tc.author}})
				if err != nil {
					t.Fatal(err)
				}
			} else if err := c.Store.SyncAuthored(map[string]string{url: "Read CSV"}); err != nil {
				t.Fatal(err)
			}
			m = to(t, press(t, refresh(t, m), slices.Repeat([]string{"up"}, 30)...), isPR)
			if v := ansi.Strip(m.View().Content); !strings.Contains(v, " "+tc.verb+" ") {
				t.Fatalf("row does not read %s:\n%s", tc.verb, v)
			}
			m = press(t, m, "l", "enter")
			m = refresh(t, m)
			r := m.rows()
			if slices.ContainsFunc(r, func(r row) bool { return isPR(r) && !m.isLater(r.need) }) || len(m.board.Later) != 1 {
				t.Fatalf("later left the row in the queue:\n%s", ansi.Strip(m.View().Content))
			}
			if err := c.Store.WakeRow("pr:" + url); err != nil {
				t.Fatal(err)
			}
			m = to(t, press(t, refresh(t, m), slices.Repeat([]string{"up"}, 30)...), isPR)
			press(t, m, "a")
			if len(stub.merged) != tc.merged {
				t.Fatalf("merged %v, want %d merges", stub.merged, tc.merged)
			}
		})
	}
}

func TestNOnAGoalOrChiefRenamesItInlineAndRejectsABadNameWithoutLeaving(t *testing.T) {
	for _, tc := range []struct {
		name, row, typed, want, err string
	}{
		{name: "a goal", row: "goal", typed: "faster-login", want: "faster-login"},
		{name: "a chief", row: "chief", typed: "platform-lead", want: "platform-lead"},
		{name: "a name another uses", row: "goal", typed: "platform", err: "already names"},
		{name: "not kebab-case", row: "chief", typed: "Platform Lead", err: "kebab-case"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c, ms := goalSetup(t, "make login faster")
			chief, err := c.NewChief("platform", "", t.TempDir(), "", []string{ms[0].ID})
			if err != nil {
				t.Fatal(err)
			}
			id := map[string]string{"goal": ms[0].ID, "chief": chief.ID}[tc.row]
			m = to(t, refresh(t, m), isNode(id))
			m = press(t, m, "N")
			m = press(t, m, strings.Split(tc.typed, "")...)
			m = press(t, m, "enter")
			if tc.err != "" {
				if m.mode != modeInput || !strings.Contains(m.View().Content, tc.err) {
					t.Fatalf("mode %v, want the input kept open saying %q:\n%s", m.mode, tc.err, m.View().Content)
				}
				return
			}
			o, err := c.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			if got, err := o.Lookup(tc.want); err != nil || got.ID != id {
				t.Fatalf("%q names %s, %v; want %s", tc.want, got.ID, err, id)
			}
			if m = refresh(t, m); !strings.Contains(m.View().Content, tc.want) {
				t.Fatalf("the board does not show %q:\n%s", tc.want, m.View().Content)
			}
		})
	}
}

func TestAQueueRowWithoutADoLineLeadsWithTheGoalsNameAndAKeyNeverLeads(t *testing.T) {
	for _, tc := range []struct {
		width   int
		do      string
		lead    string
		keyLast bool
	}{
		{width: 40, lead: "rotate-the-signing"},
		{width: 100, lead: "rotate-the-signing", keyLast: true},
		{width: 200, lead: "rotate-the-signing", keyLast: true},
		{width: 40, do: "pick the rotation", lead: "pick the rotation"},
		{width: 100, do: "pick the rotation", lead: "pick the rotation", keyLast: true},
	} {
		t.Run(strconv.Itoa(tc.width)+tc.do, func(t *testing.T) {
			m, c, ms := goalSetup(t, "rotate the signing keys")
			it, err := c.Store.AddItem(core.Item{Dossier: core.Dossier{Goal: ms[0].GoalID}, Options: picks, Kind: core.KindDecision, Question: "which rotation?\n\n" + strings.Repeat("long detail ", 40), Rec: "a", DoLine: tc.do, Session: ms[0].Session, Recipient: core.Owner})
			if err != nil {
				t.Fatal(err)
			}
			m = refresh(t, m)
			m.width = tc.width
			at := slices.IndexFunc(m.board.Needs, func(n core.Need) bool { return n.Item != nil && n.Item.Seq == it.Seq })
			if at < 0 {
				t.Fatalf("no row for %s in %+v", it.Ref(), m.board.Needs)
			}
			line, side := core.RowLine(m.board.Needs[at], "1", tc.width, m.board.At)
			_, words, _ := strings.Cut(line, "dec")
			words = strings.TrimSpace(strings.TrimLeft(words, " 0123456789m?"))
			if !strings.HasPrefix(words, tc.lead) {
				t.Fatalf("row %q, want it to lead with %q", line, tc.lead)
			}
			if strings.Contains(line, it.Ref()) {
				t.Fatalf("row %q holds its key %s", line, it.Ref())
			}
			if tc.keyLast && (!strings.Contains(side, it.Ref()) || !strings.Contains(side, "rotate-the-signing")) {
				t.Fatalf("side %q, want the goal's name and key %s", side, it.Ref())
			}
		})
	}
}

func TestABoardRowShowsTheWholeNameWhenTheWidthHasRoomForIt(t *testing.T) {
	const long = "teach-the-board-to-scroll-its-long-rows"
	for _, tc := range []struct {
		name  string
		width int
		whole bool
	}{
		{"a wide board", 180, true},
		{"a board of a hundred columns", 100, true},
		{"a narrow board", 60, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c, ms := goalSetup(t, "short ask")
			if _, err := c.Rename(ms[0].ID, long); err != nil {
				t.Fatal(err)
			}
			m = refresh(t, m)
			m.width = tc.width
			for _, i := range []int{-1, 0} {
				m.cursor = 0
				line, _, _ := strings.Cut(ansi.Strip(m.nodeLines(i, m.board.Tree[0], 0)), "\n")
				if got := strings.Contains(line, long); got != tc.whole {
					t.Fatalf("whole name shown %v, want %v, at %d columns:\n%s", got, tc.whole, tc.width, line)
				}
				if ansi.StringWidth(line) > tc.width {
					t.Fatalf("row is %d wide, past %d columns:\n%s", ansi.StringWidth(line), tc.width, line)
				}
			}
		})
	}
}

func TestShortNamesLeaveTheBoardColumnNoWiderThanTheLongest(t *testing.T) {
	m, c, ms := goalSetup(t, "one", "two")
	for i, name := range []string{"a-name-of-twenty-char", "short-one"} {
		if _, err := c.Rename(ms[i].ID, name); err != nil {
			t.Fatal(err)
		}
	}
	m = refresh(t, m)
	m.width = 180
	if got, want := m.nameColumn(0), len("a-name-of-twenty-char"); got != want {
		t.Fatalf("name column is %d wide, want %d", got, want)
	}
}

// renameRemotes is a host that answers a rename the way ym there does.
type renameRemotes struct {
	remotes
	refuse error
}

func (r *renameRemotes) Run(host string, args ...string) (string, error) {
	r.ran = append(r.ran, append([]string{host}, args...))
	return "", r.refuse
}

func TestNOnAnotherHostsGoalRenamesItThereAndARefusalKeepsTheInputOpen(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		open  bool
		shows string
	}{
		{name: "the host renames it", shows: "login-box is now login-b2"},
		{name: "the name is taken there", err: fmt.Errorf("ym goal rename login-box login-b2 on %s: login-b2 already names a goal or chief here", box), open: true, shows: "login-b2 already names a goal or chief here"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c, _ := setup(t)
			r := &renameRemotes{refuse: tc.err}
			r.boards = []core.RemoteBoard{{Host: box, At: time.Now(),
				Managers: []core.Manager{{ID: "mgr1", Role: core.RoleManager, Name: "login-box", GoalID: "goal1"}},
				Goals:    []core.Goal{{ID: "goal1", Name: "login-box", Ask: "faster login", ManagerID: "mgr1"}}}}
			c.Remotes, c.ShowRemotes = r, true
			m.width, m.peaks.hidden = 140, true
			m = refresh(t, m)
			m = press(t, to(t, m, isNode(core.RemoteKey(box, "mgr1"))), "N")
			m = press(t, m, strings.Split("login-b2", "")...)
			m = press(t, m, "enter")
			if want := []string{box, "goal", "rename", "login-box", "login-b2"}; len(r.ran) != 1 || !slices.Equal(r.ran[0], want) {
				t.Fatalf("ran %v, want %v", r.ran, want)
			}
			if open := m.mode == modeInput && m.inKind == inName; open != tc.open {
				t.Fatalf("input open %v, want %v", open, tc.open)
			}
			view := ansi.Strip(m.View().Content)
			if !strings.Contains(view, tc.shows) {
				t.Fatalf("view does not say %q:\n%s", tc.shows, view)
			}
			if strings.Contains(view, "mgr1") {
				t.Fatalf("view shows the id:\n%s", view)
			}
		})
	}
}

func TestNOnAGoalsAgentRowStartsANewGoalAsTheHelpSays(t *testing.T) {
	m, _, ms := goalSetup(t, "make login faster")
	m = press(t, to(t, m, isNode(ms[0].ID)), "enter")
	if m.mode != modeGoal {
		t.Fatalf("enter went to mode %v, want the goal's page", m.mode)
	}
	m.board.Tree[0].Agents = []core.Live{{Session: core.Session{Harness: "claude", ID: "agent1", Title: "an agent", Repo: "/src/app"}}}
	m = press(t, to(t, m, isSession("agent1")), "N")
	if m.mode != modeInput || m.inKind != inGoal {
		t.Fatalf("N on an agent row went to mode %v input %v, want a new goal's ask:\n%s", m.mode, m.inKind, m.View().Content)
	}
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, "goal in ") {
		t.Fatalf("the prompt does not say it starts a goal:\n%s", v)
	}
}

func TestAnItemsEvidenceLineNamesItsGoal(t *testing.T) {
	m, c, ms := goalSetup(t, "make login faster")
	if _, err := c.Rename(ms[0].ID, "faster-login"); err != nil {
		t.Fatal(err)
	}
	m = refresh(t, m)
	line := evidenceLine(m.board.Org, core.Item{Dossier: core.Dossier{Goal: ms[0].GoalID}})
	if !strings.Contains(line, "faster-login") || strings.Contains(line, ms[0].GoalID) {
		t.Fatalf("evidence line %q, want the goal's name and not its id", line)
	}
}

func TestANeedsYouRowShowsAWholeNameWhenTheWidthHasRoomForIt(t *testing.T) {
	const long = "this-is-a-forty-character-goal-name-here"
	for _, tc := range []struct {
		width int
		whole bool
	}{{200, true}, {100, true}, {80, true}, {50, false}} {
		t.Run(strconv.Itoa(tc.width), func(t *testing.T) {
			m, c, ms := goalSetup(t, "short ask")
			if _, err := c.Rename(ms[0].ID, long); err != nil {
				t.Fatal(err)
			}
			m = refresh(t, m)
			m.width = tc.width
			need := core.Need{Item: &core.Item{Seq: 1, ID: "D1", Kind: core.KindDecision, Question: "which way?", Dossier: core.Dossier{Goal: ms[0].GoalID}}}
			m.board.Needs = []core.Need{need}
			line, _, _ := strings.Cut(ansi.Strip(m.needLines(-1, need)), "\n")
			if got := strings.Contains(line, long); got != tc.whole {
				t.Fatalf("whole name shown %v, want %v, at %d columns:\n%s", got, tc.whole, tc.width, line)
			}
			if !strings.Contains(line, "D1") {
				t.Fatalf("row lost its key:\n%s", line)
			}
		})
	}
}

func TestAnItemRowKeepsItsKeyWholeWhateverElseIsLong(t *testing.T) {
	const lead, to = "this-is-a-forty-character-goal-name-here", "chief-with-a-forty-character-name-itself"
	for _, tc := range []struct {
		name     string
		width    int
		key      string
		question string
		wholeTo  bool
		wholeLed bool
	}{
		{name: "a wide board", width: 200, key: "#7", question: "which way should the export go", wholeTo: true, wholeLed: true},
		{name: "a short key at 100", width: 100, key: "#7", question: "which way", wholeLed: true},
		{name: "a short key at 80", width: 80, key: "#7", question: "which", wholeLed: true},
		{name: "a key of 22 at 100", width: 100, key: "byo-mbp-dm6tyx3m6c/#84", question: "which", wholeLed: true},
		{name: "a key of 34 at 100", width: 100, key: "this-is-a-thirty-char-repo-name:D1", question: "which"},
		{name: "a key of 34 at 80", width: 80, key: "this-is-a-thirty-char-repo-name:D1", question: "which"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c, ms := goalSetup(t, "short ask")
			if _, err := c.Rename(ms[0].ID, lead); err != nil {
				t.Fatal(err)
			}
			boss, err := c.NewChief(to, "", t.TempDir(), "", nil)
			if err != nil {
				t.Fatal(err)
			}
			m = refresh(t, m)
			m.width = tc.width
			it := core.Item{Seq: 7, ID: "D1", Kind: core.KindDecision, State: core.ItemOpen, Recipient: boss.ID, Question: "which way should the export go when it is large?", Dossier: core.Dossier{Goal: ms[0].GoalID}}
			m.items = []core.Item{it}
			line, _, _ := strings.Cut(ansi.Strip(m.itemLines(-1, it, map[string]string{it.Key(): tc.key})), "\n")
			if w := ansi.StringWidth(line); w > tc.width {
				t.Fatalf("row is %d wide at %d columns:\n%s", w, tc.width, line)
			}
			if !strings.HasSuffix(line, tc.key) {
				t.Fatalf("row lost its key %q:\n%s", tc.key, line)
			}
			if strings.Contains(line, lead) != tc.wholeLed || strings.Contains(line, to) != tc.wholeTo || !strings.Contains(line, tc.question) {
				t.Fatalf("want whole lead %v, whole recipient %v, question %q:\n%s", tc.wholeLed, tc.wholeTo, tc.question, line)
			}
		})
	}
}

func TestAFullDoLineDrawsWholeOnTheBoardAsOnTheOtherSurfaces(t *testing.T) {
	do := strings.Repeat("x", core.MaxDoLine)
	for _, width := range []int{40, 41, 46, 47, 60, 99} {
		for _, tc := range []struct {
			name   string
			rows   int
			impact string
		}{
			{"one digit rank", 1, ""},
			{"two digit rank", 12, ""},
			{"urgent two digit rank", 12, core.ImpactProd},
		} {
			t.Run(fmt.Sprintf("%d %s", width, tc.name), func(t *testing.T) {
				m := treeModel()
				m.width, m.cursor, m.page = width, 0, nil
				m.board.Needs = nil
				m.board.Later = make([]core.Need, 10)
				for i := range m.board.Later {
					m.board.Later[i] = core.Need{Project: "mission", Item: &core.Item{Seq: int64(100 + i), ID: fmt.Sprintf("D%d", 100+i), Kind: core.KindDecision, Question: "q", State: core.ItemSnoozed}}
				}
				for i := range tc.rows {
					m.board.Needs = append(m.board.Needs, core.Need{Project: "mission", Item: &core.Item{Seq: int64(i + 1), ID: fmt.Sprintf("D%d", i+1), Kind: core.KindDecision, Question: "q",
						DoLine: fmt.Sprintf("%s%02d", do[:core.MaxDoLine-2], i+1), Rec: "r", Impact: tc.impact}})
				}
				content := ansi.Strip(m.View().Content)
				header := core.QueueHeader(m.board.Needs, len(m.board.Later))
				if !strings.Contains(content, header) {
					t.Fatalf("header %q missing at width %d:\n%s", header, width, content)
				}
				labels := core.RankLabels(tc.rows)
				for i := range m.board.Needs {
					want, _ := core.RowLine(m.board.Needs[i], labels[i], core.QueueWidth, m.board.At)
					if !strings.Contains(content, want) {
						t.Fatalf("row %q missing at width %d:\n%s", want, width, content)
					}
				}
				for _, line := range strings.Split(content, "\n") {
					if strings.Contains(line, do[:10]) && ansi.StringWidth(line) > width {
						t.Fatalf("a %d wide row on a %d terminal: %q", ansi.StringWidth(line), width, line)
					}
				}
			})
		}
	}
}

func TestAChiefsItemDetailShowsNoIdAsItsRepository(t *testing.T) {
	for _, tc := range []struct {
		name, scope, want string
		shows             bool
	}{
		{"a chief's workspace", "/state/chiefs/mgr2", "mgr2", false},
		{"a repository", "/src/august/.git", "august", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _ := setup(t)
			m = refresh(t, m)
			m.width = 120
			it := core.Item{Seq: 7, ID: "D1", Kind: core.KindDecision, State: core.ItemOpen, Question: "which way?", Scope: tc.scope}
			m.why = &it
			v := ansi.Strip(m.whyView())
			if got := strings.Contains(v, tc.want); got != tc.shows {
				t.Fatalf("shows %q %v, want %v:\n%s", tc.want, got, tc.shows, v)
			}
		})
	}
}

func TestARelayRowNamesWhatItCarries(t *testing.T) {
	m, _, _ := setup(t)
	m.board.Org.Managers = map[string]core.Manager{"box/mgr51": {ID: "box/mgr51", Name: "far-chief", Host: "box"}}
	body, err := json.Marshal(map[string]any{"args": []string{"goal", "send", "--relayed", "mgr51", "see mgr51 too"}})
	if err != nil {
		t.Fatal(err)
	}
	got := m.shownRelay(core.Relay{Kind: core.RelayYM, Dest: "box", Body: string(body)})
	if got != "ym goal send --relayed far-chief see mgr51 too" {
		t.Fatalf("relay row reads %q", got)
	}
}
