package work

import "time"

type Scope struct {
	Observations    map[string]ExecutionObservation
	Adoptions       map[string]int64
	PendingInputs   int
	Kind            string
	Owner           string
	Parent          string
	Next            string
	WaitFor         string
	Result          string
	VerifiedBy      string
	VerifiedContext int64
	ResultContext   int64
	Priority        int
	PriorityBy      string
	ID              string
	Origin          string
	Host            string
	Repo            string
	Name            string
	State           string
	Purpose         string
	Authority       string
	Manager         string
	Revision        int64
	Context         Context
	Brief           Brief
	Bindings        map[string]int64
	Executions      map[string]Execution
	Reviews         map[string]Review
	Sources         []Source
	Related         map[string]string
	Trigger         string
	WakeAt          time.Time
	Updated         time.Time
	Changes         int64
	Pending         int
	CoverageErrors  []string
	Activity        []Event
}

func (s Scope) InFlight() bool {
	if s.State != "archived" && s.State != "completed" {
		return true
	}
	for _, r := range s.Reviews {
		if r.Closed || r.Dismissed {
			continue
		}
		if r.State == "incoming" || r.State == Ready || r.State == Preparing || r.State == WithAuthor || (r.State == Completed && r.Judgment != "" && r.FollowThrough == "") {
			return true
		}
	}
	return false
}

func (s Scope) BriefCurrent() bool {
	if s.Brief.Assessment == "" || s.Brief.Context != s.Context.Revision || s.PendingInputs > 0 {
		return false
	}
	for key, version := range s.Brief.Evidence {
		if r, ok := s.Reviews[key]; !ok || r.Version != version {
			return false
		}
	}
	return true
}

func (s Scope) Ready() int {
	n := 0
	for _, r := range s.Reviews {
		if r.Closed || r.Dismissed {
			continue
		}
		if r.State == Ready {
			n++
		}
	}
	return n
}

func (s Scope) Responsible() string {
	if s.Owner != "" {
		return s.Owner
	}
	return s.Manager
}

func (s Scope) PreparationState() string {
	if s.Responsible() == "" {
		return "incoming"
	}
	return Preparing
}
