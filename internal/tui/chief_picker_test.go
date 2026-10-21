package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestChiefPickerAttachesAndDetachesTheSelectedRowsWork(t *testing.T) {
	for _, where := range []string{"board mission", "session row", "session detail"} {
		t.Run(where, func(t *testing.T) {
			m, c, _ := setup(t)
			chief, err := c.Store.Work.CreateAgent("Atlas", "chief", "", "Keep lead work coherent", "Coordinate; publication needs owner approval", work.Execution{Harness: "codex", Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			m = refresh(t, m)
			m = to(t, m, mission("lead"))
			switch where {
			case "session row":
				m = press(t, m, "enter")
				m = to(t, m, isSession("lead"))
			case "session detail":
				m = press(t, m, "enter")
				m = to(t, m, isSession("lead"))
				m = press(t, m, "enter")
			}
			if err := c.ObserveWork(m.board); err != nil {
				t.Fatal(err)
			}
			m = refresh(t, m)
			target, ok := m.selectedWork()
			if !ok {
				t.Fatal("fixture row has no work")
			}
			if !strings.Contains(ansi.Strip(strings.Join(m.footer(), "\n")), "c chief") {
				t.Fatalf("c chief hint missing:\n%s", m.View())
			}
			m = press(t, m, "c")
			if m.mode != modeChiefs || m.chiefTarget != target.ID {
				t.Fatalf("c did not open the picker for %s: mode=%v target=%q", target.ID, m.mode, m.chiefTarget)
			}
			m = to(t, m, func(r row) bool { return r.chief != nil && r.chief.ID == chief.ID })
			m = refresh(t, press(t, m, "enter"))
			links, _ := c.Store.Work.Attachments()
			if len(links) != 1 || links[0].Agent != chief.ID || links[0].Work != target.ID {
				t.Fatalf("enter did not attach: %+v", links)
			}
			if !strings.Contains(chiefLine(m, "Atlas"), "attached") {
				t.Fatalf("picker does not mark the attached chief:\n%s", m.View())
			}
			back := refresh(t, press(t, m, "esc"))
			if !namesChief(ansi.Strip(back.View()), "Atlas") {
				t.Fatalf("row does not name its chief:\n%s", back.View())
			}
			m = refresh(t, press(t, m, "x"))
			if links, _ := c.Store.Work.Attachments(); len(links) != 0 {
				t.Fatalf("x did not detach: %+v", links)
			}
			if strings.Contains(chiefLine(m, "Atlas"), "attached") {
				t.Fatalf("picker still marks a detached chief:\n%s", m.View())
			}
		})
	}
}

func chiefLine(m Model, name string) string {
	for _, line := range strings.Split(ansi.Strip(m.chiefsView().String()), "\n") {
		if strings.Contains(line, name) {
			return line
		}
	}
	return ""
}

func namesChief(page, name string) bool {
	for _, line := range strings.Split(page, "\n") {
		if strings.Contains(line, "chief") && strings.Contains(line, name) {
			return true
		}
	}
	return false
}
