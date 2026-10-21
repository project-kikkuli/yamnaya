package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/google/uuid"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func sortedReviews(w work.Scope) []work.Review {
	out := make([]work.Review, 0, len(w.Reviews))
	for _, r := range w.Reviews {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].State != out[j].State {
			return out[i].State < out[j].State
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (m Model) reviewRows(state string) []row {
	var out []row
	seen := map[string]bool{}
	for _, p := range m.board.Projects {
		if p.Work == nil || seen[p.Work.ID] {
			continue
		}
		seen[p.Work.ID] = true
		for _, review := range sortedReviews(*p.Work) {
			if review.QueueState() == state && !review.Closed {
				r := review
				out = append(out, row{work: p.Work, review: &r, project: &p})
			}
		}
	}
	return out
}

func (m Model) workScope() (work.Scope, bool) {
	for _, p := range m.board.Projects {
		if p.Work != nil && p.Work.ID == m.workID {
			return *p.Work, true
		}
	}
	return work.Scope{}, false
}

func (m Model) selectedWork() (work.Scope, bool) {
	if m.mode == modeWork || m.mode == modeReview {
		return m.workScope()
	}
	if r, ok := m.selected(); ok {
		if r.work != nil {
			return *r.work, true
		}
		if r.project != nil && r.project.Work != nil {
			return *r.project.Work, true
		}
		if r.need != nil {
			if p, ok := m.board.Project(r.need.Project); ok && p.Work != nil {
				return *p.Work, true
			}
		}
		if r.session != nil {
			for _, p := range m.board.Projects {
				if p.Work != nil {
					if _, ok := p.Work.Bindings[r.session.Key()]; ok {
						return *p.Work, true
					}
				}
			}
		}
	}
	if m.mode == modeDetail || m.mode == modeSession || m.mode == modeAgent {
		if p, ok := m.board.Project(m.detail); ok && p.Work != nil {
			return *p.Work, true
		}
	}
	return work.Scope{}, false
}

func (m *Model) openWork(w work.Scope) {
	m.push(modeWork)
	m.workID, m.workPanel, m.scrollY = w.ID, 0, 0
	m.workSince = max(0, w.Revision-w.Changes)
	for _, p := range m.board.Projects {
		if p.Work != nil && p.Work.ID == w.ID {
			m.detail = p.Key
			break
		}
	}
	m.workRevision = -1
	m.loadWorkHistory()
	if m.core != nil {
		m.core.Store.Work.Seen(w.ID, w.Revision)
	}
}

func (m *Model) openWorkPanel(w work.Scope, panel int) {
	if m.mode != modeWork || m.workID != w.ID {
		m.openWork(w)
	}
	m.workPanel, m.scrollY, m.workAction = panel, 0, ""
}

func (m *Model) chooseWork(key string) {
	panel := 0
	if key == "C" {
		panel = 2
	}
	if w, ok := m.selectedWork(); ok {
		m.openWorkPanel(w, panel)
		return
	}
	if r, ok := m.selected(); ok && r.repo != nil && r.project == nil {
		var scopes []work.Scope
		seen := map[string]bool{}
		for _, p := range r.repo.Missions {
			if p.Work != nil && !seen[p.Work.ID] {
				scopes = append(scopes, *p.Work)
				seen[p.Work.ID] = true
			}
		}
		if len(scopes) == 1 {
			m.openWorkPanel(scopes[0], panel)
			return
		}
		if len(scopes) > 1 {
			repo := r.repo.Key
			m.push(modeRepo)
			m.repo, m.workAction = repo, key
			return
		}
	}
	m.message = "Choose a project with continuing work to read its brief or context."
}

func briefAddress(w work.Scope) (string, error) {
	path, err := core.DefaultStorePath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(path), "work-service.json"))
	if err != nil {
		return "", err
	}
	var service struct{ Address string }
	if err = json.Unmarshal(raw, &service); err != nil {
		return "", err
	}
	if service.Address == "" {
		return "", fmt.Errorf("brief service has no address")
	}
	return service.Address + "/brief/" + w.ID, nil
}

