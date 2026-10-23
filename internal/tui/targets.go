package tui

import (
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// launchTargets are where a new session can start: this host, then every remembered host.
func (m Model) launchTargets() []string {
	out := []string{""}
	if m.core != nil && m.core.Remotes != nil {
		out = append(out, m.core.Remotes.Hosts()...)
	}
	return out
}

// loadTarget starts the launch input on the host the last launch used, or here when that host is forgotten.
func (m *Model) loadTarget() {
	m.launchOn = ""
	if last, err := m.core.Store.LaunchTarget(); err == nil && slices.Contains(m.launchTargets(), last) {
		m.launchOn = last
	}
}

func (m *Model) nextTarget() {
	ts := m.launchTargets()
	m.launchOn = ts[(slices.Index(ts, m.launchOn)+1)%len(ts)]
}

func (m Model) launching() bool {
	return m.mode == modeInput && (m.inKind == inNew || m.inKind == inNewRepo || m.inKind == inGoal || m.inKind == inChief || m.inKind == inRemit)
}

// targetLine lists the launch targets with the chosen one marked, or "" when this host is the only one.
func (m Model) targetLine() string {
	ts := m.launchTargets()
	if !m.launching() || len(ts) < 2 {
		return ""
	}
	var cells []string
	for _, t := range ts {
		name := "local"
		if t != "" {
			name = m.hostTag(t)
		}
		if t == m.launchOn {
			cells = append(cells, pal.bold.Render("["+name+"]"))
		} else {
			cells = append(cells, pal.dim.Render(" "+name+" "))
		}
	}
	return " " + pal.dim.Render("on ") + strings.Join(cells, " ") + pal.dim.Render("   ctrl+t switches")
}

// remoteArgs is the ym command that starts what the input asked for on another host, naming the repository by its directory name.
func (m Model) remoteArgs(text string) ([]string, error) {
	var args []string
	switch m.inKind {
	case inNew:
		args = []string{"new"}
		if m.newL.Harness == "codex" {
			args = append(args, "--codex")
		}
	case inGoal:
		args = []string{"goal"}
	case inRemit:
		args = []string{"chief"}
		if text != "" {
			args = append(args, "--remit", text)
		}
		text = m.chiefName
		if len(m.inPicks) > 0 {
			var over []string
			for _, id := range m.inPicks {
				switch host, rest, ok := core.SplitRemote(id); {
				case ok && host == m.launchOn:
					over = append(over, rest)
				case ok:
					over = append(over, id)
				default:
					over = append(over, core.RemoteKey(m.core.SelfName(), id))
				}
			}
			args = append(args, "--over", strings.Join(over, ","))
		}
	}
	if m.newL.Repo != "" {
		args = append(args, "--repo", filepath.Base(m.newL.Repo))
	}
	return append(args, "--", text), nil
}

// launchRemote runs the launch on the chosen host through its ym and remembers that host as the next default.
func (m Model) launchRemote(text string) (tea.Model, tea.Cmd) {
	args, err := m.remoteArgs(text)
	if err != nil {
		m.message = "error: " + err.Error()
		return m, nil
	}
	host, tag, c := m.launchOn, m.hostTag(m.launchOn), m.core
	m.picked = nil
	m.message = "starting on " + tag + "…"
	return m, m.do(func() (string, error) {
		out, err := c.RunRemote(host, args...)
		if err != nil {
			return "", err
		}
		first, _, _ := strings.Cut(out, "\n")
		return "started on " + tag + ": " + first, c.Store.SetLaunchTarget(host)
	})
}
