package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

func foldBoard() core.Board {
	goal := func(id, ask string) core.Node {
		return core.Node{Manager: core.Manager{ID: id, Name: id}, Goal: &core.Goal{ID: id, Ask: ask}, Repo: "github.com/o/app", Standing: core.StandMoving}
	}
	chief := func(id string, children ...core.Node) core.Node {
		return core.Node{Manager: core.Manager{ID: id, Name: id, Role: core.RoleChief}, Children: children, Standing: core.StandMoving}
	}
	return core.Board{Tree: []core.Node{
		chief("top", chief("mid", chief("deep", goal("hit", "needle")), goal("side", "side task")), goal("sibling", "sibling task")),
		chief("remote/mid", goal("remote/hit", "remote task")), chief("empty"), goal("solo", "solo task"),
	}}
}

func nestedBoard() core.Board {
	b := foldBoard()
	sessions := []core.Live{
		{Session: core.Session{ID: "lead", Title: "Lead", Status: core.Busy, Parent: "", Crew: []core.Member{{ID: "c", Name: "Crew needle", Brief: "Crew instruction", Facts: core.Facts{Result: "Crew result"}, Status: core.Busy}}}},
		{Session: core.Session{ID: "child", Title: "Child", Parent: "lead", Status: core.Busy}},
		{Session: core.Session{ID: "grandchild", Title: "Grandchild", Parent: "child", Status: core.Idle}},
	}
	p := core.Project{Key: "mission", Name: "Mission", Repo: "repo", Sessions: sessions, Items: []core.Item{{Seq: 1, ID: "D1", Question: "Mission ask"}}}
	b.Projects = []core.Project{p}
	b.Repos = []core.Repo{{Key: "repo", Name: "Repository", Missions: []core.Project{p}}}
	b.Sessions = sessions
	managed := slices.Clone(sessions)
	for i := range managed {
		managed[i].ID = "managed-" + managed[i].ID
		if managed[i].Parent != "" {
			managed[i].Parent = "managed-" + managed[i].Parent
		}
	}
	b.Sessions = append(b.Sessions, managed...)
	b.Tree[0].Children[0].Children[0].Children[0].Live = &managed[0]
	b.Tree[0].Children[0].Children[0].Children[0].Agents = managed[1:]
	b.Needs = []core.Need{{Project: "mission", Item: &core.Item{Seq: 2, ID: "D2", Question: "Bundle"}, Asks: []core.Item{{Seq: 2, ID: "D2", Question: "Bundle"}, {Seq: 3, ID: "D3", Question: "Second ask"}}}}
	return b
}

func treeModel() Model {
	m := New(&core.Core{})
	m.board, m.loaded, m.width, m.height, m.peaks.hidden = nestedBoard(), true, 180, 80, true
	return m
}

func treeSelect(t *testing.T, m Model, key string) Model {
	t.Helper()
	for _, r := range m.projectRows(true) {
		if rowKey(r) == key {
			parents := map[string]row{}
			for _, p := range m.projectRows(true) {
				parents[rowKey(p)] = p
			}
			for p := r.parent; p != ""; p = parents[p].parent {
				m.setFold(parents[p], false)
			}
			for i, r := range m.rows() {
				if rowKey(r) == key {
					m.cursor = i
					return m
				}
			}
		}
	}
	t.Fatalf("missing tree key %s", key)
	return m
}

