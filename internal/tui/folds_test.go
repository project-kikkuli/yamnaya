package tui

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

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

func TestChiefFoldsAndNavigation(t *testing.T) {
	type step struct{ target, key string }
	all := "top:0 mid:1 deep:2 hit:3 side:2 sibling:1 remote/mid:0 remote/hit:1 empty:0 solo:0"
	midFold := "top:0 mid:1 sibling:1 remote/mid:0 remote/hit:1 empty:0 solo:0"
	for _, tc := range []struct {
		name           string
		steps          []step
		rows, selected string
		folds          []string
	}{
		{"default expanded", nil, all, "top", nil},
		{"top layer", []step{{"top", "left"}}, "top:0 remote/mid:0 remote/hit:1 empty:0 solo:0", "top", []string{"top"}},
		{"second layer", []step{{"mid", "left"}}, midFold, "mid", []string{"mid"}},
		{"third layer", []step{{"deep", "left"}}, "top:0 mid:1 deep:2 side:2 sibling:1 remote/mid:0 remote/hit:1 empty:0 solo:0", "deep", []string{"deep"}},
		{"nested choices survive parent reopening", []step{{"deep", "left"}, {"mid", "left"}, {"top", "left"}, {"", "right"}, {"mid", "right"}}, "top:0 mid:1 deep:2 side:2 sibling:1 remote/mid:0 remote/hit:1 empty:0 solo:0", "mid", []string{"deep"}},
		{"idempotent arrows", []step{{"mid", "left"}, {"", "left"}, {"", "right"}, {"", "right"}}, all, "mid", nil},
		{"host qualified ids", []step{{"mid", "left"}, {"remote/mid", "left"}, {"mid", "right"}}, "top:0 mid:1 deep:2 hit:3 side:2 sibling:1 remote/mid:0 empty:0 solo:0", "mid", []string{"remote/mid"}},
		{"fresh board retains nested folds", []step{{"deep", "left"}, {"mid", "left"}, {"", "refresh"}, {"", "right"}}, "top:0 mid:1 deep:2 side:2 sibling:1 remote/mid:0 remote/hit:1 empty:0 solo:0", "mid", []string{"deep"}},
		{"refresh follows row after insertion", []step{{"mid", "left"}, {"sibling", "insert"}}, "new:0 top:0 mid:1 sibling:1 remote/mid:0 remote/hit:1 empty:0 solo:0", "sibling", []string{"mid"}},
		{"refresh removes selected child", []step{{"mid", "left"}, {"sibling", "remove"}}, "top:0 mid:1 remote/mid:0 remote/hit:1 empty:0 solo:0", "remote/mid", []string{"mid"}},
		{"search reveals hidden match", []step{{"top", "left"}, {"", "/"}, {"", "paste:needle"}, {"", "enter"}}, "top:0 mid:1 deep:2 hit:3", "top", []string{"top"}},
		{"search arrows preserve folds", []step{{"mid", "left"}, {"top", "/"}, {"", "paste:needle"}, {"", "enter"}, {"mid", "right"}, {"", "left"}}, "top:0 mid:1 deep:2 hit:3", "mid", []string{"mid"}},
		{"clearing search selects visible ancestor", []step{{"mid", "left"}, {"", "/"}, {"", "paste:needle"}, {"", "enter"}, {"hit", "esc"}}, midFold, "mid", []string{"mid"}},
		{"matching chief reveals descendants", []step{{"mid", "left"}, {"", "/"}, {"", "paste:mid"}, {"", "enter"}}, "top:0 mid:1 deep:2 hit:3 side:2 remote/mid:0 remote/hit:1", "top", []string{"mid"}},
		{"flat ignores folds", []step{{"mid", "left"}, {"", "t"}}, "hit:0 side:0 sibling:0 remote/hit:0 solo:0", "side", []string{"mid"}},
		{"flat returns to hidden goals ancestor", []step{{"mid", "left"}, {"", "t"}, {"hit", "left"}, {"", "right"}, {"", "t"}}, midFold, "mid", []string{"mid"}},
		{"goal leaf no op", []step{{"hit", "left"}, {"", "right"}}, all, "hit", nil},
		{"empty chief no op", []step{{"empty", "left"}, {"", "right"}}, all, "empty", nil},
		{"navigation skips folded subtree", []step{{"mid", "left"}, {"", "down"}}, midFold, "sibling", []string{"mid"}},
		{"group navigation skips folded subtree", []step{{"top", "left"}, {"", "]"}}, "top:0 remote/mid:0 remote/hit:1 empty:0 solo:0", "solo", []string{"top"}},
		{"chief view retains matching chiefs descendants", []step{{"mid", "left"}, {"", "/"}, {"", "paste:mid"}, {"", "enter"}, {"mid", "enter"}}, "deep:0 hit:1 side:0", "deep", []string{"mid"}},
		{"chief drill filters unmatched siblings", []step{{"top", "/"}, {"", "paste:needle"}, {"", "enter"}, {"top", "enter"}, {"mid", "enter"}}, "deep:0 hit:1", "deep", nil},
		{"ancestor search context on direct drill", []step{{"mid", "left"}, {"top", "/"}, {"", "paste:top"}, {"", "enter"}, {"mid", "enter"}}, "deep:0 hit:1 side:0", "deep", []string{"mid"}},
		{"ancestor search context across recursive drill", []step{{"mid", "left"}, {"top", "/"}, {"", "paste:top"}, {"", "enter"}, {"top", "enter"}, {"mid", "enter"}}, "deep:0 hit:1 side:0", "deep", []string{"mid"}},
		{"ancestor search context after refresh", []step{{"mid", "left"}, {"top", "/"}, {"", "paste:top"}, {"", "enter"}, {"mid", "enter"}, {"", "refresh"}}, "deep:0 hit:1 side:0", "deep", []string{"mid"}},
		{"direct drill back and clear restores folds", []step{{"mid", "left"}, {"top", "left"}, {"", "/"}, {"", "paste:top"}, {"", "enter"}, {"mid", "enter"}, {"", "esc"}, {"", "esc"}}, "top:0 remote/mid:0 remote/hit:1 empty:0 solo:0", "top", []string{"top", "mid"}},
		{"recursive drill back and clear restores folds", []step{{"mid", "left"}, {"top", "left"}, {"", "/"}, {"", "paste:top"}, {"", "enter"}, {"top", "enter"}, {"mid", "enter"}, {"", "esc"}, {"", "esc"}, {"", "esc"}}, "top:0 remote/mid:0 remote/hit:1 empty:0 solo:0", "top", []string{"top", "mid"}},
		{"chief view recursively browses reports", []step{{"top", "enter"}}, "mid:0 deep:1 hit:2 side:1 sibling:0", "mid", nil},
		{"chief view shares nested folds", []step{{"deep", "left"}, {"top", "enter"}, {"mid", "left"}, {"", "right"}}, "mid:0 deep:1 side:1 sibling:0", "mid", []string{"deep"}},
		{"chief drill does not change parent fold", []step{{"top", "left"}, {"", "enter"}}, "mid:0 deep:1 hit:2 side:1 sibling:0", "mid", []string{"top"}},
		{"back follows identity after refresh insertion", []step{{"mid", "enter"}, {"", "insert"}, {"", "esc"}}, "new:0 " + all, "mid", nil},
		{"back from nested chief preserves choice", []step{{"top", "enter"}, {"mid", "enter"}, {"deep", "left"}, {"", "esc"}, {"", "esc"}}, "top:0 mid:1 deep:2 side:2 sibling:1 remote/mid:0 remote/hit:1 empty:0 solo:0", "top", []string{"deep"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(&core.Core{})
			m.board, m.loaded, m.width, m.peaks.hidden = foldBoard(), true, 220, true
			for _, s := range tc.steps {
				if s.target != "" {
					m = to(t, press(t, m, "home"), isNode(s.target))
				}
				switch s.key {
				case "refresh", "insert", "remove":
					b := foldBoard()
					if s.key == "insert" {
						b.Tree = append([]core.Node{{Manager: core.Manager{ID: "new", Name: "new"}, Standing: core.StandMoving}}, b.Tree...)
					}
					if s.key == "remove" {
						b.Tree[0].Children = b.Tree[0].Children[:1]
					}
					next, _ := m.Update(boardMsg{board: b})
					m = next.(Model)
				default:
					m = press(t, m, s.key)
				}
				if _, ok := m.selected(); !ok {
					t.Fatalf("invalid cursor %d after %+v", m.cursor, s)
				}
			}
			var rows []string
			for _, r := range m.rows() {
				rows = append(rows, fmt.Sprintf("%s:%d", r.node.Manager.ID, r.depth))
			}
			if got := strings.Join(rows, " "); got != tc.rows {
				t.Errorf("rows %s, want %s", got, tc.rows)
			}
			r, _ := m.selected()
			if r.node.Manager.ID != tc.selected {
				t.Errorf("selected %s, want %s", r.node.Manager.ID, tc.selected)
			}
			folds := map[string]bool{}
			for _, id := range tc.folds {
				folds[id] = true
			}
			if !maps.Equal(m.collapsed, folds) {
				t.Errorf("folds %v, want %v", m.collapsed, folds)
			}
		})
	}
}

