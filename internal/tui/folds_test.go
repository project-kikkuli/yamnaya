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
	b.Tree[0].Children[0].Children[0].Children[0].Live = &sessions[0]
	b.Tree[0].Children[0].Children[0].Children[0].Agents = sessions[1:]
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
	m = treeSelect(t, m, "node:solo")
	b := nestedBoard()
	b.Tree[0].Children[0].Children = append(b.Tree[0].Children[0].Children, b.Tree[len(b.Tree)-1])
	b.Tree = b.Tree[:len(b.Tree)-1]
	m.folds["node:mid"] = true
	next, _ := m.Update(boardMsg{board: b})
	m = next.(Model)
	r, _ := m.selected()
	if rowKey(r) != "node:mid" {
		t.Fatalf("hidden selection did not follow ancestor: %s", rowKey(r))
	}
}

func TestBoardLineageNestsSessionsWithoutHarnessParent(t *testing.T) {
	m, c, _ := setup(t)
	if err := c.Store.Link("dup", "lead"); err != nil {
		t.Fatal(err)
	}
	m = refresh(t, m)
	m.mode, m.detail = modeDetail, "lead"
	found := false
	for _, r := range m.rows() {
		if r.session != nil && r.session.ID == "dup" && r.member == nil {
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