func TestContainersAtEveryDepth(t *testing.T) {
	for _, key := range []string{"heading:needs", "heading:goals", "heading:sessions", "heading:goals/chiefs", "heading:goals/repo github.com/o/app", "repo:repo", "project:mission", "node:top", "node:mid", "node:deep", "node:hit", "session:lead", "session:child", "heading:session/lead/crew", "heading:project/mission/sessions", "heading:project/mission/items", "need:" + needID(nestedBoard().Needs[0])} {
		t.Run(key, func(t *testing.T) {
			m := treeSelect(t, treeModel(), key)
			r, _ := m.selected()
			m.setFold(r, false)
			before := m.rows()
			at := m.cursor
			if !before[at].container || at+1 >= len(before) || before[at+1].parent != key {
				t.Fatalf("%s has no child", key)
			}
			child := rowKey(before[at+1])
			parent := r.parent
			m = press(t, m, "left")
			r, _ = m.selected()
			if rowKey(r) != key || !r.closed {
				t.Fatalf("left selected %+v", r)
			}
			if slices.ContainsFunc(m.rows(), func(r row) bool { return rowKey(r) == child }) {
				t.Fatal("child visible under folded container")
			}
			if view := ansi.Strip(m.treePage().String()); !strings.Contains(view, "▸") {
				t.Fatal("missing folded indicator")
			}
			m = press(t, m, "right")
			r, _ = m.selected()
			if rowKey(r) != key || r.closed {
				t.Fatal("right must expand and retain selection")
			}
			m = press(t, m, "right")
			r, _ = m.selected()
			if rowKey(r) != child {
				t.Fatalf("right selected %s, want first child %s", rowKey(r), child)
			}
			m = treeSelect(t, m, key)
			m = press(t, m, "left", "left")
			r, _ = m.selected()
			want := parent
			if want == "" {
				want = key
			}
			if rowKey(r) != want {
				t.Fatalf("left selected %s want parent %s", rowKey(r), want)
			}
		})
	}
}

func TestNestedChoicesRefreshAndSearch(t *testing.T) {
	for _, tc := range []struct{ parent, child, match string }{
		{"node:top", "node:mid", "node:hit"},
		{"heading:sessions", "project:mission", "member:lead/c"},
		{"repo:repo", "session:lead", "member:lead/c"},
	} {
		t.Run(tc.parent+"/"+tc.child, func(t *testing.T) {
			m := treeSelect(t, treeModel(), tc.child)
			m = press(t, m, "left")
			m = treeSelect(t, m, tc.parent)
			m = press(t, m, "left")
			next, _ := m.Update(boardMsg{board: nestedBoard()})
			m = next.(Model)
			r, _ := m.selected()
			if rowKey(r) != tc.parent || !r.closed {
				t.Fatal("refresh lost selection/fold")
			}
			m = press(t, m, "right")
			m = to(t, m, func(r row) bool { return rowKey(r) == tc.child })
			r, _ = m.selected()
			if !r.closed {
				t.Fatal("opening parent erased child choice")
			}
			m = press(t, m, "/", "paste:needle", "enter")
			m = to(t, m, func(r row) bool { return rowKey(r) == tc.match })
			m = press(t, m, "esc")
			r, _ = m.selected()
			if rowKey(r) != tc.child {
				t.Fatalf("clearing search selected %s want %s", rowKey(r), tc.child)
			}
			if !r.closed {
				t.Fatal("search changed saved fold")
			}
		})
	}
}

func TestDetailSectionsShareProjection(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode mode
		key  string
	}{
		{"repo", modeRepo, "heading:repo/repo/missions"},
		{"mission", modeDetail, "heading:project/mission/sessions"},
		{"chief", modeChief, "heading:node/top/reports"},
		{"goal feed", modeGoal, "heading:node/hit/feed"},
		{"goal sessions", modeGoal, "heading:node/hit/sessions"},
		{"session crew", modeSession, "heading:session/lead/crew"},
		{"agent brief", modeAgent, "heading:member/lead/c/BRIEF"},
		{"agent result", modeAgent, "heading:member/lead/c/RESULT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := treeModel()
			m.mode, m.repo, m.detail, m.sess, m.goal, m.chief = tc.mode, "repo", "mission", "lead", "hit", "top"
			m.agent = "c"
			m = treeSelect(t, m, tc.key)
			m = press(t, m, "left")
			r, _ := m.selected()
			if !r.closed || rowKey(r) != tc.key {
				t.Fatal("detail header did not collapse")
			}
			m = press(t, m, "right")
			r, _ = m.selected()
			if r.closed {
				t.Fatal("detail header did not expand")
			}
			m = press(t, m, "enter")
			if m.mode != tc.mode {
				t.Fatal("header Enter changed page")
			}
		})
	}
}