func (m *Model) loadWorkHistory() {
	w, ok := m.workScope()
	if !ok || m.core == nil || w.Revision == m.workRevision {
		return
	}
	if w.Host != "" {
		m.workHistory, m.workRevision = w.Activity, w.Revision
		m.workSince = max(0, w.Revision-w.Changes-(w.Revision-int64(len(w.Activity))))
		return
	}
	events, err := m.core.Store.Work.History(w.ID, 0)
	if err != nil {
		m.message = err.Error()
		return
	}
	m.workHistory, m.workRevision = events, w.Revision
}

func (m Model) workKey(k tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.mode == modeInput || m.mode == modeHelp || m.mode == modeConfirm || m.mode == modePick || m.mode == modeWhy {
		return m, nil, false
	}
	if next, cmd, handled := m.chiefKey(k); handled {
		return next, cmd, true
	}
	key := k.String()
	if r, ok := m.currentReview(); ok && r.Action == "merge" && (key == "x" || key == "u") {
		return m, m.reviewAction(key), true
	}
	if m.mode == modeReviews || m.mode == modeReview {
		if key == "x" || key == "u" || (key == " " && m.mode == modeReviews) {
			if key == " " {
				key = "x"
			}
			return m, m.reviewAction(key), true
		}
		if m.mode == modeReview && (key == "n" || key == "p") {
			delta := 1
			if key == "p" {
				delta = -1
			}
			m.moveReview(delta)
			return m, nil, true
		}
	}
	if key == "enter" {
		if r, ok := m.selected(); ok && r.attention != "" {
			if r.attention == "unassigned" {
				m.openReviewQueue(*r.work)
			} else {
				m.openWork(*r.work)
			}
			return m, nil, true
		}
	}
	if key == "enter" {
		if r, ok := m.selected(); ok && r.review != nil {
			m.openReview(r)
			return m, nil, true
		}
	}
	if key == ":" {
		m.startInput(inCommand, "", "", "", false)
		return m, nil, true
	}
	if key == "R" {
		m.push(modeReviews)
		m.reviewWork, m.reviewFilter = "", ""
		return m, nil, true
	}
	if m.mode == modeRepo && m.workAction != "" && key == "enter" {
		if r, ok := m.selected(); ok && r.project != nil && r.project.Work != nil {
			panel := 0
			if m.workAction == "C" {
				panel = 2
			}
			m.openWorkPanel(*r.project.Work, panel)
		} else {
			m.message = "This project has no continuing work record yet."
		}
		return m, nil, true
	}
	if key == "B" || key == "C" {
		m.chooseWork(key)
		return m, nil, true
	}
	if key == "e" && m.mode == modeWork && m.workPanel == 2 {
		if w, ok := m.workScope(); ok {
			return m, m.editContext(w), true
		}
	}
	sourceKey := key == "o" && (m.mode == modeReviews || m.mode == modeReview)
	if r, ok := m.selected(); ok && r.review != nil && key == "o" {
		sourceKey = true
	}
	if r, ok := m.selected(); ok && r.attention == "unassigned" && (key == "o" || key == "V") {
		m.openReviewQueue(*r.work)
		sourceKey = true
	}
	if key == "V" || sourceKey {
		w, ok := m.selectedWork()
		if !ok {
			return m, nil, true
		}
		address := w.Brief.Artifact
		if m.mode == modeReview {
			address = w.Reviews[m.reviewID].URL
		} else if r, ok := m.selected(); ok && r.review != nil {
			address = r.review.URL
		}
		if address == "" {
			if m.mode == modeReview || m.mode == modeReviews {
				m.message = "This review has no source URL."
				return m, nil, true
			}
			var err error
			address, err = briefAddress(w)
			if err != nil {
				m.message = "Brief service is unavailable; run ym serve."
				return m, nil, true
			}
		}

		return m, m.do(func() (string, error) {
			bin := "xdg-open"
			if runtime.GOOS == "darwin" {
				bin = "open"
			}
			return "opened " + address, exec.Command(bin, address).Run()
		}), true
	}
	if m.mode == modeReviews {
		if key == "tab" {
			states := []string{"pending", "", "incoming", work.Preparing, work.WithAuthor, work.Reviewed, "dismissed", work.Completed}
			if m.reviewWork != "" {
				states = []string{"pending", work.Reviewed, "dismissed", work.WithAuthor, work.Completed}
			}
			for i, s := range states {
				if s == m.reviewFilter {
					m.reviewFilter = states[(i+1)%len(states)]
					break
				}
			}
			m.cursor = 0
			return m, nil, true
		}
		if key == "enter" {
			if r, ok := m.selected(); ok && r.review != nil {
				m.openReview(r)
			}
			return m, nil, true
		}
	}
	if m.mode == modeWork {
		if r, ok := m.selected(); ok && r.target != "" {
			if key == "s" {
				m.inputWork = *r.work
				m.startInput(inWorkMessage, r.target, "", "", false)
				m.inLabel = r.target
				return m, nil, true
			}
			if key == "o" || key == "enter" {
				host, target := r.work.Host, r.target
				command := "~/.local/bin/ym open '" + strings.ReplaceAll(target, "'", "'\"'\"'") + "'"
				return m, tea.ExecProcess(exec.Command("ssh", "-t", "-oBatchMode=yes", host, command), func(err error) tea.Msg { return doneMsg{err: err} }), true
			}
		}
		if key == "a" {
			if w, ok := m.workScope(); ok && w.State == "verifying" {
				m.inputWork = w
				m.startInput(inWorkVerify, "", "", "", false)
				return m, nil, true
			}
		}
		if key == "enter" {
			if r, ok := m.selected(); ok && r.work != nil && r.review == nil && r.session == nil && r.work.ID != m.workID {
				m.openWork(*r.work)
				return m, nil, true
			}
		}
		if key == "tab" {
			m.workPanel = (m.workPanel + 1) % 4
			m.cursor, m.scrollY = 0, 0
			return m, nil, true
		}
		if key == "s" {
			if m.workPanel == 1 {
				if r, ok := m.selected(); ok && r.session != nil {
					return m, nil, false
				}
			}
			if w, ok := m.workScope(); ok {
				m.inputWork = w
				m.startInput(inWorkMessage, "", "", "", false)
				m.inLabel = w.Manager
			}
			return m, nil, true
		}
		if key == "enter" {
			if r, ok := m.selected(); ok && r.review != nil {
				m.openReview(r)
				return m, nil, true
			}
		}
	}
	if m.mode == modeReview {
		if key == "a" || key == "f" {
			w, ok := m.workScope()
			if !ok {
				return m, nil, true
			}
			r := w.Reviews[m.reviewID]
			if r.Action == "merge" {
				m.message = "Open the PR with o/V to merge it. Its outcome is tracked from GitHub."
				return m, nil, true
			}
			if r.State != work.Ready {
				m.message = "This review is not prepared for your judgment."
				return m, nil, true
			}
			m.inputWork = w
			kind := inWorkDecision
			if key == "f" {
				kind = inWorkFeedback
			}
			m.startInput(kind, m.reviewID, "", "", false)
			return m, nil, true
		}
	}
	if m.mode == modeReview || (m.mode == modeWork && m.workPanel != 1) {
		switch key {
		case "up", "k":
			m.scrollY = max(0, m.scrollY-1)
			return m, nil, true
		case "down", "j":
			m.scrollY++
			return m, nil, true
		case "pgdown", " ":
			m.scrollY += m.height / 2
			return m, nil, true
		case "pgup", "b":
			m.scrollY = max(0, m.scrollY-m.height/2)
			return m, nil, true
		}
	}
	if key == "s" {
		if r, ok := m.selected(); ok && r.session != nil {
			return m, nil, false
		}
		if w, ok := m.selectedWork(); ok {
			m.inputWork = w
			m.startInput(inWorkMessage, "", "", "", false)
			m.inLabel = w.Manager
			return m, nil, true
		}
	}
	return m, nil, false
}

