package core

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Worker is one agent working for a goal: a session its manager started, or a subagent running inside the manager's session or
// one of those sessions.
type Worker struct {
	// Session is the key of the session the worker is, or runs inside when Member is set.
	Session string
	Member  *Member
	Name    string
	// State is busy, waiting, idle, hung, adrift or done.
	State string
	// Activity is the latest thing the worker did or said, At when.
	Activity string
	At       time.Time
	Branch   string
	Ahead    int
	PRs      []PR
}

// Active reports whether the worker has not finished.
func (w Worker) Active() bool { return w.State != "done" }

// workers are every agent working for the node's goal, sessions first, then subagents, active before finished, newest first.
func (n Node) workers(now, woke time.Time) []Worker {
	var out []Worker
	add := func(s Live, own bool) {
		if !own {
			w := Worker{Session: s.Key(), Name: s.Title, State: liveState(s, now, woke), Branch: s.Branch, Ahead: s.Ahead, PRs: s.PRs}
			if w.Name == "" {
				w.Name = Short(s.Key())
			}
			w.Activity, w.At = latestActivity(s.Facts, s.Since)
			out = append(out, w)
		}
		for i := range s.Crew {
			m := s.Crew[i]
			w := Worker{Session: s.Key(), Member: &m, Name: memberName(m), State: memberState(m), Branch: m.Branch, Ahead: m.Ahead, PRs: m.PRs}
			w.Activity, w.At = latestActivity(m.Facts, m.Last)
			out = append(out, w)
		}
	}
	if n.Live != nil {
		add(*n.Live, true)
	}
	for _, a := range n.Agents {
		add(a, false)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Member == nil) != (out[j].Member == nil) {
			return out[i].Member == nil
		}
		if out[i].Active() != out[j].Active() {
			return out[i].Active()
		}
		return out[i].At.After(out[j].At)
	})
	return out
}

// Working counts the node's workers that have not finished, and those that have.
func (n Node) Working() (active, done int) {
	for _, w := range n.Workers {
		if w.Active() {
			active++
		} else {
			done++
		}
	}
	return active, done
}

func liveState(s Live, now, woke time.Time) string {
	switch {
	case s.Status == Busy && s.Hung(now, woke):
		return "hung"
	case len(s.Drift) > 0:
		return "adrift"
	case s.Status == Moved || s.Status == Unknown:
		return "idle"
	}
	return s.Status.String()
}

func memberState(m Member) string {
	switch {
	case m.Cut:
		return "done"
	case len(m.Drift) > 0 && m.Status == Busy:
		return "adrift"
	case m.Status == Busy || m.Status == Waiting:
		return m.Status.String()
	}
	return "done"
}

func memberName(m Member) string {
	for _, s := range []string{m.Description, m.Name, m.Type, m.ID} {
		if s != "" {
			return s
		}
	}
	return "subagent"
}

// latestActivity is the latest of what the agent asked, said or ran, falling back to the task it started from at since.
func latestActivity(f Facts, since time.Time) (string, time.Time) {
	text, at := "", time.Time{}
	if f.Doing.Tool != "" {
		text, at = f.Doing.String(), f.Doing.At
	}
	if f.Result != "" && !f.ResultAt.Before(at) {
		text, at = "said: "+f.Result, f.ResultAt
	}
	if f.Asked != "" {
		text = "asks: " + f.Asked
	}
	if text == "" && f.Task != "" {
		text, at = "started: "+tagless.ReplaceAllString(f.Task, " "), since
	}
	return strings.Join(strings.Fields(text), " "), at
}

var tagless = regexp.MustCompile(`</?[a-zA-Z][\w-]*(\s[^>]*)?>`)
