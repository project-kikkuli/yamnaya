package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// navBoard is a board of two asks, a chief over a goal in repository a, goals in a and b, and sessions in a and b.
func navBoard(t *testing.T) Model {
	t.Helper()
	goal := func(id, repo string) core.Node {
		return core.Node{Manager: core.Manager{ID: "m" + id}, Goal: &core.Goal{ID: id, Ask: "ask " + id, Repo: "/src/" + repo}, Repo: "github.com/o/" + repo, Standing: core.StandMoving}
	}
	mission := func(key, repo string) core.Project {
		return core.Project{Key: key, Name: key, RepoDir: "/src/" + repo}
	}
	m := New(&core.Core{})
	m.board = core.Board{
		Needs: []core.Need{{Item: &core.Item{ID: "D1", Question: "first?"}}, {Item: &core.Item{ID: "D2", Question: "second?"}}},
		Tree: []core.Node{
			goal("g3", "b"),
			{Manager: core.Manager{ID: "c1", Name: "lead"}, Standing: core.StandMoving, Children: []core.Node{goal("g1", "a")}},
			goal("g2", "a"),
		},
		Repos: []core.Repo{
			{Key: "github.com/o/a", Name: "a", Missions: []core.Project{mission("p1", "a"), mission("p2", "a")}},
			{Key: "github.com/o/b", Name: "b", Missions: []core.Project{mission("p3", "b")}},
		},
	}
	m.loaded, m.width, m.peaks.hidden = true, 200, true
	return m
}

// rowName names a row of navBoard by its item, manager or mission, or its repository's name.
func rowName(r row) string {
	switch {
	case r.item != nil:
		return r.item.ID
	case r.node != nil:
		return r.node.Manager.ID
	case r.project != nil:
		return r.project.Key
	case r.repo != nil:
		return "repo " + r.repo.Name
	}
	return "?"
}

func TestGoalsGroupByRepositoryWithChiefsNestingTheirReports(t *testing.T) {
	for _, tc := range []struct {
		flat     bool
		rows     []string
		headings []string
	}{
		{false, []string{"D1", "D2", "c1", "mg1", "mg2", "mg3", "repo a", "p1", "p2", "repo b", "p3"}, []string{"chiefs", "a", "b"}},
		{true, []string{"D1", "D2", "mg1", "mg2", "mg3", "repo a", "p1", "p2", "repo b", "p3"}, []string{"a", "b"}},
	} {
		t.Run(fmt.Sprint("flat ", tc.flat), func(t *testing.T) {
			m := navBoard(t)
			m.flat = tc.flat
			var got []string
			for _, r := range m.rows() {
				got = append(got, rowName(r))
			}
			if !slices.Equal(got, tc.rows) {
				t.Fatalf("rows %v, want %v", got, tc.rows)
			}
			view := ansi.Strip(m.listView().String())
			goals := view[strings.Index(view, "GOALS"):strings.Index(view, "SESSIONS YOU STARTED")]
			var headings []string
			for _, line := range strings.Split(goals, "\n") {
				if strings.HasPrefix(line, "   ") && !strings.HasPrefix(line, "    ") && !strings.Contains(line, "ask ") && !strings.Contains(line, "lead") {
					headings = append(headings, strings.TrimSpace(line))
				}
			}
			if !slices.Equal(headings, tc.headings) {
				t.Fatalf("goal headings %v, want %v:\n%s", headings, tc.headings, goals)
			}
		})
	}
}

