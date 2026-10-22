package core

import (
	"strings"
	"testing"
)

func TestAManagerHearsOnceThatItsAgentStopped(t *testing.T) {
	for _, c := range []struct {
		name   string
		state  string
		killed bool
		want   string
	}{
		{"killed by a signal", "sig9", false, "was killed by signal 9"},
		{"exited", "exited 1", false, "exited with status 1"},
		{"still running", "running", false, ""},
		{"landed", "landed", false, ""},
		{"ended with ym kill", "sig15", true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "make the importer handle CSV", "/repo/august")
			w.spawn("agent", m.Session)
			w.stop("agent")
			if c.killed {
				if err := w.core.Store.RecordKill("agent", w.clock); err != nil {
					t.Fatal(err)
				}
			}
			w.launches.launched = []Launched{
				{Key: "august-wt-csv", Harness: "claude", Session: "agent", State: c.state, Path: "/repo/august-wt-csv"},
				{Key: m.ID, Harness: "claude", Session: m.Session, State: c.state, Path: "/repo/august-wt-mgr"},
			}
			w.loop(t, 3)
			var got []string
			for _, text := range w.handedTo(m.Session) {
				if strings.Contains(text, "stopped running") {
					got = append(got, text)
				}
			}
			if c.want == "" {
				if len(got) != 0 {
					t.Fatalf("manager heard %q", got)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(got[0], c.want) || !strings.Contains(got[0], "ym revive august-wt-csv") {
				t.Fatalf("manager heard %q, want one notice that says %q", got, c.want)
			}
		})
	}
}

func TestAStoppedAgentReachesItsChiefAndTheFeedButNotTheOwnersUnread(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "make the importer handle CSV", "/repo/august")
	chief := w.chief(t, "platform", m.ID)
	w.spawn("agent", m.Session)
	w.stop("agent")
	w.launches.launched = []Launched{{Key: "august-wt-csv", Harness: "claude", Session: "agent", State: "sig9", Path: "/repo/august-wt-csv"}}
	w.loop(t, 3)
	if got := wakes(w.handedTo(chief.Session)); len(got) != 1 || !strings.Contains(got[0], "agent-stopped: agent agent") {
		t.Fatalf("chief heard %q, want one batch with the stopped agent", got)
	}
	n, _ := w.board(t).Node(m.ID)
	if len(n.Feed.Recent) == 0 || n.Feed.Recent[0].Kind != EventAgentStopped || n.Feed.Unread != 0 {
		t.Fatalf("feed %v unread %d, want the stop shown and not counted as news for the owner", kinds(n.Feed.Recent), n.Feed.Unread)
	}
}
