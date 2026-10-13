package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/discover"
	"github.com/project-kikkuli/yamnaya/internal/signals"
	"github.com/project-kikkuli/yamnaya/internal/store"
)

// crewDoneShown is how many finished crew stay listed under an expanded session.
const crewDoneShown = 3

func (m *boardModel) session(pane string) (signals.Session, bool) {
	for _, s := range m.snap.Sessions {
		if s.Pane == pane {
			return s, true
		}
	}
	return signals.Session{}, false
}

func (m *boardModel) expandedSession(s signals.Session) bool {
	return (m.sel.kind == rowSession || m.sel.kind == rowCrew) && m.sel.target == s.Pane
}

// shownCrew lists running and idle crew and the few most recently finished.
func shownCrew(crew []discover.Member) (shown []discover.Member, hidden int) {
	done := 0
	for _, c := range crew {
		if c.Status == discover.CrewDone {
			if done++; done > crewDoneShown {
				hidden++
				continue
			}
		}
		shown = append(shown, c)
	}
	return shown, hidden
}

func (m *boardModel) sessionRows() []row {
	var rows []row
	for _, s := range m.snap.Sessions {
		rows = append(rows, row{kind: rowSession, target: s.Pane})
		if !m.expandedSession(s) {
			continue
		}
		shown, _ := shownCrew(s.Crew)
		for _, c := range shown {
			rows = append(rows, row{kind: rowCrew, target: s.Pane, window: c.ID})
		}
	}
	return rows
}

func sessionTitle(s signals.Session) string {
	if s.Title != "" {
		return s.Title
	}
	if s.WindowName != "" {
		return s.WindowName
	}
	return s.Harness
}

func crewSummary(s signals.Session) string {
	if len(s.Crew) == 0 {
		return ""
	}
	if n := s.CrewRunning(); n > 0 {
		return fmt.Sprintf("%d crew · %d running", len(s.Crew), n)
	}
	return fmt.Sprintf("%d crew", len(s.Crew))
}

func (m *boardModel) sessionsView(now time.Time) []string {
	var lines []string
	for _, s := range m.snap.Sessions {
		selected := m.sel.kind == rowSession && m.sel.target == s.Pane
		where := s.Repo
		if s.Branch != "" {
			where += "/" + s.Branch
		}
		if where == "" {
			where = fmt.Sprintf("%s:%d", s.TmuxSession, s.WindowIndex)
		}
		titleStyle, statusStyle := styleDim, styleDim
		if selected {
			titleStyle = stylePlain
		}
		if s.Loud() {
			titleStyle, statusStyle = stylePlain, styleAsk
		}
		title := ansi.Truncate(sessionTitle(s), 44, "…")
		head := titleStyle.Render(fmt.Sprintf("%s%-44s", marker(selected), title)) + " " + styleDim.Render(fmt.Sprintf("%-22s", ansi.Truncate(where, 22, "…")))
		parts := []string{statusStyle.Render(s.Label(now))}
		if c := crewSummary(s); c != "" {
			parts = append(parts, styleDim.Render(c))
		}
		lines = append(lines, head+" "+strings.Join(parts, "  "))
		if !m.expandedSession(s) {
			continue
		}
		shown, hidden := shownCrew(s.Crew)
		for _, c := range shown {
			sel := m.sel.kind == rowCrew && m.sel.target == s.Pane && m.sel.window == c.ID
			style := styleDim
			if sel {
				style = stylePlain
			}
			status := c.Status.String()
			if c.Status != discover.CrewRunning && !c.LastActive.IsZero() {
				status += " " + signals.Duration(now.Sub(c.LastActive))
			}
			lines = append(lines, style.Render(fmt.Sprintf("    %s%-40s %s", marker(sel), ansi.Truncate(c.Label(), 40, "…"), status)))
		}
		if hidden > 0 {
			lines = append(lines, styleDim.Render(fmt.Sprintf("      +%d done", hidden)))
		}
	}
	return lines
}

// promote turns a discovered session into a ym project whose manager is that session's window, without restarting it.
func (a *app) promote(s signals.Session, project string) error {
	if err := validName("project", project); err != nil {
		return err
	}
	if w, err := a.store.OpenWindowByTarget(s.WindowID); err == nil {
		return fmt.Errorf("window %s is already %s/%s", s.WindowID, w.Project, w.Name)
	}
	manager := store.Window{Project: project, Name: store.RoleManager, Role: store.RoleManager, Harness: s.Harness, SessionID: s.ID, Managed: false}
	tag := func(w store.Window) (string, error) {
		return s.WindowID, a.tmux.Tag(s.WindowID, strconv.FormatInt(w.ID, 10))
	}
	gitDir, branch := gitSource(s.Cwd)
	return a.store.CreateProject(store.Project{Name: project, Cwd: s.Cwd, GitDir: gitDir, Branch: branch}, manager, tag)
}

func promoteName(s signals.Session) string {
	if s.Repo != "" {
		return adoptName(s.Repo)
	}
	return adoptName(strings.ToLower(sessionTitle(s)))
}
