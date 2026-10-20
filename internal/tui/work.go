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
			if review.State == state {
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
	if p, ok := m.board.Project(m.detail); ok && p.Work != nil {
		return *p.Work, true
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
	key := k.String()
	if key == "enter" {
		if r, ok := m.selected(); ok && r.review != nil {
			m.push(modeReview)
			m.workID, m.reviewID, m.scrollY = r.work.ID, r.review.ID, 0
			return m, nil, true
		}
	}
	if key == ":" {
		m.startInput(inCommand, "", "", "", false)
		return m, nil, true
	}
	if key == "R" {
		m.push(modeReviews)
		m.reviewFilter = ""
		return m, nil, true
	}
	if key == "B" {
		if w, ok := m.selectedWork(); ok {
			m.openWork(w)
		} else {
			m.message = "No continuing work is linked yet; background discovery will attach it."
		}
		return m, nil, true
	}
	if key == "C" {
		w, ok := m.selectedWork()
		if !ok {
			return m, nil, true
		}
		return m, m.editContext(w), true
	}
	if key == "V" {
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
			path, _ := core.DefaultStorePath()
			raw, err := os.ReadFile(filepath.Join(filepath.Dir(path), "work-service.json"))
			var service struct{ Address string }
			if err == nil {
				err = json.Unmarshal(raw, &service)
			}
			if err != nil || service.Address == "" {
				m.message = "Brief service is unavailable; run ym serve."
				return m, nil, true
			}
			address = service.Address + "/brief/" + w.ID
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
			states := []string{"", work.Preparing, work.WithAuthor, work.Completed}
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
				m.push(modeReview)
				m.workID, m.reviewID, m.scrollY = r.work.ID, r.review.ID, 0
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
				m.push(modeReview)
				m.reviewID, m.scrollY = r.review.ID, 0
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
		case "pgup":
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
	if m.inKind != inWorkFeedback && m.inKind != inWorkDecision && m.inKind != inWorkMessage {
		return m, nil, false
	}
	if text == "" {
		return m, nil, true
	}
	kind := "decide"
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
		}
	}
	return m, m.do(func() (string, error) {
		_, err := m.core.ChangeWork(w, work.Event{ID: uuid.NewString(), Type: kind, Actor: "owner", Target: target, Text: text})
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
	b.WriteString(pal.bold.Render(" "+w.Name) + pal.dim.Render("  "+core.Short(w.ID)+" · manager "+w.Manager) + "\n")
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
		if !w.BriefCurrent() {
			b.WriteString(pal.yellow.Render(" Assessment needs refreshing against current context or evidence.") + "\n\n")
		}
		m.field(&b, "assessment", w.Brief.Assessment)
		m.field(&b, "changed", w.Brief.Changed)
		m.field(&b, "purpose", w.Purpose)
		m.field(&b, "approach", w.Brief.Rationale)
		m.field(&b, "uncertain", w.Brief.Uncertainty)
		m.field(&b, "next", w.Brief.Next)
		m.field(&b, "authority", w.Authority)
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
	case 1:
		for i, r := range m.rows() {
			switch {
			case r.target != "":
				b.WriteString(m.mark(i) + pad("remote", 10) + " " + r.target + pal.dim.Render("  "+r.work.Host+" · s message · o native session") + "\n")
			case r.review != nil:
				b.WriteString(m.mark(i) + pal.yellow.Render(pad(r.review.Kind, 10)) + " " + pad(r.review.State, 12) + " " + r.review.Title + "\n")
			case r.session != nil:
				b.WriteString(m.mark(i) + pad(r.session.Status.String(), 10) + " " + titleOf(*r.session) + pal.dim.Render("  "+r.session.Location()) + "\n")
			}
		}
	case 2:
		b.WriteString(fmt.Sprintf(" CONTEXT r%d\n\n", w.Context.Revision))
		m.field(&b, "guidance", w.Context.Text)
		b.WriteString("\n INCORPORATED BY\n")
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
	b.WriteString(pal.bold.Render(" REVIEW OBLIGATIONS") + pal.dim.Render("  "+state+" · tab changes next owner") + "\n\n")
	for i, r := range m.rows() {
		b.WriteString(m.mark(i) + pal.yellow.Render(pad(r.review.Kind, 10)) + " " + pad(r.work.Name, min(30, max(12, m.width/4))) + " " + r.review.Title + "\n")
		if i == m.cursor {
			m.field(&b, "ask", r.review.Question)
			m.field(&b, "rec", r.review.Recommendation)
			m.field(&b, "changed", r.review.Changed)
			m.field(&b, "requester", r.review.Requester)
			if r.review.Provisional {
				b.WriteString(pal.yellow.Render("             inferred request; ownership needs confirmation") + "\n")
			}
		}
	}
	if len(m.rows()) == 0 {
		b.WriteString(pal.dim.Render(" No obligations in this state. Reading never completes a review.") + "\n")
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
	b.WriteString(pal.bold.Render(" "+r.Title) + pal.dim.Render(" · "+r.State+" · "+w.Name) + "\n\n")
	m.field(&b, "ask", r.Question)
	m.field(&b, "rec", r.Recommendation)
	m.field(&b, "changed", r.Changed)
	m.field(&b, "feedback", r.Feedback)
	m.field(&b, "judgment", r.Judgment)
	m.field(&b, "requester", r.Requester)
	m.field(&b, "author", r.Author)
	m.field(&b, "prepared by", r.Preparer)
	m.field(&b, "next owner", r.NextOwner(w.Manager))
	m.field(&b, "source", r.URL)
	m.field(&b, "version", r.Version)
	b.WriteString("\n EVIDENCE\n")
	for _, line := range strings.Split(ansi.Wrap(r.Evidence, max(20, m.width-4), " "), "\n") {
		b.WriteString("   " + line + "\n")
	}
	b.WriteString("\n Reading preserves the obligation. Feedback transfers the next action.\n")
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