func (m Model) editContext(w work.Scope) tea.Cmd {
	f, err := os.CreateTemp("", "ym-context-*")
	if err != nil {
		return func() tea.Msg { return doneMsg{err: err} }
	}
	path := f.Name()
	if _, err = f.WriteString(w.Context.Text); err != nil {
		f.Close()
		os.Remove(path)
		return func() tea.Msg { return doneMsg{err: err} }
	}
	f.Close()
	editor := strings.Fields(os.Getenv("EDITOR"))
	if len(editor) == 0 {
		editor = []string{"vi"}
	}
	cmd := exec.Command(editor[0], append(editor[1:], path)...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer os.Remove(path)
		if err != nil {
			return doneMsg{err: err}
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return doneMsg{err: err}
		}
		if string(raw) == w.Context.Text {
			return doneMsg{message: "context unchanged"}
		}
		_, err = m.core.ChangeWork(w, work.Event{ID: uuid.NewString(), Type: "context", Actor: "owner", Text: string(raw)})
		message := "context recorded; delivery and adoption are tracked separately"
		if w.Host != "" {
			message = "context queued for " + w.Host + "; awaiting application"
		}
		return doneMsg{message: message, err: err}
	})
}

func (m Model) workInput(text string) (tea.Model, tea.Cmd, bool) {
	if next, cmd, handled := m.chiefInput(text); handled {
		return next, cmd, true
	}
	if m.inKind == inCommand {
		return m, func() tea.Msg {
			args, err := commandWords(text)
			if err != nil {
				return commandMsg{err: err}
			}
			if len(args) == 0 {
				return commandMsg{}
			}
			if args[0] == "ym" {
				args = args[1:]
			}
			if len(args) == 0 {
				return commandMsg{}
			}
			self, err := os.Executable()
			if err != nil {
				return commandMsg{err: err}
			}
			raw, err := exec.Command(self, args...).CombinedOutput()
			return commandMsg{text: strings.TrimSpace(string(raw)), err: err}
		}, true
	}
	if m.inKind != inWorkFeedback && m.inKind != inWorkDecision && m.inKind != inWorkMessage && m.inKind != inWorkVerify {
		return m, nil, false
	}
	if text == "" {
		return m, nil, true
	}
	kind := "decide"
	if m.inKind == inWorkVerify {
		kind = "verify"
	}
	if m.inKind == inWorkFeedback {
		kind = "feedback"
	}
	if m.inKind == inWorkMessage {
		kind = "message"
	}
	w := m.inputWork
	target := m.inItem
	if kind == "message" {
		if target == "" {
			target = w.Manager
			if w.Owner != "" {
				target = w.Owner
			}
		}
	}
	return m, m.do(func() (string, error) {
		_, err := m.core.ChangeWork(w, work.Event{ID: uuid.NewString(), Type: kind, Actor: "owner", Target: target, Text: text, Context: w.Context.Revision})
		if w.Host != "" {
			return "queued for " + w.Host + "; awaiting application", err
		}
		return kind + " recorded; next action remains tracked", err
	}), true
}

