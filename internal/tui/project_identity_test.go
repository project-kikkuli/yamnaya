package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

type projectIdentityLaunches struct {
	launches
	targets []core.Live
}

func (l *projectIdentityLaunches) Send(s core.Live, text string) (string, error) {
	l.targets = append(l.targets, s)
	return "sent", nil
}

func TestCollidingProjectNavigationAndActionsStayOnSelectedHost(t *testing.T) {
	for _, localVerifier := range []bool{false, true} {
		name := "two owner missions"
		if localVerifier {
			name = "local verifier and remote owner"
		}
		t.Run(name, func(t *testing.T) {
			m, c, _ := setup(t)
			l := &projectIdentityLaunches{}
			c.Launches = l
			if localVerifier {
				g, err := c.Store.AddGoal(core.Goal{Ask: "verify the importer", Repo: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.Store.AddVerification(core.Verification{Goal: g.ID, Session: "twin"}); err != nil {
					t.Fatal(err)
				}
			}
			r := &remotes{boards: []core.RemoteBoard{{Host: "box", At: time.Now(),
				Sessions: []core.Live{{Session: core.Session{ID: "twin", Harness: "claude", Title: "Remote owner", Status: core.Busy,
					Crew: []core.Member{{ID: "member", Name: "Remote crew", Status: core.Busy}}}, Project: "twin"}},
				Dormant: []core.Dormant{{Remote: "box", Key: "box/exited", Session: "exited-twin", Parent: "twin", Harness: "claude", Title: "Remote exited", State: "exited 0"}},
				Items:   []core.Item{{Host: "box", Seq: 77, ID: "D77", Session: "twin", State: core.ItemOpen, Question: "Remote question"}},
			}}}
			c.Remotes, c.ShowRemotes = r, true
			m.width, m.height, m.peaks.hidden = 120, 40, true
			m = refresh(t, m)
			remoteMission := func(r row) bool { return r.project != nil && r.session == nil && core.ProjectHost(*r.project) == "box" }
			m = to(t, m, remoteMission)
			m = press(t, m, "enter")
			if m.mode != modeDetail {
				t.Fatalf("mission did not open: %v", m.mode)
			}
			for _, row := range m.rows() {
				if row.project == nil || core.ProjectHost(*row.project) != "box" || row.project.Verifier != nil {
					t.Fatalf("detail contains another host's mission: %+v", row)
				}
			}
			if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Remote owner") || strings.Contains(view, "Twin work") {
				t.Fatalf("detail shows another host:\n%s", view)
			}
			m = press(t, m, "esc")
			m = refresh(t, m)
			if row, ok := m.selected(); !ok || !remoteMission(row) {
				t.Fatalf("back/refresh lost selected host: %+v", row)
			}
			m = press(t, m, "enter")
			m = to(t, m, isMember)
			m = press(t, m, "enter")
			s, member, ok := m.member()
			if !ok || s.Remote != "box" || member.Name != "Remote crew" {
				t.Fatalf("crew navigation crossed hosts: session=%+v member=%+v", s, member)
			}
			m = press(t, m, "s", "h", "i", "enter")
			if len(l.targets) != 1 || l.targets[0].Remote != "box" || l.targets[0].Title != "Remote owner" {
				t.Fatalf("send targeted another host: %+v", l.targets)
			}
			m = press(t, m, "esc", "home")
			m = to(t, m, func(r row) bool { return r.session != nil && r.member == nil })
			m = press(t, m, "enter", "r", "h", "i", "enter")
			if len(l.targets) != 2 || l.targets[1].Remote != "box" {
				t.Fatalf("session reply targeted another host: %+v", l.targets)
			}
			m = press(t, m, "I")
			if len(m.items) != 1 || m.items[0].Host != "box" || m.items[0].Seq != 77 {
				t.Fatalf("mission items crossed hosts: %+v", m.items)
			}
			m = press(t, m, "esc", "K", "y")
			if len(r.ran) != 1 || strings.Join(r.ran[0], " ") != "box kill --yes twin" {
				t.Fatalf("kill targeted another host: %v", r.ran)
			}
			m = press(t, m, "esc")
			m = to(t, m, func(r row) bool { return r.dormant != nil })
			m = press(t, m, "K", "y")
			if len(r.ran) != 2 || strings.Join(r.ran[1], " ") != "box kill --yes exited-twin" {
				t.Fatalf("dormant kill targeted another host: %v", r.ran)
			}
			if !localVerifier {
				m = press(t, m, "q", "home")
				m = to(t, m, projectOnHost("twin", ""))
				m = refresh(t, m)
				if row, ok := m.selected(); !ok || !projectOnHost("twin", "")(row) {
					t.Fatalf("refresh moved local selection to remote: %+v", row)
				}
				m = press(t, m, "enter")
				for _, row := range m.rows() {
					if row.project == nil || core.ProjectHost(*row.project) != "" {
						t.Fatalf("local navigation opened remote detail: %+v", row)
					}
				}
			}
		})
	}
}
