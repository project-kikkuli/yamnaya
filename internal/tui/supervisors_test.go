package tui

import (
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestSupervisorPanelCreatesAndEditsIndependentAttachments(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			m, c, ms := goalSetup(t, "ship CSV")
			if err := c.Store.SetLaunchTarget("remote-execution"); err != nil {
				t.Fatal(err)
			}
			m = press(t, m, "O")
			if m.pn.kind != panelSupervisors || !strings.Contains(m.View().Content, "SUPERVISORS") {
				t.Fatal("supervisor panel did not open")
			}
			key := "n"
			if harness == "codex" {
				key = "N"
			}
			m = press(t, m, key)
			m = press(t, m, "paste:"+"intent")
			m = press(t, m, "enter")
			m = press(t, m, "paste:"+"Preserve API compatibility.")
			m = press(t, m, "enter")
			m = press(t, m, "paste:"+ms[0].ID)
			m = press(t, m, "enter")
			target, _ := c.Store.LaunchTarget()
			if target != "remote-execution" {
				t.Fatal("local supervisor changed the execution launch target")
			}
			view, err := c.Supervisor("intent")
			if err != nil || view.Supervisor.Harness != harness || len(view.Targets) != 1 {
				t.Fatalf("view %+v,%v", view, err)
			}
			m = refresh(t, m)
			m.pn.cur = m.firstPanelRow(0, 1)
			m = press(t, m, "c")
			m = press(t, m, "paste:"+"Add CSV coverage.")
			m = press(t, m, "enter")
			view, err = c.Supervisor("intent")
			if err != nil || view.Revision != 1 || view.OriginalIntent != "Preserve API compatibility." {
				t.Fatalf("context %+v,%v", view, err)
			}
			m = press(t, m, "d")
			m = press(t, m, "paste:"+ms[0].ID)
			m = press(t, m, "enter")
			view, _ = c.Supervisor("intent")
			if len(view.Targets) != 0 {
				t.Fatal("board detach failed")
			}
			m = press(t, m, "a")
			m = press(t, m, "paste:"+ms[0].ID)
			m = press(t, m, "enter")
			view, _ = c.Supervisor("intent")
			if len(view.Targets) != 1 {
				t.Fatal("board attach failed")
			}
			m = press(t, m, "enter")
			if m.mode != modeScreen || !strings.Contains(m.screen, "OriginalIntent") || !strings.Contains(m.screen, "Add CSV coverage.") {
				t.Fatal("board omitted persistent context or evidence")
			}
			actual, _ := c.Store.Manager(ms[0].ID)
			if actual.ParentID != core.Owner {
				t.Fatal("board supervision reparented execution")
			}
		})
	}
}
