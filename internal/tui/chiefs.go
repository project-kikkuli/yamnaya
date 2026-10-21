package tui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func (m Model) agentName(id string) string {
	for _, a := range m.board.Agents {
		if a.ID == id {
			return a.Name
		}
	}
	return id
}

func (m Model) chiefCount() int {
	n := 0
	for _, a := range m.board.Agents {
		if a.Role == "chief" {
			n++
		}
	}
	return n
}

func (m Model) chiefRows() []row {
	var out []row
	for _, a := range m.board.Agents {
		if !m.matches(a.Name, a.Role) {
			continue
		}
		for _, p := range m.board.Projects {
			if p.Work != nil && p.Work.ID == a.Workspace {
				out = append(out, row{chief: &a, work: p.Work, project: &p})
				break
			}
		}
	}
	return out
}

func (m Model) managementAttention() []row {
	var out []row
	for _, p := range m.board.Projects {
		if p.Work == nil {
			continue
		}
		w := p.Work
		if w.Kind == "intake" && pendingReviews(*w) > 0 && w.Owner == "" {
			out = append(out, row{attention: "unassigned", work: w})
			continue
		}
		if w.State == "verifying" && w.Parent == "" {
			out = append(out, row{attention: "verify result", work: w})
		}
		for _, a := range m.board.Agents {
			if a.Workspace == w.ID && a.Error != "" && w.Parent == "" {
				out = append(out, row{attention: "investigate", work: w})
			}
		}
	}
	return out
}

func (m Model) chiefState(a work.Agent) string {
	if a.Paused {
		return "paused"
	}
	if a.Error != "" {
		return "needs investigation"
	}
	if a.Running {
		return "working"
	}
	if a.Pending > 0 {
		return fmt.Sprintf("%d changed", a.Pending)
	}
	return "watching"
}

func (m Model) chiefsView() viewPage {
	b := viewPage{}
	b.WriteString(pal.bold.Render(" YOUR AGENTS") + pal.dim.Render("  context · judgment · follow-through") + "\n\n")
	if len(m.chiefSelected) > 0 {
		m.field(&b, "selected", fmt.Sprintf("%d managers; includes selections outside the current filter", len(m.chiefSelected)))
	}
	if batch, ok := m.currentAgentBatch(); ok {
		m.field(&b, "message", batchSummary(batch)+" · v receipts")
	}
	if m.chiefTarget != "" {
		m.field(&b, "attach to", m.workName(m.chiefTarget))
		b.WriteString("\n")
	}
	for i, r := range m.rows() {
		a := r.chief
		style := pal.dim
		if !a.Paused {
			if a.Error != "" {
				style = pal.red
			} else if a.Running {
				style = pal.green
			} else if a.Pending > 0 {
				style = pal.yellow
			}
		}
		selected := "[ ] "
		if _, ok := m.chiefSelected[a.ID]; ok {
			selected = "[x] "
		}
		attached := ""
		if m.chiefTarget != "" && m.attached(a.ID, m.chiefTarget) {
			attached = "  " + pal.green.Render("attached")
		}
		b.WriteString(m.mark(i) + selected + m.hue(a.ID).Render(pad(a.Name, min(28, max(12, m.width/3)))) + " " + pad(a.Role, 8) + " " + style.Render(m.chiefState(*a)) + "  " + m.missionSpend(*r.project) + attached + "\n")
		if i == m.cursor {
			m.field(&b, "remit", r.work.Purpose)
			m.field(&b, "assessment", r.work.Brief.Assessment)
			m.field(&b, "next", r.work.Next)
			m.field(&b, "waiting for", r.work.WaitFor)
			m.field(&b, "investigate", a.Error)
			for _, rule := range m.board.AgentRules {
				if rule.Agent == a.ID {
					scope := rule.Repo
					if scope == "" {
						scope = "all new sessions"
					}
					m.field(&b, "default", scope)
				}
			}
			for _, link := range m.board.Attachments {
				if link.Agent == a.ID {
					m.field(&b, "follows", m.workName(link.Work)+" · "+link.Reason)
				}
			}
			b.WriteString("\n")
		}
	}
	if len(m.rows()) == 0 {
		b.WriteString(" No delegated agents yet. Press n to give a chief a name, remit, and authority.\n")
	}
	return b
}

