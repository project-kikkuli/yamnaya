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
}

func Apply(s *Scope, e Event) error {
	if s.Bindings == nil {
		s.Bindings = map[string]int64{}
	}
	if s.Reviews == nil {
		s.Reviews = map[string]Review{}
	}
	switch e.Type {
	case "context":
		if strings.TrimSpace(e.Text) == "" {
			return errors.New("context must not be empty")
		}
		s.Context = Context{Revision: s.Context.Revision + 1, Text: e.Text}
		for id, r := range s.Reviews {
			if r.State == Ready {
				r.State = Preparing
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
		if e.Target != "" {
			if _, ok := s.Bindings[e.Target]; !ok {
				s.Bindings[e.Target] = 0
			}
		}
	case "purpose":
		s.Purpose = e.Text
		if e.Actor == "owner" {
			s.Context.Revision++
			for id, r := range s.Reviews {
				if r.State == Ready {
					r.State = Preparing
					s.Reviews[id] = r
				}
			}
		}
	case "authority":
		s.Authority = e.Text
		s.Context.Revision++
		for id, r := range s.Reviews {
			if r.State == Ready {
				r.State = Preparing
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
			r.Feedback, r.Judgment = old.Feedback, old.Judgment
			if r.Question == "" {
				r.Question = old.Question
			}
			if r.Requester == "" {
				r.Requester = old.Requester
			}
		}
		r.State, r.PreparedVersion = Preparing, ""
		r.Updated = e.At
		if r.Closed {
			r.State = "withdrawn"
			if old.State == Completed {
				r.State = Completed
			}
			r.Changed = "Source closed; no outstanding review request"
		}
		s.Reviews[r.ID] = r
	case "prepare":
		r, ok := s.Reviews[e.Target]
		if !ok {
			return errors.New("review not found")
		}
		if r.Version != e.Review.Version || e.Context != s.Context.Revision {
			return errors.New("review evidence or context changed")
		}
		if r.State == Completed {
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
	case "feedback", "decide":
		if e.Actor != "owner" {
			return errors.New("this judgment is reserved for the owner")
		}
		r, ok := s.Reviews[e.Target]
		if !ok {
			return errors.New("review not found")
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
		}
		r.Updated = e.At
		s.Reviews[e.Target] = r
	case "watch":
		if e.Source.URL == "" {
			return errors.New("source address is required")
		}
		found := false
		for i, source := range s.Sources {
			if source.URL == e.Source.URL {
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
				r.State = Preparing
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