func (m Model) workView() viewPage {
	w, ok := m.workScope()
	if !ok {
		return textPage(" continuing work is unavailable; esc returns to the board", "")
	}
	b := viewPage{place: "m:" + m.detail}
	b.WriteString(pal.bold.Render(" "+w.Name) + pal.dim.Render("  "+core.Short(w.ID)+" · responsible "+m.agentName(w.Responsible())) + "\n")
	panels := []string{"brief", "workstreams", "context", "history"}
	for i, name := range panels {
		if i == m.workPanel {
			b.WriteString(pal.bold.Render(" [" + name + "] "))
		} else {
			b.WriteString(pal.dim.Render(" " + name + " "))
		}
	}
	b.WriteString("\n\n")
	switch m.workPanel {
	case 0:
		if !w.BriefCurrent() {
			b.WriteString(pal.yellow.Render(" Assessment needs refreshing against current context or evidence.") + "\n\n")
		}
		m.field(&b, "assessment", w.Brief.Assessment)
		m.field(&b, "changed", w.Brief.Changed)
		m.field(&b, "purpose", w.Purpose)
		m.field(&b, "owner", m.agentName(w.Owner))
		m.field(&b, "next", w.Next)
		m.field(&b, "waiting for", w.WaitFor)
		m.field(&b, "parent", w.Parent)
		m.field(&b, "result", w.Result)
		m.field(&b, "verified by", w.VerifiedBy)
		m.agentFields(&b, w)
		m.field(&b, "approach", w.Brief.Rationale)
		m.field(&b, "uncertain", w.Brief.Uncertainty)
		m.field(&b, "next", w.Brief.Next)
		m.field(&b, "authority", w.Authority)
		m.field(&b, "context", w.Context.Text)
		if address, err := briefAddress(w); err == nil {
			m.field(&b, "browser", address)
		}
		m.field(&b, "artifact", w.Brief.Artifact)
		related := make([]string, 0, len(w.Related))
		for id := range w.Related {
			related = append(related, id)
		}
		sort.Strings(related)
		for _, id := range related {
			m.field(&b, "related", id+" · "+w.Related[id])
		}
		if w.Brief.Assessment == "" {
			b.WriteString(pal.dim.Render(" No manager assessment published. The manager publishes it with ym work brief.") + "\n")
		}
		ready, preparing, author := 0, 0, 0
		for _, r := range w.Reviews {
			switch r.State {
			case work.Ready:
				ready++
			case work.Preparing:
				preparing++
			case work.WithAuthor:
				author++
			}
		}
		b.WriteString(fmt.Sprintf("\n %d judgments ready · %d being prepared · %d with authors\n", ready, preparing, author))
		m.field(&b, "trigger", w.Trigger)
		for _, source := range w.Sources {
			m.field(&b, "coverage", source.Kind+" "+source.URL+" · "+source.Coverage)
		}
		for _, issue := range w.CoverageErrors {
			m.field(&b, "unavailable", issue)
		}
		if m.workSince < int64(len(m.workHistory)) {
			b.WriteString(pal.bold.Render(" CHANGED SINCE YOUR LAST READ") + "\n")
			changes := m.workHistory[m.workSince:]
			for _, e := range changes[max(0, len(changes)-5):] {
				summary := e.Text
				if e.Type == "brief" {
					summary = e.Brief.Changed
				}
				if e.Type == "observe" {
					summary = e.Review.Title + " · " + e.Review.Version
				}
				m.field(&b, e.Type, summary)
			}
			b.WriteString("\n")
		}

	case 1:
		for i, r := range m.rows() {
			switch {
			case r.work != nil && r.work.ID != w.ID && r.review == nil && r.session == nil:
				b.WriteString(m.mark(i) + pad(r.work.State, 12) + " " + r.work.Name + "\n")
				if i == m.cursor {
					m.field(&b, "assessment", r.work.Brief.Assessment)
					m.field(&b, "next", r.work.Next)
				}
			case r.target != "":
				b.WriteString(m.mark(i) + pad("remote", 10) + " " + r.target + pal.dim.Render("  "+r.work.Host+" · s message · o native session") + "\n")
			case r.review != nil:
				b.WriteString(m.mark(i) + pal.yellow.Render(pad(r.review.Kind, 10)) + " " + pad(r.review.State, 12) + " " + r.review.Title + "\n")
			case r.session != nil:
				b.WriteString(m.mark(i) + pad(r.session.Status.String(), 10) + " " + titleOf(*r.session) + pal.dim.Render("  "+r.session.Location()) + "\n")
			}
		}
	case 2:
		b.WriteString(fmt.Sprintf(" CONTEXT r%d · e edits in $EDITOR\n\n", w.Context.Revision))
		if strings.TrimSpace(w.Context.Text) == "" {
			b.WriteString(pal.dim.Render(" No shared context recorded. Press e to add guidance for the project.") + "\n\n")
		}
		m.field(&b, "direction by", w.Context.Actor)
		m.field(&b, "guidance", w.Context.Text)
		b.WriteString("\n INCORPORATED BY\n")
		ids := make([]string, 0, len(w.Adoptions))
		for id := range w.Adoptions {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			state := fmt.Sprintf("r%d incorporated", w.Adoptions[id])
			if w.Adoptions[id] < w.Context.Revision {
				state = "awaiting incorporation"
			}
			b.WriteString("   " + m.agentName(id) + "  " + state + "\n")
		}
		sessions := make([]string, 0, len(w.Bindings))
		for session := range w.Bindings {
			sessions = append(sessions, session)
		}
		sort.Strings(sessions)
		for _, session := range sessions {
			state := fmt.Sprintf("r%d incorporated", w.Bindings[session])
			if w.Bindings[session] < w.Context.Revision {
				state = "awaiting adoption"
			}
			b.WriteString("   " + session + "  " + state + "\n")
		}
	case 3:
		for _, e := range m.workHistory {
			b.WriteString(pal.bold.Render(" "+e.Type) + pal.dim.Render(" · "+e.Actor) + "\n")
			m.field(&b, "", e.Text)
			if e.Type == "brief" {
				m.field(&b, "assessment", e.Brief.Assessment)
				m.field(&b, "changed", e.Brief.Changed)
			}
			if e.Type == "observe" {
				m.field(&b, "source", e.Review.Title+" · "+e.Review.Version)
			}
			b.WriteString("\n")
		}
	}
	return b
}

