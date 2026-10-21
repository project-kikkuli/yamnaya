package work

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrConflict = errors.New("work changed; refresh before applying this judgment")

type Event struct {
	ID              string
	Work            string
	Expected        int64
	Type            string
	Actor           string
	Text            string
	Target          string
	Context         int64
	PreviousVersion string
	Review          Review
	Brief           Brief
	Source          Source
	At              time.Time
	WakeAt          time.Time
	Execution       *Execution
	Priority        int
	Observation     ExecutionObservation
}

func Apply(s *Scope, e Event) error {
	if s.Bindings == nil {
		s.Bindings = map[string]int64{}
	}
	if s.Reviews == nil {
		s.Reviews = map[string]Review{}
	}
	switch e.Type {
	case "execution":
		if _, bound := s.Bindings[e.Target]; !bound {
			return errors.New("execution must belong to a bound native session")
		}
		if s.Observations == nil {
			s.Observations = map[string]ExecutionObservation{}
		}
		s.Observations[e.Target] = e.Observation
	case "kind":
		if e.Text != "chief" && e.Text != "agent" && e.Text != "intake" && e.Text != "project" {
			return errors.New("unknown work kind")
		}
		s.Kind = e.Text
	case "owner":
		if e.Target == "" {
			return errors.New("accountable agent is required")
		}
		s.Owner = e.Target
		for id, r := range s.Reviews {
			if r.State == "incoming" {
				r.State = s.PreparationState()
				s.Reviews[id] = r
			}
		}
	case "parent":
		if e.Target == s.ID {
			return errors.New("work cannot delegate to itself")
		}
		s.Parent = e.Target
	case "plan":
		if strings.TrimSpace(e.Text) == "" {
			return errors.New("plan needs a next action or wait condition")
		}
		s.Next, s.WaitFor, s.WakeAt = e.Text, e.Target, e.WakeAt
	case "priority":
		s.Priority, s.PriorityBy = e.Priority, e.Actor
	case "result":
		if strings.TrimSpace(e.Text) == "" || e.Context != s.Context.Revision {
			return errors.New("result needs evidence against current context")
		}
		s.Result, s.ResultContext, s.State = e.Text, e.Context, "verifying"
		s.VerifiedBy = ""
	case "verify":
		if s.Result == "" || s.ResultContext != s.Context.Revision || e.Context != s.Context.Revision {
			return errors.New("current result evidence is required")
		}
		if e.Actor == s.Owner || e.Actor == "" {
			return errors.New("completion requires the delegator or owner to verify")
		}
		if strings.TrimSpace(e.Text) == "" {
			return errors.New("verification needs evidence")
		}
		s.VerifiedBy, s.VerifiedContext, s.State = e.Actor, e.Context, "completed"
	case "reopen":
		s.State = "active"
		s.VerifiedBy = ""
		s.Next = e.Text
	case "context":
		if s.State == "completed" || s.State == "verifying" {
			s.State = "active"
			s.VerifiedBy = ""
		}
		if strings.TrimSpace(e.Text) == "" {
			return errors.New("context must not be empty")
		}
		s.Context = Context{Revision: s.Context.Revision + 1, Text: e.Text, Actor: e.Actor}
		for id, r := range s.Reviews {
			if r.Action != "merge" && (r.State == Ready || r.State == Reviewed) {
				r.State = s.PreparationState()
				s.Reviews[id] = r
			}
		}
	case "adopt":
		if e.Context != s.Context.Revision {
			return errors.New("cannot adopt an obsolete context revision")
		}
		if _, ok := s.Bindings[e.Actor]; !ok {
			return errors.New("session is not bound to this work")
		}
		s.Bindings[e.Actor] = e.Context
	case "bind":
		if e.Target == "" {
			return errors.New("session is required")
		}
		if _, ok := s.Bindings[e.Target]; !ok {
			s.Bindings[e.Target] = 0
		}
		if e.Execution != nil {
			if s.Executions == nil {
				s.Executions = map[string]Execution{}
			}
			s.Executions[e.Target] = *e.Execution
		}
	case "manager":
		s.Manager = e.Target
		for id, r := range s.Reviews {
			if r.State == Preparing || r.State == "incoming" {
				r.State = s.PreparationState()
				s.Reviews[id] = r
			}
		}
		if e.Target != "" {
			if _, ok := s.Bindings[e.Target]; !ok {
				s.Bindings[e.Target] = 0
			}
		}
	case "purpose":
		s.Purpose = e.Text
		if e.Actor == "owner" {
			s.Context.Revision++
			if s.State == "completed" || s.State == "verifying" {
				s.State = "active"
				s.VerifiedBy = ""
			}
			for id, r := range s.Reviews {
				if r.Action != "merge" && (r.State == Ready || r.State == Reviewed) {
					r.State = s.PreparationState()
					s.Reviews[id] = r
				}
			}
		}
	case "authority":
		s.Authority = e.Text
		s.Context.Revision++
		if s.State == "completed" || s.State == "verifying" {
			s.State = "active"
			s.VerifiedBy = ""
		}
		for id, r := range s.Reviews {
			if r.Action != "merge" && (r.State == Ready || r.State == Reviewed) {
				r.State = s.PreparationState()
				s.Reviews[id] = r
			}
		}
	case "relate":
		if e.Target == "" || e.Target == s.ID || strings.TrimSpace(e.Text) == "" {
			return errors.New("relationship needs another work ID and its meaning")
		}
		if s.Related == nil {
			s.Related = map[string]string{}
		}
		s.Related[e.Target] = e.Text
	case "unrelate":
		delete(s.Related, e.Target)
	case "close":
		s.State = "archived"
		s.WakeAt = time.Time{}
	case "resume":
		s.State = "active"
	case "brief":
		if e.Brief.Assessment == "" {
			return errors.New("brief needs an assessment")
		}
		if e.Brief.Context != s.Context.Revision {
			return errors.New("brief uses obsolete project context")
		}
		for id, version := range e.Brief.Evidence {
			if r, ok := s.Reviews[id]; !ok || r.Version != version {
				return errors.New("brief evidence has changed")
			}
		}
		s.Brief = e.Brief

	case "handoff":
		incoming := e.Review
		if incoming.ID == "" || incoming.Version == "" || incoming.Handoff == nil || e.Source.Requester == "" {
			return errors.New("handoff needs its source version, requester and transcript provenance")
		}
		old, exists := s.Reviews[incoming.ID]
		if exists {
			old.Handoff = incoming.Handoff
			if old.Requester == "" {
				old.Requester = incoming.Requester
				old.Question = incoming.Question
			}
			old.Provisional = false
			s.Reviews[old.ID] = old
		} else {
			incoming.State = s.PreparationState()
			if incoming.Closed {
				incoming.State = "withdrawn"
			}
			incoming.Updated = e.At
			s.Reviews[incoming.ID] = incoming
		}
		found := false
		for i, source := range s.Sources {
			if source.URL == e.Source.URL {
				found = true
				s.Sources[i].AgentPR = source.AgentPR || e.Source.AgentPR
				if source.Requester == "" {
					s.Sources[i].Requester = e.Source.Requester
					s.Sources[i].Question = e.Source.Question
				}
			}
		}
		if !found && e.Source.Kind != "artifact" {
			s.Sources = append(s.Sources, e.Source)
		}
	case "link-pr":
		found := false
		for i, source := range s.Sources {
			if source.URL == e.Target && source.Kind == "github" {
				s.Sources[i].AgentPR, found = true, true
			}
		}
		if !found {
			return errors.New("agent PR link requires a registered GitHub source")
		}
	case "merge-status":
		r := e.Review
		if r.Action != "merge" || r.URL == "" || r.ID != MergeReviewID(r.URL) || r.Version == "" {
			return errors.New("merge status requires its PR identity and version")
		}
		old := s.Reviews[r.ID]
		if old.Version == r.Version {
			return nil
		}
		if r.StatusError != "" && old.State == Ready && !old.Closed {
			r.State, r.Evidence = Ready, old.Evidence
		}
		r.Dismissed = old.Dismissed
		r.Updated = e.At
		s.Reviews[r.ID] = r
	case "dismiss-merge", "restore-merge":
		r, ok := s.Reviews[e.Target]
		if e.Actor != "owner" || !ok || r.Action != "merge" || r.Closed {
			return errors.New("only the owner can dismiss or restore an open merge item")
		}
		r.Dismissed, r.Updated = e.Type == "dismiss-merge", e.At
		s.Reviews[e.Target] = r
	case "observe":
		r := e.Review
		if r.ID == "" || r.Version == "" {
			return errors.New("source identity and version are required")
		}
		old, exists := s.Reviews[r.ID]
		if exists {
			if r.Order < old.Order || r.Version == old.Version {
				return nil
			}
			if r.Order == old.Order && e.PreviousVersion != old.Version {
				return errors.New("source revision order is ambiguous")
			}
			r.Handoff = old.Handoff
			r.Feedback, r.Judgment = old.Feedback, old.Judgment
			r.ReviewedVersion, r.ReviewedContext = old.ReviewedVersion, old.ReviewedContext
			if r.Question == "" {
				r.Question = old.Question
			}
			if r.Requester == "" {
				r.Requester = old.Requester
			}
		}
		r.State, r.PreparedVersion = s.PreparationState(), ""
		r.Updated = e.At
		if r.Closed {
			r.State = "withdrawn"
			if old.State == Completed || old.State == Reviewed {
				r.State = old.State
			}
			r.Changed = "Source closed; no outstanding review request"
		}
		s.Reviews[r.ID] = r
	case "prepare":
		r, ok := s.Reviews[e.Target]
		if !ok {
			return errors.New("review not found")
		}
		if r.Action == "merge" {
			return errors.New("merge outcomes are tracked from GitHub")
		}
		if r.Version != e.Review.Version || e.Context != s.Context.Revision {
			return errors.New("review evidence or context changed")
		}
		if r.State == Completed || r.State == Reviewed {
			return errors.New("completed review needs a new source revision")
		}
		if r.Closed {
			return errors.New("source is closed; no active review request")
		}
		if e.Review.Recommendation == "" || e.Review.Evidence == "" || e.Review.Question == "" {
			return errors.New("prepare needs the question, recommendation, and evidence")
		}
		r.Question, r.Recommendation, r.Evidence, r.Changed = e.Review.Question, e.Review.Recommendation, e.Review.Evidence, e.Review.Changed
		r.State, r.PreparedVersion, r.PreparedContext = Ready, r.Version, s.Context.Revision
		r.Preparation++
		r.Preparer = e.Actor
		r.Updated = e.At
		s.Reviews[e.Target] = r
	case "reviewed", "unreview":
		if e.Actor != "owner" {
			return errors.New("review checklist actions belong to the owner")
		}
		r, ok := s.Reviews[e.Target]
		if !ok || r.Version == "" || r.Version != e.Review.Version || e.Context != s.Context.Revision {
			return errors.New("review source or context changed; refresh before updating the checklist")
		}
		if r.Action == "merge" {
			return errors.New("merge outcomes are tracked from GitHub")
		}
		if e.Type == "reviewed" {
			if !r.Pending() {
				return errors.New("review is not awaiting owner review")
			}
			r.ReviewedVersion, r.ReviewedContext, r.State = r.Version, s.Context.Revision, Reviewed
		} else {
			if r.State != Reviewed || r.Closed {
				return errors.New("only an open reviewed item can return to the checklist")
			}
			r.ReviewedVersion, r.ReviewedContext, r.State = "", 0, s.PreparationState()
		}
		r.Updated = e.At
		s.Reviews[e.Target] = r
	case "follow-through":
		r, ok := s.Reviews[e.Target]
		if !ok || r.Judgment == "" || r.State != Completed {
			return errors.New("follow-through requires a current owner decision")
		}
		if e.Context != s.Context.Revision || e.Review.Version != r.Version {
			return errors.New("follow-through evidence or context changed")
		}
		if e.Actor != "owner" && e.Actor != s.Responsible() {
			return errors.New("follow-through belongs to the accountable manager")
		}
		if strings.TrimSpace(e.Text) == "" {
			return errors.New("follow-through requires result evidence")
		}
		r.FollowThrough, r.FollowedBy = e.Text, e.Actor
		s.Reviews[e.Target] = r
	case "feedback", "decide":
		if e.Actor != "owner" {
			return errors.New("this judgment is reserved for the owner")
		}
		r, ok := s.Reviews[e.Target]
		if !ok {
			return errors.New("review not found")
		}
		if r.Action == "merge" {
			return errors.New("merge outcomes are tracked from GitHub")
		}
		if r.State != Ready || r.PreparedVersion != r.Version || r.PreparedContext != s.Context.Revision {
			return errors.New("review needs preparation against current source and context")
		}
		if strings.TrimSpace(e.Text) == "" {
			return errors.New("judgment must not be empty")
		}
		if e.Type == "feedback" {
			r.State, r.Feedback = WithAuthor, e.Text
		} else {
			r.State, r.Judgment = Completed, e.Text
			r.FollowThrough, r.FollowedBy = "", ""
		}
		r.Updated = e.At
		s.Reviews[e.Target] = r
	case "withdraw-request":
		for i, source := range s.Sources {
			if source.URL != e.Target || !source.AutomaticRequest() {
				continue
			}
			r, exists := s.Reviews[e.Target]
			if exists && (r.Feedback != "" || r.Judgment != "" || r.ReviewedVersion != "") {
				source.Origin, source.RequestVersion = "", ""
				source.Coverage = "Retained after owner review; automatic assignment no longer active"
				s.Sources[i] = source
			} else if source.LinkedPR() {
				source.Origin = "session-pr"
				source.Requester, source.Question, source.RequestVersion = "", "", ""
				source.Coverage = "linked agent PR; human review request not yet confirmed"
				s.Sources[i] = source
				if exists {
					r.Requester, r.Question, r.PreparedVersion = "", "", ""
					r.Provisional, r.State = true, s.PreparationState()
				}
			} else {
				s.Sources = append(s.Sources[:i], s.Sources[i+1:]...)
				if exists {
					r.State = "withdrawn"
				}
			}
			if exists {
				r.Changed, r.Updated = e.Text, e.At
				s.Reviews[e.Target] = r
			}
			break
		}
	case "watch":
		if e.Source.URL == "" {
			return errors.New("source address is required")
		}
		found := false
		for i, source := range s.Sources {
			if source.URL == e.Source.URL {
				e.Source.AgentPR = e.Source.AgentPR || source.LinkedPR()
				s.Sources[i] = e.Source
				found = true
			}
		}
		if !found {
			s.Sources = append(s.Sources, e.Source)
		}
		if r, ok := s.Reviews[e.Source.URL]; ok && e.Source.Requester != "" {
			if r.State == Ready && (r.Requester != e.Source.Requester || (e.Source.Question != "" && r.Question != e.Source.Question)) {
				r.State, r.PreparedVersion = Preparing, ""
			}
			r.Requester = e.Source.Requester
			r.Provisional = false
			if e.Source.Question != "" {
				r.Question = e.Source.Question
			}
			if r.State == Completed || r.State == "withdrawn" {
				r.State = s.PreparationState()
			}
			r.Closed = false
			r.Updated = e.At
			s.Reviews[r.ID] = r
		}
	case "trigger":
		if e.Text == "" || e.WakeAt.IsZero() {
			return errors.New("trigger needs a reason and wake time")
		}
		s.Trigger, s.WakeAt = e.Text, e.WakeAt
	case "wake":
		if s.WakeAt.IsZero() || e.At.Before(s.WakeAt) {
			return errors.New("trigger is not due")
		}
		s.WakeAt = time.Time{}
	case "message", "receipt", "seen":
	default:
		return fmt.Errorf("unknown work event %q", e.Type)
	}
	s.Revision++
	s.Updated = e.At
	return nil
}
