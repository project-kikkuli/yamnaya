package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestDeadSessionsArchiveWithOrWithoutLaunchRecordsAndKeepOwnerQueue(t *testing.T) {
	for _, state := range []string{"unlandable launch", "missing launch", "already landed"} {
		t.Run(state, func(t *testing.T) {
			m, w := continuing(t)
			var session string
			for s := range w.Bindings {
				session = s
				break
			}
			url := "https://github.com/example/query/pull/42"
			var err error
			w, err = m.core.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "merge-status", Review: work.Review{ID: work.MergeReviewID(url), URL: url, Action: "merge", State: work.Ready, Version: "head1", Title: "Caller migration"}})
			if err != nil {
				t.Fatal(err)
			}
			l := &launches{launched: []core.Launched{}}
			if state == "unlandable launch" {
				l.dormant = []core.Dormant{{Key: "old", Harness: "claude", Session: session, Title: w.Name, State: "unpushed changes"}}
			} else if state == "already landed" {
				l.launched = []core.Launched{{Key: "old", Session: session, State: "landed", Landed: time.Now()}}
			}
			m.core.Launches = l
			m.core.Procs = func() ([]proc.Proc, error) { return nil, nil }
			m.core.Harnesses = nil
			m = refresh(t, m)
			m = to(t, m, func(r row) bool {
				return r.project != nil && r.project.Work != nil && r.project.Work.ID == w.ID && r.review == nil && r.need == nil && r.attention == ""
			})
			if !strings.Contains(strings.Join(m.footer(), "\n"), "K archive") {
				t.Fatal("dead session has no archive action")
			}
			if state != "unlandable launch" && strings.Contains(strings.Join(m.footer(), "\n"), "L land") {
				t.Fatal("offered worktree cleanup without a live launch record")
			}
			m = press(t, m, "K", "y")
			latest, err := m.core.Store.Work.Get(w.ID)
			if err != nil || latest.State != "archived" || latest.Brief.Assessment != w.Brief.Assessment || !latest.Reviews[work.MergeReviewID(url)].Pending() {
				t.Fatalf("archive lost the mission record or obligation: %+v %v", latest, err)
			}
			for _, p := range m.board.Projects {
				if p.Work != nil && p.Work.ID == w.ID && p.Shown() {
					t.Fatal("archiving left an active ghost")
				}
			}
			found := false
			for _, r := range m.rows() {
				found = found || r.review != nil && r.review.ID == work.MergeReviewID(url)
			}
			if !found || len(l.landed) != 0 || len(l.forgot) != 0 {
				t.Fatal("archive removed the owner queue action or invoked workspace cleanup")
			}
		})
	}
}
