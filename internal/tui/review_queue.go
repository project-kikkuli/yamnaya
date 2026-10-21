package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

type reviewActionMsg struct {
	scope  work.Scope
	target string
	action string
	index  int
	err    error
}

func pendingReviews(w work.Scope) int {
	n := 0
	for _, r := range w.Reviews {
		if r.Pending() {
			n++
		}
	}
	return n
}

func (m Model) reviewQueueRows() []row {
	state := m.reviewFilter
	if state == "" {
		state = work.Ready
	}
	var out []row
	seen := map[string]bool{}
	for _, p := range m.board.Projects {
		if p.Work == nil || seen[p.Work.ID] || (m.reviewWork != "" && p.Work.ID != m.reviewWork) {
			continue
		}
		seen[p.Work.ID] = true
		for _, r := range sortedReviews(*p.Work) {
			if (state == "pending" && r.Pending()) || r.QueueState() == state {
				out = append(out, row{work: p.Work, review: &r, project: &p})
			}
		}
	}
	return out
}

func (m *Model) openReviewQueue(w work.Scope) {
	m.push(modeReviews)
	m.reviewWork, m.reviewFilter, m.scrollY = w.ID, "pending", 0
}

func (m *Model) openReview(r row) {
	from := m.mode
	m.push(modeReview)
	if from != modeReviews {
		m.reviewWork, m.reviewFilter = r.work.ID, r.review.State
		if r.review.Pending() {
			m.reviewFilter = "pending"
		}
	}
	m.workID, m.reviewID, m.scrollY = r.work.ID, r.review.ID, 0
}

func (m *Model) moveReview(delta int) {
	rows := m.reviewQueueRows()
	for i, r := range rows {
		if r.work.ID == m.workID && r.review.ID == m.reviewID {
			next := max(0, min(len(rows)-1, i+delta))
			m.workID, m.reviewID, m.scrollY = rows[next].work.ID, rows[next].review.ID, 0
			return
		}
	}
}

func (m Model) reviewAction(key string) tea.Cmd {
	w, ok := m.selectedWork()
	if !ok {
		return nil
	}
	var r work.Review
	if m.mode == modeReview {
		r = w.Reviews[m.reviewID]
	} else if selected, ok := m.selected(); ok && selected.review != nil {
		r = *selected.review
	}
	if r.ID == "" || (key == "x" && !r.Pending()) || (key == "u" && r.State != work.Reviewed && !r.Dismissed) {
		return nil
	}
	action := "reviewed"
	if key == "u" {
		action = "unreview"
	}
	if r.Action == "merge" {
		action = "dismiss-merge"
		if key == "u" {
			action = "restore-merge"
		}
	}
	index := 0
	for i, row := range m.reviewQueueRows() {
		if row.work.ID == w.ID && row.review.ID == r.ID {
			index = i
			break
		}
	}
	return func() tea.Msg {
		updated, err := m.core.ChangeWork(w, work.Event{ID: uuid.NewString(), Type: action, Actor: "owner", Target: r.ID})
		return reviewActionMsg{scope: updated, target: r.ID, action: action, index: index, err: err}
	}
}

func (m Model) finishReviewAction(msg reviewActionMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.message = "error: " + msg.err.Error()
		return m, m.refresh(false)
	}
	if msg.scope.Host != "" {
		m.pendingReview = &msg
		m.message = "Checklist update queued for " + msg.scope.Host
		return m, m.refresh(false)
	}
	keep := ""
	if r, ok := m.selected(); ok {
		keep = rowKey(r)
	}
	for i := range m.board.Projects {
		w := m.board.Projects[i].Work
		if w != nil && w.ID == msg.scope.ID && w.Revision <= msg.scope.Revision {
			m.board.Projects[i].Work = &msg.scope
		}
	}
	m.follow(keep)
	m.completeReviewAction(msg)
	return m, m.refresh(false)
}

func (m *Model) completeReviewAction(msg reviewActionMsg) {
	m.message = "Review returned to the checklist"
	if msg.action == "restore-merge" {
		m.message = "Merge item restored"
		if m.mode == modeReview {
			m.reviewFilter = "pending"
		}
		return
	}
	if msg.action != "reviewed" && msg.action != "dismiss-merge" {
		if m.mode == modeReview && m.workID == msg.scope.ID && m.reviewID == msg.target {
			m.reviewFilter = "pending"
		}
		return
	}
	m.message = "Marked reviewed for this source version"
	if msg.action == "dismiss-merge" {
		m.message = "Merge item dismissed; Tab shows dismissed items, u restores"
	}
	if m.mode != modeReview || m.workID != msg.scope.ID || m.reviewID != msg.target {
		return
	}
	w, ok := m.workScope()
	if !ok || (msg.action == "reviewed" && w.Reviews[msg.target].State != work.Reviewed) || (msg.action == "dismiss-merge" && !w.Reviews[msg.target].Dismissed) {
		m.message = "Source changed; the request remains in the checklist"
		return
	}
	rows := m.reviewQueueRows()
	if len(rows) > 0 {
		next := rows[min(msg.index, len(rows)-1)]
		m.workID, m.reviewID, m.scrollY = next.work.ID, next.review.ID, 0
		return
	}
	if len(m.stack) > 0 && m.stack[len(m.stack)-1].mode == modeReviews {
		m.pop()
	} else {
		m.mode = modeReviews
	}
	m.reviewID, m.scrollY = "", 0
	m.message = "Checklist complete"
}

func (m *Model) finishPendingReview() {
	if m.pendingReview == nil {
		return
	}
	msg := *m.pendingReview
	for _, p := range m.board.Projects {
		if p.Work == nil || p.Work.ID != msg.scope.ID {
			continue
		}
		r := p.Work.Reviews[msg.target]
		if (msg.action == "reviewed" && r.State == work.Reviewed && r.ReviewedVersion == msg.scope.Reviews[msg.target].Version) || (msg.action == "unreview" && r.Pending()) || (msg.action == "dismiss-merge" && r.Dismissed) || (msg.action == "restore-merge" && !r.Dismissed) {
			m.pendingReview = nil
			m.completeReviewAction(msg)
		}
		return
	}
}

func (m Model) reviewPosition() string {
	rows := m.reviewQueueRows()
	for i, r := range rows {
		if r.work.ID == m.workID && r.review.ID == m.reviewID {
			return fmt.Sprintf("%d of %d", i+1, len(rows))
		}
	}
	return ""
}

func (m Model) currentReview() (work.Review, bool) {
	if m.mode == modeReview {
		if w, ok := m.workScope(); ok {
			r, found := w.Reviews[m.reviewID]
			return r, found
		}
	}
	if r, ok := m.selected(); ok && r.review != nil {
		return *r.review, true
	}
	return work.Review{}, false
}
