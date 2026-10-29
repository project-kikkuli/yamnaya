package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func live(id string) core.Live { return core.Live{Session: core.Session{ID: id, Title: id}} }

// broadcastBoard is a chief over two goals in two repositories, and a repository with a mission the owner runs.
func broadcastBoard() core.Board {
	owned := core.Project{Key: "p1", Name: "owned", Repo: "rk1", RepoName: "one", Sessions: []core.Live{live("s1"), live("s2")},
		Dormant: []core.Dormant{{Key: "old", Harness: "claude", Session: "old", State: "exited 0"}}}
	managed := core.Project{Key: "p2", Name: "managed", Repo: "rk1", RepoName: "one", Sessions: []core.Live{live("a1")}, Manager: "m1"}
	chiefLive, m1, m2 := live("cs"), live("m1s"), live("m2s")
	tree := []core.Node{{
		Manager: core.Manager{ID: "c1", Role: "chief", Name: "boss"}, Live: &chiefLive,
		Children: []core.Node{
			{Manager: core.Manager{ID: "m1", Role: "manager", Name: "one", ParentID: "c1"}, Goal: &core.Goal{ID: "g1", Ask: "first"}, Repo: "rk1",
				Live: &m1, Agents: []core.Live{live("a1")}},
			{Manager: core.Manager{ID: "m2", Role: "manager", Name: "two", ParentID: "c1"}, Goal: &core.Goal{ID: "g2", Ask: "second"}, Repo: "rk2",
				Live: &m2, Agents: []core.Live{live("a2")}},
		},
	}}
	return core.Board{
		Tree:     tree,
		Projects: []core.Project{owned, managed},
		Repos:    []core.Repo{{Key: "rk1", Name: "one", Missions: []core.Project{owned, managed}}},
		Sessions: []core.Live{live("s1"), live("s2"), chiefLive, m1, live("a1"), m2, live("a2"), {Session: core.Session{PID: 9}}},
		Stale:    []core.Need{{Item: &core.Item{ID: "D9", Question: "old"}}},
	}
}

func TestBroadcastReachesEveryAgentUnderTheCursorsLevel(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  mode
		at    func(row) bool
		scope string
		want  []string
	}{
		{"needs you rows reach every agent", modeList, func(r row) bool { return r.stale }, "every agent",
			[]string{"a1", "a2", "cs", "m1s", "m2s", "s1", "s2"}},
		{"chief reaches its whole subtree", modeList, func(r row) bool { return r.node != nil && r.node.Manager.ID == "c1" }, "boss (c1) and everyone below",
			[]string{"a1", "a2", "cs", "m1s", "m2s"}},
		{"goal reaches its manager and agents", modeList, func(r row) bool { return r.node != nil && r.node.Manager.ID == "m2" }, "two (m2)",
			[]string{"a2", "m2s"}},
		{"repository reaches its missions and goals", modeList, func(r row) bool { return r.repo != nil && r.project == nil }, "repository one",
			[]string{"a1", "m1s", "s1", "s2"}},
		{"mission reaches its sessions", modeList, func(r row) bool { return r.project != nil && r.project.Key == "p1" }, "mission owned",
			[]string{"s1", "s2"}},
		{"session reaches itself", modeDetail, isSession("s2"), "s2",
			[]string{"s2"}},
		{"a row with no agent of its own takes the drilled view", modeDetail, func(r row) bool { return r.dormant != nil }, "mission owned",
			[]string{"s1", "s2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &launches{}
			m := New(&core.Core{Launches: l})
			m.board, m.mode, m.detail = broadcastBoard(), tc.mode, "p1"
			m.showStale = true
			m = to(t, m, tc.at)
			m = press(t, m, broadcastKey)
			if m.mode != modeInput || m.inKind != inBroadcast {
				t.Fatalf("! did not open the broadcast prompt: mode %v, message %q", m.mode, m.message)
			}
			if !strings.Contains(m.inLabel, tc.scope) || !strings.Contains(m.inLabel, plural(len(tc.want), "agent")) {
				t.Fatalf("prompt %q, want scope %q and %d agents", m.inLabel, tc.scope, len(tc.want))
			}
			m = press(t, m, "h", "i")
			next, cmd := m.Update(keyOf("enter"))
			if cmd == nil {
				t.Fatalf("enter sent nothing")
			}
			if done := cmd().(doneMsg); done.err != nil {
				t.Fatal(done.err)
			}
			var want []string
			for _, id := range tc.want {
				want = append(want, id+" hi")
			}
			slices.Sort(l.sent)
			if !slices.Equal(l.sent, want) {
				t.Fatalf("sent %q, want %q", l.sent, want)
			}
			if next.(Model).mode == modeInput {
				t.Fatal("prompt stayed open")
			}
		})
	}
}
