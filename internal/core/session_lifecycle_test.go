package core

import (
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestOwnerStopSurvivesStaleSnapshotsAndOnlyFreshExecutionResumesIt(t *testing.T) {
	world := newWorld(t)
	c := world.core
	at := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return at }
	world.run(42, 0, "/dev/a", "claude", "lead", Idle)
	old := world.board(t)
	if err := c.ObserveWork(old); err != nil {
		t.Fatal(err)
	}
	all, _ := c.Store.Work.List()
	w := all[0]
	w, err := c.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "brief", Brief: work.Brief{Assessment: "Preserve the caller contract"}})
	if err != nil {
		t.Fatal(err)
	}
	url := "https://github.com/example/query/pull/42"
	w, err = c.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "merge-status", Review: work.Review{ID: work.MergeReviewID(url), URL: url, Action: "merge", State: work.Ready, Version: "head1"}})
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	c.Signal = func(int, syscall.Signal) error { world.procs = nil; return nil }
	if _, err = c.Kill(old.Sessions[0]); err != nil {
		t.Fatal(err)
	}
	assertHidden := func() {
		t.Helper()
		for _, p := range world.board(t).Projects {
			if p.Work != nil && p.Work.ID == w.ID && p.Shown() {
				t.Fatal("stopped mission became an active ghost")
			}
		}
	}
	assertHidden()
	at = at.Add(time.Second)
	old.Projects[0].Sessions[0].Status = Busy
	if err = c.ObserveWork(old); err != nil {
		t.Fatal(err)
	}
	assertHidden()
	w, _ = c.Store.Work.Get(w.ID)
	if w.State != "archived" || w.Brief.Assessment != "Preserve the caller contract" || !w.Reviews[work.MergeReviewID(url)].Pending() {
		t.Fatal("stop lost the brief or owner obligation")
	}
	at = at.Add(time.Second)
	world.run(43, 0, "/dev/b", "claude", "lead", Idle)
	if err = c.ObserveWork(world.board(t)); err != nil {
		t.Fatal(err)
	}
	w, _ = c.Store.Work.Get(w.ID)
	if w.State == "archived" || !w.Observations["lead"].LastLiveAt.Equal(at) {
		t.Fatal("fresh resumed process did not resume the mission")
	}
	world.procs = nil
	at = at.Add(time.Second)
	if err = c.ObserveWork(world.board(t)); err != nil {
		t.Fatal(err)
	}
	w, _ = c.Store.Work.Get(w.ID)
	if w.State == "archived" {
		t.Fatal("old stop archived a genuinely resumed mission on natural exit")
	}
	if _, err = c.Kill(Live{Session: Session{ID: "lead", Harness: "claude", PID: 43}}); err != nil {
		t.Fatal(err)
	}
	assertHidden()
	if err = c.ObserveWork(world.board(t)); err != nil {
		t.Fatal(err)
	}
	w, _ = c.Store.Work.Get(w.ID)
	if w.State != "archived" {
		t.Fatal("killing an already absent process failed to archive the mission")
	}
	var seq int
	var name, path string
	if err = c.Store.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	c.Store.Close()
	c.Store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Store.Close() })
	assertHidden()
}
