package core

import (
	"slices"
	"sort"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// signalled makes Stop's SIGTERM end the fake process it reaches, safely from the goroutines a goal kill stops sessions on.
func signalled(w *orgWorld) {
	var mu sync.Mutex
	w.core.KillGrace = 200 * time.Millisecond
	w.core.Procs = func() ([]proc.Proc, error) {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(w.procs), nil
	}
	w.core.Signal = func(pid int, _ syscall.Signal) error {
		mu.Lock()
		defer mu.Unlock()
		w.procs = slices.DeleteFunc(w.procs, func(p proc.Proc) bool { return p.PID == pid })
		return nil
	}
}

func TestKillingAGoalEndsItsManagerAgentsAndVerifierAndShowsItKilled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verifier bool
	}{{"working", false}, {"verifying", true}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			other, om := w.goal(t, "fix the docs", "/repo/august")
			w.spawn("agent-1", m.Session)
			w.spawn("agent-2", "agent-1")
			w.spawn("outsider", "")
			want := []string{m.Session, "agent-1", "agent-2"}
			if tc.verifier {
				if _, _, err := w.core.FinishGoal(m.Session, "shipped", nil); err != nil {
					t.Fatal(err)
				}
				w.supervise(t)
				want = append(want, w.launches.starts[len(w.launches.starts)-1].Session)
			}
			signalled(w)
			b := w.board(t)
			if _, err := w.core.KillGoal(b, g.ID); err != nil {
				t.Fatal(err)
			}
			ended := slices.Clone(w.launches.forgot)
			sort.Strings(ended)
			sort.Strings(want)
			if !slices.Equal(ended, want) {
				t.Fatalf("ended %v, want %v", ended, want)
			}
			b = w.supervise(t)
			for _, id := range append(want, "outsider", om.Session) {
				_, alive := b.Session(id)
				if gone := !alive; gone != slices.Contains(want, id) {
					t.Fatalf("after the kill %s alive %v", id, alive)
				}
			}
			n, ok := b.Node(m.ID)
			if now, _ := w.core.Store.Goal(g.ID); now.State != GoalKilled || !ok || n.Standing != StandKilled {
				t.Fatalf("goal %s on the tree %v standing %q; want killed", now.State, ok, n.Standing)
			}
			if kept, _ := w.core.Store.Goal(other.ID); kept.State != GoalActive {
				t.Fatalf("the other goal is %s", kept.State)
			}
			w.clock = w.clock.Add(DoneShown)
			if _, ok := w.board(t).Node(m.ID); ok {
				t.Fatal("a killed goal stays on the tree past a day")
			}
		})
	}
}

func TestKillingAChiefRetiresItAloneAndItsGoalsReportUp(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "ship the importer", "/repo/august")
	ch := w.chief(t, "platform", m.ID)
	w.supervise(t)
	signalled(w)
	b := w.board(t)
	if _, err := w.core.KillGoal(b, ch.ID); err != nil {
		t.Fatal(err)
	}
	b = w.supervise(t)
	o, _ := w.core.LoadOrg()
	if !o.Managers[ch.ID].Retired || o.Managers[ch.ID].Session == "" || !slices.Equal(w.launches.forgot, []string{o.Managers[ch.ID].Session}) {
		t.Fatalf("chief retired %v, ended %v", o.Managers[ch.ID].Retired, w.launches.forgot)
	}
	if _, alive := b.Session(m.Session); !alive || o.Above(o.Managers[m.ID].ParentID) != Owner {
		t.Fatalf("its goal's manager alive %v reports to %q; want running and reporting to the owner", alive, o.Above(o.Managers[m.ID].ParentID))
	}
}
