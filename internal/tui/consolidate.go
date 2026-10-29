package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

func (m Model) consolidate(copyItem core.Item) (tea.Model, tea.Cmd) {
	m.askLine("original item (repository:ID or #key); reopens it and closes this copy", "", false, func(m Model, ref string) (tea.Model, tea.Cmd) {
		if ref == "" {
			return m, nil
		}
		m.askLine("consolidate "+copyItem.Qualified()+" into "+ref+" because", "", false, func(m Model, why string) (tea.Model, tea.Cmd) {
			if why == "" {
				return m, nil
			}
			return m, m.do(func() (string, error) {
				if copyItem.Host != "" {
					host := copyItem.Host
					copyItem.Host = ""
					return m.core.RunRemote(host, "item", "consolidate", copyItem.Key(), ref, "--why", why)
				}
				source, err := m.core.Store.Find(copyItem.Scope, ref)
				if err != nil {
					return "", err
				}
				it, err := m.core.Consolidate(copyItem.Key(), source.Key(), why)
				return fmt.Sprintf("%s reopened; %s kept in history", it.Qualified(), copyItem.Qualified()), err
			})
		})
		return m, nil
	})
	return m, nil
}