func (m Model) workName(id string) string {
	for _, p := range m.board.Projects {
		if p.Work != nil && p.Work.ID == id {
			return p.Work.Name
		}
	}
	return id
}

func (m Model) chiefKey(k tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if next, cmd, handled := m.agentBatchKey(k); handled {
		return next, cmd, true
	}
	if k.String() == "M" {
		m.chiefTarget = ""
		if w, ok := m.selectedWork(); ok {
			m.chiefTarget = w.ID
		}
		m.push(modeChiefs)
		return m, nil, true
	}
	if k.String() == "c" && m.chiefPickable() {
		w, ok := m.selectedWork()
		if !ok {
			m.message = "This row has no continuing work record to give a chief."
			return m, nil, true
		}
		m.chiefTarget = w.ID
		m.push(modeChiefs)
		return m, nil, true
	}
	if m.mode != modeChiefs {
		return m, nil, false
	}
	switch k.String() {
	case "/":
		m.startInput(inFilter, "", "", m.filter, false)
		return m, nil, true
	case "ctrl+u":
		m.chiefSelected = nil
		return m, nil, true
	case "*":
		for _, r := range m.chiefRows() {
			m = m.selectChief(*r.chief, true)
		}
		return m, nil, true
	case "v":
		if b, ok := m.currentAgentBatch(); ok {
			m.receiptBatch = b.ID
			m.push(modeAgentBatch)
		}
		return m, nil, true
	case "s":
		if len(m.chiefSelected) > 0 {
			return m.composeChiefMessage(m.selectedChiefs())
		}
	}
	if k.String() == "n" {
		m.chiefDraft = work.Agent{Role: "chief", Execution: work.Execution{Harness: "codex"}}
		m.startInput(inChiefName, "", "", "", false)
		return m, nil, true
	}
	r, ok := m.selected()
	if !ok || r.chief == nil {
		return m, nil, false
	}
	a := *r.chief
	switch k.String() {
	case " ":
		_, selected := m.chiefSelected[a.ID]
		return m.selectChief(a, !selected), nil, true
	case "enter", "B":
		if k.String() == "enter" && m.chiefTarget != "" {
			return m.attachChief(a)
		}
		m.openWork(*r.work)
		return m, nil, true
	case "s":
		return m.composeChiefMessage([]work.Agent{a})
	case "a":
		return m.attachChief(a)
	case "x":
		target := m.chiefTarget
		if target == "" || !m.attached(a.ID, target) {
			m.message = a.Name + " is not attached here."
			return m, nil, true
		}
		return m, m.do(func() (string, error) {
			return "Chief detached from " + m.workName(target), m.core.Store.Work.Detach(a.ID, target)
		}), true
	case "d", "D":
		repo := ""
		if k.String() == "d" {
			for _, p := range m.board.Projects {
				if p.Work != nil && p.Work.ID == m.chiefTarget {
					repo = p.Work.Repo
				}
			}
			if repo == "" {
				m.message = "Open M from a repository project to set its default."
				return m, nil, true
			}
		}
		remove := false
		for _, rule := range m.board.AgentRules {
			if rule.Agent == a.ID && rule.Repo == repo {
				remove = true
			}
		}
		return m, m.do(func() (string, error) {
			return "New-session attachment rule updated", m.core.Store.Work.SetRule(a.ID, repo, remove)
		}), true
	case "K":
		b := m.board
		m.confirm("kill "+a.Name+"? stops its execution and drops its attachments and defaults; it never wakes again", m.do(func() (string, error) { return m.core.KillAgent(a, b) }))
		return m, nil, true
	case "p":
		a.Paused = !a.Paused
		return m, m.do(func() (string, error) { return "Agent availability updated", m.core.Store.Work.SaveAgent(a) }), true
	}
	return m, nil, false
}

