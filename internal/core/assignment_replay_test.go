package core

import "testing"

func TestOldLaunchObservationsCannotRebindATerminalAssignment(t *testing.T) {
	for _, state := range []string{"reported", "accepted"} {
		t.Run(state, func(t *testing.T) {
			w := newOrgWorld(t)
			g, manager := w.goal(t, "work", "/repo/app")
			a, err := w.core.AssignWork(manager.Session, "continued-worker", "launch:one", "implement")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.core.Store.db.Exec(`UPDATE ym_assignments SET state=?, report=17, accepted_report=17 WHERE id=?`, state, a.ID); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := w.core.SettleAssignment(a.ID, "old-launch-session", manager.Session); err != nil {
					t.Fatalf("terminal assignment observation failed: %v", err)
				}
			}
			assignments, err := w.core.Store.Assignments(g.ID)
			if err != nil || len(assignments) != 1 {
				t.Fatalf("assignments %+v, %v", assignments, err)
			}
			got := assignments[0]
			if got.State != state || got.Report != 17 || got.AcceptedReport != 17 || got.Session != "continued-worker" || got.Generation != a.Generation {
				t.Fatalf("observation rewrote terminal assignment: %+v", got)
			}
			var links int
			if err := w.core.Store.db.QueryRow(`SELECT count(*) FROM ym_links WHERE child='old-launch-session'`).Scan(&links); err != nil || links != 0 {
				t.Fatalf("old launch rebound into lineage: %d, %v", links, err)
			}
		})
	}
}
