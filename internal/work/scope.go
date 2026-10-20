package work

import "time"

type Scope struct {
	ID             string
	Origin         string
	Host           string
	Repo           string
	Name           string
	State          string
	Purpose        string
	Authority      string
	Manager        string
	Revision       int64
	Context        Context
	Brief          Brief
	Bindings       map[string]int64
	Executions     map[string]Execution
	Reviews        map[string]Review
	Sources        []Source
	Related        map[string]string
	Trigger        string
	WakeAt         time.Time
	Updated        time.Time
	Changes        int64
	Pending        int
	CoverageErrors []string
	Activity       []Event
}

func (s Scope) InFlight() bool {
	if s.State != "archived" {
		return true
	}
	for _, r := range s.Reviews {
		if r.State == Ready || r.State == Preparing || r.State == WithAuthor {
			return true
		}
	}
	return false
}

func (s Scope) BriefCurrent() bool {
	if s.Brief.Assessment == "" || s.Brief.Context != s.Context.Revision {
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
		if r.State == Ready {
			n++
		}
	}
	return n
}
