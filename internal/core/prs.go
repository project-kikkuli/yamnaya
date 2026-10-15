package core

import (
	"fmt"
	"sync"
	"time"
)

// PRStatus is a pull request's state and CI rollup as last fetched.
type PRStatus struct {
	State   string
	Title   string
	Review  string
	Passed  int
	Failed  int
	Pending int
	// Head is the pull request's branch; Commits counts its commits.
	Head    string
	Commits int
	// Threads counts review threads; Unresolved those still open.
	Threads    int
	Unresolved int
	Fetched    time.Time
}

// Closed reports whether the pull request can no longer change.
func (s PRStatus) Closed() bool { return s.State == "MERGED" || s.State == "CLOSED" }

// CI is the check rollup in one word: failing, running, passing, or "".
func (s PRStatus) CI() string {
	switch {
	case s.Failed > 0:
		return "failing"
	case s.Pending > 0:
		return "running"
	case s.Passed > 0:
		return "passing"
	}
	return ""
}

func (s PRStatus) String() string {
	out := s.State
	if s.Closed() {
		return out
	}
	if ci := s.CI(); ci != "" {
		out += fmt.Sprintf(" ci %s %d/%d", ci, s.Passed, s.Passed+s.Failed+s.Pending)
	}
	if s.Review != "" {
		out += " " + s.Review
	}
	return out
}

// PRSource fetches a pull request's status, usually over the network.
type PRSource interface {
	PRStatus(url string) (PRStatus, error)
}

// PRStale is how long a fetched status of an open pull request stays good.
const PRStale = 2 * time.Minute

// RefreshPRs fetches every pull request on the board whose status is missing or stale, a few at a time.
// It never runs on the render path; callers start it in the background.
func (c *Core) RefreshPRs(b Board) (int, error) {
	if c.PRs == nil {
		return 0, nil
	}
	known, err := c.Store.PRStatuses()
	if err != nil {
		return 0, err
	}
	now := c.now()
	var due []string
	seen := map[string]bool{}
	for _, s := range b.Sessions {
		for _, pr := range s.AllPRs() {
			if seen[pr.URL] {
				continue
			}
			seen[pr.URL] = true
			st, ok := known[pr.URL]
			if !ok || (!st.Closed() && now.Sub(st.Fetched) >= PRStale) {
				due = append(due, pr.URL)
			}
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	n := 0
	sem := make(chan struct{}, 4)
	for _, url := range due {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			st, err := c.PRs.PRStatus(url)
			if err != nil {
				st = PRStatus{State: "UNKNOWN"}
			}
			if st.Fetched.IsZero() {
				st.Fetched = now
			}
			if serr := c.Store.SavePRStatus(url, st); err == nil {
				err = serr
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", url, err)
			}
			if err == nil {
				n++
			}
		}()
	}
	wg.Wait()
	return n, firstErr
}

// AllPRs are the session's pull requests and those its crew opened.
func (s Session) AllPRs() []PR {
	out := append([]PR(nil), s.PRs...)
	for _, m := range s.Crew {
		out = append(out, m.PRs...)
	}
	return out
}