func TestFoldIdentityAndSelectionAcrossRefresh(t *testing.T) {
	m := treeSelect(t, treeModel(), "node:mid")
	m = press(t, m, "left")
	m = treeSelect(t, m, "node:remote/mid")
	m = press(t, m, "left")
	m = treeSelect(t, m, "node:mid")
	m = press(t, m, "right")
	if !m.folds["node:remote/mid"] || m.folds["node:mid"] {
		t.Fatal("host-qualified folds collide")
	}
	m = treeSelect(t, m, "node:mid")
	m = press(t, m, "left")
	m = treeSelect(t, m, "node:solo")
	b := nestedBoard()
	b.Tree[0].Children[0].Children = append(b.Tree[0].Children[0].Children, b.Tree[len(b.Tree)-1])
	b.Tree = b.Tree[:len(b.Tree)-1]
	next, _ := m.Update(boardMsg{board: b})
	m = next.(Model)
	r, _ := m.selected()
	if rowKey(r) != "node:mid" {
		t.Fatalf("hidden selection did not follow ancestor: %s", rowKey(r))
	}
}

func TestBoardLineageNestsSessionsWithoutHarnessParent(t *testing.T) {
	m, c, _ := setup(t)
	if err := c.Store.Link("twin", "lead"); err != nil {
		t.Fatal(err)
	}
	m = refresh(t, m)
	m.mode, m.detail = modeDetail, "lead"
	found := false
	for _, r := range m.rows() {
		if r.session != nil && r.session.ID == "twin" && r.member == nil {
			found = true
			if r.parent != "session:lead" {
				t.Fatalf("lineage parent=%s", r.parent)
			}
		}
	}
	if !found {
		t.Fatal("linked child is missing")
	}
}

func TestSessionFoldsKeepHostAndProfileIdentity(t *testing.T) {
	m := treeModel()
	m.mode, m.detail = modeDetail, "mission"
	p := m.board.Projects[0]
	p.Sessions = nil
	for _, tc := range []struct{ host, profile string }{{"", ""}, {"box", ""}, {"other", ""}, {"box", "work"}} {
		p.Sessions = append(p.Sessions, core.Live{Session: core.Session{ID: "same", Profile: tc.profile, Harness: "codex", Crew: []core.Member{{ID: "crew", Name: "worker"}}}, Remote: tc.host})
	}
	m.board.Projects[0] = p
	keys := []string{"session:same", "session:box/same", "session:other/same", "session:box/codex:work@same"}
	for _, key := range keys {
		m = treeSelect(t, m, key)
		m = press(t, m, "left")
		for _, other := range keys {
			m = to(t, m, func(r row) bool { return rowKey(r) == other })
		}
	}
	for _, key := range keys {
		if !m.folds[key] {
			t.Fatalf("lost fold %s", key)
		}
	}
}

func TestAgentSectionPagingRoundTrip(t *testing.T) {
	for _, key := range []string{"pgup", "b"} {
		t.Run(key, func(t *testing.T) {
			m := treeModel()
			m.mode, m.detail, m.sess, m.agent, m.height = modeAgent, "mission", "lead", "c", 20
			m.board.Projects[0].Sessions[0].Crew[0].Brief = strings.Repeat("long instruction line\n", 80)
			m = press(t, m, "pgdown")
			if m.scrollY == 0 {
				t.Fatal("page down did not scroll section content")
			}
			m = press(t, m, key)
			if m.scrollY != 0 {
				t.Fatal("page up did not return to the top")
			}
			m = press(t, m, "left")
			r, _ := m.selected()
			if !r.closed {
				t.Fatal("paging broke section folding")
			}
		})
	}
}