func TestNavigationKeysJumpBetweenSectionsRepositoryGroupsAndEnds(t *testing.T) {
	for _, tc := range []struct {
		from string
		keys []string
		want string
	}{
		{"D1", []string{"tab"}, "c1"},
		{"mg1", []string{"tab"}, "repo a"},
		{"p1", []string{"tab"}, "D1"},
		{"D1", []string{"shift+tab"}, "repo a"},
		{"mg2", []string{"shift+tab"}, "D1"},
		{"D1", []string{"]"}, "c1"},
		{"c1", []string{"]"}, "mg2"},
		{"mg2", []string{"]"}, "mg3"},
		{"mg3", []string{"]"}, "repo a"},
		{"p1", []string{"]"}, "repo b"},
		{"p3", []string{"]"}, "p3"},
		{"p3", []string{"["}, "repo a"},
		{"mg2", []string{"["}, "c1"},
		{"c1", []string{"["}, "D1"},
		{"D2", []string{"["}, "D1"},
		{"p1", []string{"<"}, "D1"},
		{"D1", []string{">"}, "p3"},
		{"p2", []string{"home"}, "D1"},
		{"D2", []string{"end"}, "p3"},
		{"D1", []string{"]", "]", "]", "]", "]"}, "repo b"},
	} {
		t.Run(tc.from+" "+strings.Join(tc.keys, " "), func(t *testing.T) {
			m := navBoard(t)
			m = to(t, m, func(r row) bool { return rowName(r) == tc.from })
			m = press(t, m, tc.keys...)
			if r, _ := m.selected(); rowName(r) != tc.want {
				t.Fatalf("cursor on %s, want %s", rowName(r), tc.want)
			}
		})
	}
}

func TestTheBoardFooterIsOneLineAndHelpListsEveryKeyInIt(t *testing.T) {
	m := navBoard(t)
	footer := m.footer()
	if len(footer) != 1 || ansi.Strip(footer[0]) != " "+boardHints {
		t.Fatalf("board footer %q, want the one line %q", footer, boardHints)
	}
	for _, hint := range strings.Split(boardHints, hintSep) {
		for _, key := range strings.Fields(hint) {
			if strings.ContainsAny(key, "abcdefghijklmnopqrstuvwxyz") && len(key) > 1 {
				break
			}
			if !strings.Contains(Help, "\n   "+key+" ") && !strings.Contains(Help, " "+key+" ") {
				t.Errorf("help does not list %q", key)
			}
		}
	}
}

func isAsk(r row) bool { return r.need != nil && r.item != nil }

func TestKeysActOnTheSelectedRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   func(ms []core.Manager) func(row) bool
		keys []string
		kind inputKind
	}{
		{"r on an ask answers it", func([]core.Manager) func(row) bool { return isAsk }, []string{"r"}, inAnswer},
		{"r on a goal sends its manager a message", func(ms []core.Manager) func(row) bool { return isNode(ms[0].ID) }, []string{"r"}, inSteer},
		{"c on a goal opens the chief menu", func(ms []core.Manager) func(row) bool { return isNode(ms[0].ID) }, []string{"c"}, inChiefMenu},
		{"c a picks the chief to report to", func(ms []core.Manager) func(row) bool { return isNode(ms[0].ID) }, []string{"c", "a"}, inAttach},
		{"c n starts a new chief", func(ms []core.Manager) func(row) bool { return isNode(ms[0].ID) }, []string{"c", "n"}, inOver},
		{"c on an ask offers only a new chief", func([]core.Manager) func(row) bool { return isAsk }, []string{"c", "enter"}, inOver},
		{"n picks the repository first", func(ms []core.Manager) func(row) bool { return isNode(ms[0].ID) }, []string{"n"}, inNewRepo},
		{"N starts in the row's repository", func(ms []core.Manager) func(row) bool { return isNode(ms[0].ID) }, []string{"N"}, inGoal},
		{"e renames a mission", func([]core.Manager) func(row) bool { return mission("lead") }, []string{"e"}, inRename},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, ms := goalSetup(t, "goal one")
			m = press(t, to(t, m, tc.at(ms)), tc.keys...)
			if m.mode != modeInput || m.inKind != tc.kind {
				t.Fatalf("%v went to mode %v input %v, want input %v:\n%s", tc.keys, m.mode, m.inKind, tc.kind, m.View().Content)
			}
		})
	}
}

func TestReplyOnASessionSendsItAFollowUp(t *testing.T) {
	m, _, _ := goalSetup(t)
	m = press(t, to(t, m, mission("lead")), "enter")
	m = press(t, to(t, m, isSession("lead")), "r")
	if m.mode != modeInput || m.inKind != inSend || m.inItem != "lead" && !strings.HasSuffix(m.inItem, "lead") {
		t.Fatalf("r on a session went to mode %v input %v for %q", m.mode, m.inKind, m.inItem)
	}
}