func TestChiefFoldIndicator(t *testing.T) {
	for _, tc := range []struct {
		name, query, id, want string
		folded                bool
	}{
		{"expanded", "", "mid", "▾", false},
		{"folded", "", "mid", "▸", true},
		{"search reveals folded", "needle", "mid", "▾", true},
		{"empty", "", "empty", "", false},
		{"goal", "", "hit", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{board: foldBoard(), filter: tc.query, width: 160, cursor: -1, collapsed: map[string]bool{tc.id: tc.folded}}
			n, _ := m.board.Node(tc.id)
			line := m.nodeLines(-2, n, 1)
			for _, indicator := range []string{"▸", "▾"} {
				if strings.Contains(line, indicator) != (indicator == tc.want) {
					t.Errorf("indicator %q in %q, want %q", indicator, line, tc.want)
				}
			}
		})
	}
}

func TestFoldRefreshReparentsSelectionUnderFoldedChief(t *testing.T) {
	m := New(&core.Core{})
	m.board, m.loaded, m.collapsed = foldBoard(), true, map[string]bool{"mid": true}
	m = to(t, m, isNode("solo"))
	b := foldBoard()
	b.Tree[0].Children[0].Children = append(b.Tree[0].Children[0].Children, b.Tree[len(b.Tree)-1])
	b.Tree = slices.Delete(b.Tree, len(b.Tree)-1, len(b.Tree))
	next, _ := m.Update(boardMsg{board: b})
	m = next.(Model)
	r, ok := m.selected()
	if !ok || r.node.Manager.ID != "mid" {
		t.Fatalf("selected %+v, want folded mid", r)
	}
}