func (m Model) reviewsView() viewPage {
	b := viewPage{}
	state := m.reviewFilter
	if state == "" {
		state = work.Ready
	}
	label := ""
	if m.reviewWork != "" {
		label = " · " + m.workName(m.reviewWork)
	}
	b.WriteString(pal.bold.Render(" YOUR QUEUE") + pal.dim.Render(fmt.Sprintf("  %s%s · %d items", state, label, len(m.rows()))) + "\n\n")
	for i, r := range m.rows() {
		check := "[ ]"
		if r.review.Action == "merge" {
			check = " → "
		}
		if r.review.State == work.Reviewed || r.review.State == work.Completed {
			check = "[x]"
		}
		b.WriteString(m.mark(i) + check + " " + pal.yellow.Render(pad(r.review.Kind, 10)) + " " + r.review.Title + "\n")
		if i == m.cursor {
			m.field(&b, "ask", r.review.Question)
			m.field(&b, "rec", r.review.Recommendation)
			m.field(&b, "changed", r.review.Changed)
			m.field(&b, "requester", r.review.Requester)
			m.field(&b, "author", r.review.Author)
			m.field(&b, "source", r.review.URL)
			m.field(&b, "status", r.review.StatusError)
			if r.review.Provisional {
				b.WriteString(pal.yellow.Render("             inferred request; ownership needs confirmation") + "\n")
			}
		}
	}
	if len(m.rows()) == 0 {
		if state == "pending" {
			b.WriteString(pal.dim.Render(" All caught up. Tab shows other queues, including reviewed items.") + "\n")
		} else {
			b.WriteString(pal.dim.Render(" No requests in this queue.") + "\n")
		}
	}
	return b
}