func TestNamedProfileSessionParents(t *testing.T) {
	for _, tc := range []struct {
		name, host, parent string
		goal               bool
	}{
		{"native parent", "", "parent", false},
		{"remote native parent", "box", "parent", false},
		{"profile qualified parent", "", "codex:work@parent", false},
		{"manager implied parent", "", "", true},
		{"remote manager implied parent", "box", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := treeModel()
			parent := core.Live{Session: core.Session{ID: "parent", Harness: "codex", Profile: "work"}, Remote: tc.host}
			child := core.Live{Session: core.Session{ID: "child", Harness: "codex", Profile: "work", Parent: tc.parent}, Remote: tc.host}
			var nodes []treeNode
			if tc.goal {
				nodes = m.nodeSessions(core.Node{Live: &parent, Agents: []core.Live{child}})
			} else {
				nodes = m.sessionsTree([]core.Live{parent, child}, nil)
			}
			if len(nodes) != 1 || len(nodes[0].children) != 1 || nodes[0].children[0].row.session.ID != "child" {
				t.Fatalf("named-profile parent lost: %+v", nodes)
			}
		})
	}
}

func TestSearchDrillKeepsDetailContext(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        mode
		query, want string
	}{
		{"goal", modeGoal, "needle", "FEED"},
		{"repository", modeRepo, "Repository", "MISSIONS"},
		{"mission", modeDetail, "Mission", "SESSIONS AND CREW"},
		{"session", modeSession, "Lead", "CREW"},
		{"agent", modeAgent, "Crew needle", "BRIEF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := treeModel()
			m.filter = tc.query
			m.mode, m.goal, m.repo, m.detail, m.sess, m.agent = tc.mode, "hit", "repo", "mission", "lead", "c"
			if rows := m.rows(); len(rows) == 0 {
				t.Fatal("search removed the opened detail context")
			}
			if view := ansi.Strip(m.prepareView().String()); !strings.Contains(view, tc.want) {
				t.Fatalf("search hid detail content: %s", view)
			}
		})
	}
}

func TestRefreshKeepsAskOccurrence(t *testing.T) {
	m := treeModel()
	item := m.board.Needs[0].Asks[1]
	m.board.Projects[0].Items = append(m.board.Projects[0].Items, item)
	m.board.Repos[0].Missions[0] = m.board.Projects[0]
	m = treeSelect(t, m, "project:mission")
	m = press(t, m, "right")
	m = treeSelect(t, m, "need:"+needID(m.board.Needs[0]))
	m = press(t, m, "right")
	var occurrences []int
	for i, r := range m.rows() {
		if r.need == nil && r.item != nil && r.item.Seq == item.Seq {
			occurrences = append(occurrences, i)
		}
	}
	if len(occurrences) != 2 {
		t.Fatalf("ask occurrences=%v", occurrences)
	}
	for _, at := range occurrences {
		m.cursor = at
		r, _ := m.selected()
		key := rowKey(r)
		next, _ := m.Update(boardMsg{board: m.board})
		m = next.(Model)
		r, _ = m.selected()
		if rowKey(r) != key {
			t.Fatalf("refresh moved ask from %s to %s", key, rowKey(r))
		}
	}
}

func TestDocumentAskBundleFoldsWithoutChangingEnter(t *testing.T) {
	m := treeModel()
	docs := []core.DocAsk{{Key: "thread1", Page: "page", Title: "First discussion", URL: "https://example.test/page#first"}, {Key: "thread2", Page: "page", Title: "Second discussion", URL: "https://example.test/page#second"}}
	m.board.Needs = append(m.board.Needs, core.Need{Doc: &docs[0], Docs: docs})
	m = treeSelect(t, m, "need:doc:page")
	r, _ := m.selected()
	if r.closed {
		t.Fatal("document bundle should retain expanded initial details")
	}
	m = press(t, m, "left")
	m = press(t, m, "right", "right")
	r, _ = m.selected()
	if rowKey(r) != "doc-ask:thread1" {
		t.Fatal("bundle did not reveal its first discussion")
	}
	var opened string
	m.openURL = func(url string) error { opened = url; return nil }
	next, cmd := m.Update(keyOf("enter"))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("Enter did not open discussion")
	}
	cmd()
	if opened != docs[0].URL {
		t.Fatalf("Enter opened %q", opened)
	}
	m = press(t, m, "left", "left")
	r, _ = m.selected()
	if rowKey(r) != "need:doc:page" || !r.closed {
		t.Fatal("left did not fold the document bundle")
	}
}

