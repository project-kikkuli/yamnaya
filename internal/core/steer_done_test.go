package core

import "testing"

func TestVouchedChiefSteerReopensDoneAcrossTwoChiefHosts(t *testing.T) {
	for _, state := range []string{GoalActive, GoalDone, GoalKilled, GoalDropped} {
		t.Run(state, func(t *testing.T) {
			f := newFleet(t)
			top := f.a.chief(t, "top")
			middle := f.c.chief(t, "middle")
			if err := f.c.core.Attach(middle.ID, RemoteKey("a", top.ID)); err != nil {
				t.Fatal(err)
			}
			g, m := f.b.goal(t, "work", "/repo/app")
			if err := f.b.core.Attach(m.ID, RemoteKey("c", middle.ID)); err != nil {
				t.Fatal(err)
			}
			current, _ := f.b.core.Store.Goal(g.ID)
			current.State = state
			if err := f.b.core.Store.SaveGoal(current); err != nil {
				t.Fatal(err)
			}
			_, err := f.b.core.SteerContext(f.b.board(t), m.ID, "new scope", RemoteKey("a", top.ID))
			if state == GoalKilled || state == GoalDropped {
				if err == nil {
					t.Fatal("ended goal accepted steer")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for range 7 {
				f.sync(t)
			}
			after, err := f.b.core.Store.Goal(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			steers, err := f.b.core.Store.Steers(m.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.State != GoalActive || after.WorkRevision != 1 || len(steers) != 1 {
				t.Fatalf("multi-hop steer did not reopen: %+v %+v", after, steers)
			}
		})
	}
}
