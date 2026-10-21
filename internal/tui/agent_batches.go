package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func (m Model) selectChief(a work.Agent, selected bool) Model {
	copy := map[string]work.Agent{}
	for id, a := range m.chiefSelected {
		copy[id] = a
	}
	if selected {
		copy[a.ID] = a
	} else {
		delete(copy, a.ID)
	}
	m.chiefSelected = copy
	return m
}

func (m Model) selectedChiefs() []work.Agent {
	var out []work.Agent
	for _, a := range m.chiefSelected {
		for _, current := range m.board.Agents {
			if current.ID == a.ID {
				a = current
				break
			}
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].ID < out[j].ID
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (m Model) composeChiefMessage(agents []work.Agent) (tea.Model, tea.Cmd, bool) {
	m.chiefRecipients = append([]work.Agent(nil), agents...)
	m.chiefBatchID = uuid.NewString()
	m.scrollY = 0
	m.startInput(inChiefMessage, "", "", "", false)
	return m, nil, true
}

func (m Model) chiefComposeView() viewPage {
	b := viewPage{}
	b.WriteString(pal.bold.Render(fmt.Sprintf(" MESSAGE TO %d MANAGERS", len(m.chiefRecipients))) + "\n\n")
	b.WriteString(" Recipients are frozen. Enter sends once; Esc cancels.\n\n")
	for _, a := range m.chiefRecipients {
		m.field(&b, a.Name, a.ID)
	}
	return b
}

func batchSummary(b work.AgentBatch) string {
	counts := map[string]int{}
	for _, r := range b.Recipients {
		counts[r.Status()]++
	}
	parts := []string{}
	for _, state := range []string{"pending", "accepted", "delivered", "incorporated", "failed"} {
		if counts[state] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[state], state))
		}
	}
	return core.Short(b.ID) + " · " + strings.Join(parts, " · ")
}

func (m Model) currentAgentBatch() (work.AgentBatch, bool) {
	for _, b := range m.board.AgentBatches {
		if m.receiptBatch == "" || b.ID == m.receiptBatch {
			return b, true
		}
	}
	return work.AgentBatch{}, false
}

func (m Model) agentBatchView() viewPage {
	b := viewPage{}
	batch, ok := m.currentAgentBatch()
	if !ok {
		b.WriteString(" Message receipts are unavailable; refresh or inspect : agent batches.\n")
		return b
	}
	b.WriteString(pal.bold.Render(" MANAGER MESSAGE") + "\n\n")
	m.field(&b, "status", batchSummary(batch))
	m.field(&b, "from", batch.Actor)
	m.field(&b, "message", batch.Text)
	b.WriteString("\n Acceptance records the message. Delivery hands it to native execution.\n Incorporation requires a checkpoint covering its input revision.\n\n")
	for _, r := range batch.Recipients {
		m.field(&b, r.Name, r.Status()+" · "+r.Agent)
		m.field(&b, "failure", r.Error)
	}
	return b
}

func (m Model) agentBatchKey(k tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.mode != modeAgentBatch {
		return m, nil, false
	}
	switch k.String() {
	case "r":
		if b, ok := m.currentAgentBatch(); ok {
			return m, m.do(func() (string, error) {
				return "Retry recorded for failed recipients", m.core.Store.Work.DispatchAgentBatch(b.ID, true)
			}), true
		}
	case "[", "]":
		for i, b := range m.board.AgentBatches {
			if b.ID != m.receiptBatch {
				continue
			}
			delta := 1
			if k.String() == "[" {
				delta = -1
			}
			m.receiptBatch = m.board.AgentBatches[max(0, min(len(m.board.AgentBatches)-1, i+delta))].ID
			m.scrollY = 0
			break
		}
	case "up", "k":
		m.scrollY = max(0, m.scrollY-1)
	case "down", "j":
		m.scrollY++
	case "pgup":
		m.scrollY = max(0, m.scrollY-m.height/2)
	case "pgdown", " ":
		m.scrollY += m.height / 2
	case "esc", "q":
		m.pop()
	default:
		return m, nil, false
	}
	return m, nil, true
}
