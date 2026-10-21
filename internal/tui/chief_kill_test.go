package tui

import (
	"syscall"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestKillingAChiefStopsItsExecutionAndItNeverWakesAgain(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "running"}[running], func(t *testing.T) {
			m, agents := managers(t)
			c := m.core
			a, err := c.Store.Work.CreateAgent("Atlas", "chief", "", "Keep compatibility work coherent", "Coordinate; publication needs owner approval", work.Execution{Harness: "codex", Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			w, err := c.Store.Work.Ensure("query", "query", "Compatibility")
			if err != nil {
				t.Fatal(err)
			}
			if err = c.Store.Work.Attach(a.ID, w.ID, "follow"); err != nil {
				t.Fatal(err)
			}
			if err = c.Store.Work.SetRule(a.ID, "", false); err != nil {
				t.Fatal(err)
			}
			var signalled []int
			c.Signal = func(pid int, sig syscall.Signal) error {
				signalled = append(signalled, pid)
				return nil
			}
			if running {
				c.Procs = func() ([]proc.Proc, error) {
					return []proc.Proc{{PID: 4242, Args: []string{"/usr/local/bin/ym", "agent-run", a.ID}}, {PID: 7, Args: []string{"/usr/local/bin/ym", "agent-run", agents[1].ID}}}, nil
				}
			}
			m = refresh(t, m)
			m = to(t, m, func(r row) bool { return r.chief != nil && r.chief.ID == a.ID })
			m = refresh(t, press(t, m, "K", "y"))
			for _, r := range m.rows() {
				if r.chief.ID == a.ID {
					t.Fatal("killed chief is still listed")
				}
			}
			if want := map[bool]int{false: 0, true: 1}[running]; len(signalled) != want || (running && signalled[0] != -4242) {
				t.Fatalf("signalled %v, want only the chief's process group", signalled)
			}
			links, _ := c.Store.Work.Attachments()
			rules, _ := c.Store.Work.Rules()
			if len(links) != 0 || len(rules) != 0 {
				t.Fatalf("attachments %v rules %v survived", links, rules)
			}
			if ws, _ := c.Store.Work.Get(a.Workspace); ws.State != "archived" {
				t.Fatalf("chief workspace is %q", ws.State)
			}
			w, _ = c.Store.Work.Get(w.ID)
			if _, err = c.ChangeWork(w, work.Event{Type: "message", Actor: "owner", Text: "Callers changed"}); err != nil {
				t.Fatal(err)
			}
			due, _ := c.Store.Work.DueAgents()
			for _, d := range due {
				if d.ID == a.ID {
					t.Fatal("killed chief was woken by its old work")
				}
			}
		})
	}
}
