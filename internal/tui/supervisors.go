package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

func (m Model) supervisorRows() ([]prow, []pdo) {
	rows := []prow{}
	for _, mg := range m.board.Org.Ordered() {
		if mg.Role != core.RoleSupervisor || mg.Host != "" || mg.Retired {
			continue
		}
		id := mg.ID
		targets, err := m.core.Store.SupervisorTargets(id)
		if err != nil {
			rows = append(rows, prow{text: err.Error()})
			continue
		}
		rows = append(rows, prow{text: pal.bold.Render(mg.Shown()) + "  " + strings.Join(targets, ", "), key: "supervisor:" + id, does: []pdo{
			{"enter", "evidence and reports", func(m Model) (tea.Model, tea.Cmd) {
				view, err := m.core.Supervisor(id)
				if err != nil {
					m.message = err.Error()
					return m, nil
				}
				raw, err := json.MarshalIndent(view, "", "  ")
				if err != nil {
					m.message = err.Error()
					return m, nil
				}
				m.show("O", string(raw))
				return m, nil
			}},
			{"a", "attach work", func(m Model) (tea.Model, tea.Cmd) { return m.supervisorLinks(id, false) }},
			{"d", "detach work", func(m Model) (tea.Model, tea.Cmd) { return m.supervisorLinks(id, true) }},
			{"c", "append owner correction", func(m Model) (tea.Model, tea.Cmd) {
				m.askLine("owner correction for "+mg.Shown(), "", false, func(m Model, text string) (tea.Model, tea.Cmd) {
					if strings.TrimSpace(text) == "" {
						return m, nil
					}
					return m, m.do(func() (string, error) {
						view, err := m.core.Supervisor(id)
						if err != nil {
							return "", err
						}
						st, err := m.core.SteerContext(m.board, id, view.Context+"\n\nOwner correction:\n"+text, core.Owner)
						return fmt.Sprintf("supervisor context revision %d", st.Revision), err
					})
				})
				return m, nil
			}},
		}})
	}
	if len(rows) == 0 {
		rows = append(rows, prow{text: "No local supervisors. n starts Claude; N starts Codex."})
	}
	return rows, []pdo{
		{"n", "new Claude supervisor", func(m Model) (tea.Model, tea.Cmd) { return m.newSupervisor("claude") }},
		{"N", "new Codex supervisor", func(m Model) (tea.Model, tea.Cmd) { return m.newSupervisor("codex") }},
	}
}

func supervisorRefs(text string) []string {
	refs := []string{}
	for _, ref := range strings.Split(text, ",") {
		if ref = strings.TrimSpace(ref); ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func (m Model) supervisorLinks(id string, detach bool) (tea.Model, tea.Cmd) {
	verb := "attach"
	if detach {
		verb = "detach"
	}
	m.askLine(verb+" managers/chiefs (comma-separated refs, including host/id)", "", false, func(m Model, text string) (tea.Model, tea.Cmd) {
		refs := supervisorRefs(text)
		if len(refs) == 0 {
			return m, nil
		}
		return m, m.do(func() (string, error) {
			for _, ref := range refs {
				if err := m.core.SupervisorAttach(id, ref, detach); err != nil {
					return "", err
				}
			}
			return fmt.Sprintf("%s: %d supervision attachments", verb, len(refs)), nil
		})
	})
	return m, nil
}

func (m Model) newSupervisor(harness string) (tea.Model, tea.Cmd) {
	m.askLine("new local "+harness+" supervisor, name", "", false, func(m Model, name string) (tea.Model, tea.Cmd) {
		if strings.TrimSpace(name) == "" {
			return m, nil
		}
		m.askLine("original owner intent and authorization limits", "", false, func(m Model, intent string) (tea.Model, tea.Cmd) {
			if strings.TrimSpace(intent) == "" {
				return m, nil
			}
			m.askLine("attachments (comma-separated managers/chiefs or host/id; empty: none)", "", false, func(m Model, text string) (tea.Model, tea.Cmd) {
				return m, m.do(func() (string, error) {
					s, err := m.core.NewSupervisor(name, intent, harness, "", supervisorRefs(text))
					if err != nil {
						return "", err
					}
					if s.LastError != "" {
						return s.Shown() + " recorded; launch deferred: " + s.LastError, nil
					}
					return "supervisor " + s.Shown() + " started locally", nil
				})
			})
			return m, nil
		})
		return m, nil
	})
	return m, nil
}
