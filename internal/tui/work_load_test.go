package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestThousandSimulatedWorkstreamsKeepSparseJudgmentsAndStableSelection(t *testing.T) {
	store, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := map[int]core.Session{}
	var processes []proc.Proc
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("stream-%04d", i)
		repo := fmt.Sprintf("/src/repo-%d/.git", i%10)
		w, err := store.Work.Ensure("simulation/"+id, repo, id)
		if err != nil {
			t.Fatal(err)
		}
		apply := func(e work.Event) {
			t.Helper()
			e.Work, e.Expected = w.ID, w.Revision
			w, err = store.Work.Commit(e)
			if err != nil {
				t.Fatal(err)
			}
		}
		apply(work.Event{Type: "bind", Target: id})
		apply(work.Event{Type: "brief", Brief: work.Brief{Assessment: "Continuing within the delegated contract", Next: "Validate the implementation", Context: 0}})
		if i%100 == 0 {
			apply(work.Event{Type: "observe", Review: work.Review{ID: id + "/PR", Kind: "PR", Title: "Review " + id, Version: "head1", Order: 1}})
			apply(work.Event{Type: "prepare", Target: id + "/PR", Review: work.Review{Version: "head1", Question: "Approve gated rollout?", Recommendation: "approve", Evidence: "rollback verified"}})
		}
		sessions[i+1] = core.Session{ID: id, Title: id, Status: core.Busy, RepoKey: repo, Repo: fmt.Sprintf("repo-%d", i%10)}
		processes = append(processes, proc.Proc{PID: i + 1, TTY: fmt.Sprintf("/dev/%d", i), Args: []string{"claude"}})
	}
	c := &core.Core{Store: store, Harnesses: []core.Harness{harness{sessions: sessions}}, Procs: func() ([]proc.Proc, error) { return processes, nil }}
	b, err := c.Board()
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.loaded, m.board = true, b
	m.alpine.observe(b)
	m.width, m.height = 120, 40
	if len(b.Sessions) != 1000 || len(b.Repos) != 10 {
		t.Fatalf("simulation lost work: %d sessions in %d repos", len(b.Sessions), len(b.Repos))
	}
	ready := 0
	for _, r := range m.rows() {
		if r.review != nil {
			ready++
		}
	}
	if ready != 10 {
		t.Fatalf("1000 workstreams inflated the judgment queue: %d", ready)
	}
	m.cursor = len(m.rows()) - 1
	selected, _ := m.selected()
	key := rowKey(selected)
	m = refresh(t, m)
	after, _ := m.selected()
	if key != rowKey(after) {
		t.Fatal("large refresh lost the selected workstream")
	}
	if lines := strings.Count(m.View(), "\n") + 1; lines > m.height {
		t.Fatalf("large portfolio overflowed terminal height: %d", lines)
	}
	t.Logf("simulation: %d native session records, %d repositories, %d reserved judgments", len(b.Sessions), len(b.Repos), ready)
}