func (m Model) reviewView() viewPage {
	w, ok := m.workScope()
	if !ok {
		return textPage(" work unavailable", "")
	}
	r, ok := w.Reviews[m.reviewID]
	if !ok {
		return textPage(" review unavailable", "")
	}
	b := viewPage{}
	b.WriteString(pal.bold.Render(" "+r.Title) + pal.dim.Render(" · "+r.QueueState()+" · "+w.Name) + "\n\n")
	m.field(&b, "checklist", m.reviewPosition())
	m.field(&b, "status", r.StatusError)
	m.field(&b, "ask", r.Question)
	m.field(&b, "rec", r.Recommendation)
	m.field(&b, "changed", r.Changed)
	m.field(&b, "feedback", r.Feedback)
	m.field(&b, "judgment", r.Judgment)
	m.field(&b, "follow-through", r.FollowThrough)
	m.field(&b, "followed by", r.FollowedBy)
	m.field(&b, "requester", r.Requester)
	m.field(&b, "author", r.Author)
	m.field(&b, "prepared by", r.Preparer)
	m.field(&b, "next owner", r.NextOwner(w.Responsible()))
	m.field(&b, "source", r.URL)
	m.field(&b, "version", r.Version)
	m.field(&b, "reviewed", r.ReviewedVersion)
	b.WriteString("\n EVIDENCE\n")
	for _, line := range strings.Split(ansi.Wrap(r.Evidence, max(20, m.width-4), " "), "\n") {
		b.WriteString("   " + line + "\n")
	}
	if r.Action == "merge" {
		b.WriteString("\n o/V opens the PR. x dismisses; u restores. GitHub merge/close status completes this item.\n")
	} else {
		b.WriteString("\n o/V opens the source. x marks reviewed and advances. n/p skips between requests.\n")
	}
	return b
}

func commandWords(text string) ([]string, error) {
	var out []string
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	for _, r := range text {
		if escaped {
			word.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if r == ' ' || r == '\t' {
			if started {
				out = append(out, word.String())
				word.Reset()
				started = false
			}
			continue
		}
		word.WriteRune(r)
		started = true
	}
	if quote != 0 || escaped {
		return nil, fmt.Errorf("unfinished quote or escape")
	}
	if started {
		out = append(out, word.String())
	}
	return out, nil
}