func (m Model) attachChief(a work.Agent) (tea.Model, tea.Cmd, bool) {
	if m.chiefTarget == "" {
		m.message = "Press c on a project or session to attach its work."
		return m, nil, true
	}
	target := m.chiefTarget
	return m, m.do(func() (string, error) {
		return "Chief attached to " + m.workName(target), m.core.Store.Work.Attach(a.ID, target, "Carry context and coordinate follow-through under the chief remit")
	}), true
}

func (m Model) chiefPickable() bool {
	switch m.mode {
	case modeList, modeRepo, modeDetail, modeSession:
		return m.workAction == ""
	}
	return false
}

func (m Model) attached(agent, id string) bool {
	for _, link := range m.board.Attachments {
		if link.Agent == agent && link.Work == id {
			return true
		}
	}
	return false
}

func (m Model) chiefsOn(id string) string {
	var names []string
	for _, link := range m.board.Attachments {
		if link.Work == id {
			names = append(names, m.agentName(link.Agent))
		}
	}
	return strings.Join(names, ", ")
}

func (m Model) chiefInput(text string) (tea.Model, tea.Cmd, bool) {
	if m.inKind == inChiefMessage {
		if strings.TrimSpace(text) == "" {
			return m, nil, true
		}
		id, recipients := m.chiefBatchID, append([]work.Agent(nil), m.chiefRecipients...)
		m.receiptBatch = id
		return m, m.do(func() (string, error) {
			b, err := m.core.Store.Work.SendAgentBatch(id, "owner", text, recipients)
			return batchSummary(b), err
		}), true
	}
	if m.inKind != inChiefName && m.inKind != inChiefRemit && m.inKind != inChiefAuthority {
		return m, nil, false
	}
	if strings.TrimSpace(text) == "" {
		m.message = "A chief needs a name, remit, and explicit authority."
		return m, nil, true
	}
	switch m.inKind {
	case inChiefName:
		m.chiefDraft.Name = text
		m.startInput(inChiefRemit, "", "", "", false)
		return m, nil, true
	case inChiefRemit:
		m.chiefRemit = text
		m.startInput(inChiefAuthority, "", "", "", false)
		return m, nil, true
	}
	a, remit, target := m.chiefDraft, m.chiefRemit, m.chiefTarget
	dir, _ := os.Getwd()
	a.Execution.Directory = dir
	if target != "" {
		for _, p := range m.board.Projects {
			if p.Work != nil && p.Work.ID == target {
				a.Repo = p.Work.Repo
				for _, s := range p.Sessions {
					if s.Cwd != "" {
						a.Execution.Directory = s.Cwd
						break
					}
				}
			}
		}
	}
	return m, m.do(func() (string, error) {
		created, err := m.core.Store.Work.CreateAgent(a.Name, "chief", a.Repo, remit, text, a.Execution)
		if err != nil {
			return "", err
		}
		if target != "" {
			err = m.core.Store.Work.Attach(created.ID, target, "Carry context and coordinate follow-through under the chief remit")
		}
		return "Chief created; initial assessment is queued", err
	}), true
}

func (m Model) agentFields(b *viewPage, w work.Scope) {
	for _, a := range m.board.Agents {
		if a.Workspace == w.ID {
			m.field(b, "agent", a.Name+" · "+a.Role+" · "+m.chiefState(a))
			m.field(b, "execution", a.Execution.Harness+" · "+a.Execution.Directory)
			m.field(b, "session", a.Session)
			m.field(b, "investigate", a.Error)
			for _, link := range m.board.Attachments {
				if link.Agent == a.ID {
					m.field(b, "follows", m.workName(link.Work)+" · "+link.Reason)
				}
			}
		}
	}
}
