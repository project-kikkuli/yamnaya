package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// followBoard is a board of Needs you rows named by the first letter of their kind (D item, m merge, r review, d doc, s waiting
// session, G stuck goal), then goals g, then missions p; head versions every PR, review and doc so each build reads them anew.
func followBoard(needs, goals, missions []string, head int) core.Board {
	b := core.Board{}
	for _, n := range needs {
		at := fmt.Sprint(head)
		switch n[0] {
		case 'D':
			b.Needs = append(b.Needs, core.Need{Item: &core.Item{ID: n, Question: n + "?"}})
		case 'm':
			b.Needs = append(b.Needs, core.Need{PR: &core.PR{URL: "https://x/pull/" + n, Status: &core.PRStatus{Merge: core.PRMergeFacts{OID: at}}}})
		case 'r':
			b.Needs = append(b.Needs, core.Need{Review: &core.ReviewRequest{URL: "https://x/review/" + n, Updated: at}})
		case 'd':
			b.Needs = append(b.Needs, core.Need{Doc: &core.DocAsk{Page: n, Title: n}, Docs: []core.DocAsk{{Key: n, Page: n, Version: at}}})
		case 's':
			b.Needs = append(b.Needs, core.Need{Session: &core.Live{Session: core.Session{ID: n, Status: core.Waiting}}})
		case 'G':
			b.Needs = append(b.Needs, core.Need{Goal: &core.Goal{ID: n, Ask: n}})
		}
	}
	for _, g := range goals {
		b.Tree = append(b.Tree, core.Node{Manager: core.Manager{ID: g}, Goal: &core.Goal{ID: g, Ask: "ask " + g, Repo: "/src/a"}, Repo: "github.com/o/a", Standing: core.StandMoving})
	}
	var ps []core.Project
	for _, p := range missions {
		ps = append(ps, core.Project{Key: p, Name: p, RepoDir: "/src/a"})
	}
	if len(ps) > 0 {
		b.Repos = []core.Repo{{Key: "github.com/o/a", Name: "a", Missions: ps}}
	}
	return b
}

// followName names a followBoard row as followBoard was given it.
func followName(r row) string {
	switch n := r.need; {
	case n == nil:
	case n.PR != nil:
		return strings.TrimPrefix(n.PR.URL, "https://x/pull/")
	case n.Review != nil:
		return strings.TrimPrefix(n.Review.URL, "https://x/review/")
	case n.Doc != nil:
		return n.Doc.Page
	case n.Goal != nil:
		return n.Goal.ID
	case n.Item != nil:
		return n.Item.ID
	case n.Session != nil:
		return n.Session.ID
	}
	switch {
	case r.node != nil:
		return r.node.Manager.ID
	case r.project != nil:
		return r.project.Key
	case r.repo != nil:
		return "repo"
	}
	return "?"
}

func TestTheCursorStaysOnItsRowAcrossRefreshesAndFallsToItsOwnSection(t *testing.T) {
	needs := []string{"D1", "m1", "r1", "d1", "s1", "G1", "m2"}
	goals := []string{"g1", "g2", "g3"}
	missions := []string{"p1", "p2", "p3"}
	without := func(xs []string, x string) []string {
		return slices.DeleteFunc(slices.Clone(xs), func(y string) bool { return y == x })
	}
	sections := func(sel string) (own []string, all [3][]string) {
		all = [3][]string{needs, goals, missions}
		for i, s := range all {
			if slices.Contains(s, sel) {
				return all[i], all
			}
		}
		return nil, all
	}
	for _, sel := range []string{"m1", "m2", "r1", "d1", "s1", "G1", "D1", "g2", "g3", "p2", "p3"} {
		for _, tc := range []struct {
			name   string
			change func(own []string, all [3][]string) [3][]string
			want   func(own []string) string
		}{
			{"refresh", func(_ []string, all [3][]string) [3][]string { return all }, nil},
			{"reorder", func(_ []string, all [3][]string) [3][]string {
				for i := range all {
					all[i] = slices.Clone(all[i])
					slices.Reverse(all[i])
				}
				return all
			}, nil},
			{"insert above", func(_ []string, all [3][]string) [3][]string {
				return [3][]string{append([]string{"m0", "D0"}, all[0]...), append([]string{"g0"}, all[1]...), append([]string{"p0"}, all[2]...)}
			}, nil},
			{"remove above", func(own []string, all [3][]string) [3][]string {
				for i := range all {
					if all[i][0] != sel {
						all[i] = all[i][1:]
					}
				}
				return all
			}, nil},
			{"remove selected", func(own []string, all [3][]string) [3][]string {
				for i := range all {
					all[i] = without(all[i], sel)
				}
				return all
			}, func(own []string) string {
				if i := slices.Index(own, sel); i+1 < len(own) {
					return own[i+1]
				}
				return own[len(own)-2]
			}},
		} {
			t.Run(sel+" "+tc.name, func(t *testing.T) {
				m := New(&core.Core{})
				m.loaded, m.width, m.peaks.hidden = true, 200, true
				m.board = followBoard(needs, goals, missions, 0)
				m = to(t, m, func(r row) bool { return followName(r) == sel })
				want := sel
				own, all := sections(sel)
				if tc.want != nil {
					want = tc.want(own)
				}
				all = tc.change(own, all)
				for head := 1; head <= 3; head++ {
					next, _ := m.Update(boardMsg{board: followBoard(all[0], all[1], all[2], head)})
					m = next.(Model)
					if r, _ := m.selected(); followName(r) != want {
						t.Fatalf("refresh %d put the cursor on %s, want %s:\n%s", head, followName(r), want, m.View().Content)
					}
				}
			})
		}
	}
}