func TestAskDetailSectionsFoldAndKeepBackSelection(t *testing.T) {
	for _, at := range []int{0, 1} {
		t.Run(string(rune('0'+at)), func(t *testing.T) {
			m := treeModel()
			m.mode, m.back, m.cursor = modeWhy, modeList, 1
			m.why = &core.Item{Seq: 9, ID: "D9", Dossier: core.Dossier{Goal: "goal-context", Changes: []string{"context-content"}}, History: []core.Event{{}, {Text: "history-content"}}}
			m.whyAt = at
			before := m.whyView()
			text := "context-content"
			if at == 1 {
				text = "history-content"
			}
			if !strings.Contains(before, text) {
				t.Fatal("missing section body")
			}
			m = press(t, m, "left")
			context := m
			context.filter = ""
			rows := context.projectNodes(context.whySections(), false)
			if !rows[at].closed || strings.Contains(m.whyView(), text) {
				t.Fatal("ask detail section did not fold its content")
			}
			m = press(t, m, "right")
			rows = context.projectNodes(m.whySections(), false)
			if rows[at].closed {
				t.Fatal("ask detail section did not expand")
			}
			m = press(t, m, "esc")
			if m.mode != modeList || m.cursor != 1 {
				t.Fatal("detail fold changed source selection")
			}
		})
	}
}

func TestSearchArrowsFoldTemporarily(t *testing.T) {
	for _, tc := range []struct{ key, query string }{{"node:mid", "needle"}, {"heading:sessions", "Mission"}, {"heading:needs", "Bundle"}} {
		t.Run(tc.key, func(t *testing.T) {
			m := treeSelect(t, treeModel(), tc.key)
			m = press(t, m, "left")
			m = press(t, m, "/", "paste:"+tc.query, "enter")
			m = to(t, m, func(r row) bool { return rowKey(r) == tc.key })
			r, _ := m.selected()
			if r.closed {
				t.Fatal("search did not reveal saved folded container")
			}
			m = press(t, m, "left")
			r, _ = m.selected()
			if !r.closed || rowKey(r) != tc.key {
				t.Fatal("left did not collapse search container")
			}
			m = press(t, m, "right")
			r, _ = m.selected()
			if r.closed {
				t.Fatal("right did not expand search container")
			}
			m = press(t, m, "esc")
			r, _ = m.selected()
			if rowKey(r) != tc.key || !r.closed {
				t.Fatal("clearing search did not restore saved fold")
			}
		})
	}
}

func TestExitedSessionDescendantsFoldWithTheirParent(t *testing.T) {
	for _, tc := range []struct{ name, parent string }{{"live parent", "lead"}, {"exited parent", "ended"}} {
		t.Run(tc.name, func(t *testing.T) {
			m := treeModel()
			m.mode, m.detail = modeDetail, "mission"
			m.board.Projects[0].Dormant = []core.Dormant{{Key: "ended-launch", Session: "ended", Parent: "lead", Title: "Ended parent"}, {Key: "child-launch", Session: "ended-child", Parent: tc.parent, Title: "Ended child"}}
			parent := "session:lead"
			if tc.parent == "ended" {
				parent = "dormant:ended-launch"
			}
			m = treeSelect(t, m, "dormant:child-launch")
			r, _ := m.selected()
			if r.parent != parent {
				t.Fatalf("exited child parent=%s want %s", r.parent, parent)
			}
			m = press(t, m, "left", "left")
			r, _ = m.selected()
			if rowKey(r) != parent || !r.closed {
				t.Fatal("parent did not collapse exited child")
			}
			if slices.ContainsFunc(m.rows(), func(r row) bool { return rowKey(r) == "dormant:child-launch" }) {
				t.Fatal("exited child escaped parent fold")
			}
			m = press(t, m, "right")
			if !slices.ContainsFunc(m.rows(), func(r row) bool { return rowKey(r) == "dormant:child-launch" }) {
				t.Fatal("exited child did not return")
			}
		})
	}
}

func TestInlineSessionEnterKeepsMissionItemsAndScope(t *testing.T) {
	for _, route := range []string{"inline", "mission detail"} {
		t.Run(route, func(t *testing.T) {
			m, _, _ := setup(t)
			m = to(t, m, mission("lead"))
			if route == "inline" {
				m = press(t, m, "right")
				m = to(t, m, isSession("lead"))
			} else {
				m = press(t, m, "enter")
			}
			if m.detail != "" && route == "inline" {
				t.Fatal("test did not start from the overview")
			}
			m = press(t, m, "enter")
			if m.mode != modeSession || m.detail != "lead" {
				t.Fatalf("session route lost mission context: mode=%d detail=%q", m.mode, m.detail)
			}
			if !slices.ContainsFunc(m.rows(), func(r row) bool { return r.item != nil && r.item.ID == "D1" }) {
				t.Fatal("session lost the owner's linked open item")
			}
			m = press(t, m, "I")
			if m.itemsOf != "lead" || m.mode != modeItems || !slices.ContainsFunc(m.rows(), func(r row) bool { return r.item != nil && r.item.ID == "D1" }) {
				t.Fatal("I lost the mission item scope")
			}
		})
	}
}

func TestAgentHomeEndScrollLongSectionBody(t *testing.T) {
	for _, keys := range [][]string{{"pgdown", "home"}, {"end", "home"}, {"pgdown", "end", "home"}} {
		t.Run(strings.Join(keys, " "), func(t *testing.T) {
			m := treeModel()
			m.mode, m.detail, m.sess, m.agent, m.height = modeAgent, "mission", "lead", "c", 30
			m.board.Projects[0].Sessions[0].Crew[0].Brief = "BRIEF-FIRST\n" + strings.Repeat("middle instruction line\n", 80) + "BRIEF-LAST"
			for _, key := range keys {
				m = press(t, m, key)
				view := ansi.Strip(m.View().Content)
				if key == "end" && !strings.Contains(view, "BRIEF-LAST") {
					t.Fatalf("End did not reach section body end: %s", view)
				}
				if key == "home" && (m.scrollY != 0 || !strings.Contains(view, "Crew needle") || !strings.Contains(view, "BRIEF-FIRST")) {
					t.Fatalf("Home did not restore top viewport: %s", view)
				}
			}
			m = press(t, m, "left")
			r, _ := m.selected()
			if !r.closed {
				t.Fatal("Home/End broke header folding")
			}
		})
	}
}

func TestSearchRefreshQueryChangeAndBundleEnter(t *testing.T) {
	m := treeModel()
	key := "need:" + needID(m.board.Needs[0])
	m = press(t, m, "/", "paste:Bundle", "enter")
	m = to(t, m, func(r row) bool { return rowKey(r) == key })
	m = press(t, m, "enter")
	r, _ := m.selected()
	if !r.closed {
		t.Fatal("Enter did not temporarily fold filtered ask bundle")
	}
	next, _ := m.Update(boardMsg{board: nestedBoard()})
	m = next.(Model)
	r, _ = m.selected()
	if rowKey(r) != key || !r.closed {
		t.Fatal("refresh lost same-query temporary fold")
	}
	m = press(t, m, "/", "ctrl+u", "paste:Bund", "enter")
	m = to(t, m, func(r row) bool { return rowKey(r) == key })
	r, _ = m.selected()
	if r.closed {
		t.Fatal("edited query did not reveal matching bundle")
	}
	m = press(t, m, "enter", "enter", "right")
	r, _ = m.selected()
	if r.item == nil || r.need != nil {
		t.Fatal("bundle Enter/right did not reveal its item")
	}
	m = press(t, m, "esc")
	r, _ = m.selected()
	if rowKey(r) != key || !r.closed {
		t.Fatal("clearing search did not return to the normally folded bundle")
	}
}
