package core

import (
	"fmt"
	"sync"
	"time"
)

// PRStatus is a pull request's state and CI rollup as last fetched.
type PRStatus struct {
	Merge   PRMergeFacts
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

// PRGone is the state of a pull request link into a repository GitHub does not have, such as one from a test fixture.
const PRGone = "GONE"

// PRNotFound is the state of a pull request GitHub answers 404 for, as when its repository was deleted; it is read again
// only every PRNotFoundRecheck, in case it comes back.
const PRNotFound = "NOT_FOUND"

const PRNotFoundRecheck = time.Hour

// Closed reports whether the pull request can no longer change.
func (s PRStatus) Closed() bool {
	return s.State == "MERGED" || s.State == "CLOSED" || s.State == PRGone
}

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
	if s.State == PRNotFound {
		return "pull request not found on GitHub"
	}
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

// PRBatch is a PRSource that reads many pull requests in one request; one it could not read is missing from the result.
type PRBatch interface {
	PRStatuses(urls []string) (map[string]PRStatus, error)
}

// PRBatchSize is how many pull requests RefreshPRs asks a PRBatch for at once.
const PRBatchSize = 50

// PRStale is how long a fetched status of an open pull request stays good while GitHub's budget is plentiful.
const PRStale = 2 * time.Minute

// PRBudget is a PRSource that knows how much of the account's GitHub budget is left: a fraction of the hourly limit, and when
// it resets. ok is false before the source has seen the budget.
type PRBudget interface {
	Budget() (left float64, reset time.Time, ok bool)
}

// How reads slow as the account's budget falls, so the owner's other tools always keep their share: at full pace down to half
// the budget left, then stretching smoothly to PRSlowest at a tenth, where reads stop until the budget resets. Pull requests
// no active goal tracks drop to PRSlowest at a quarter, so managers keep their CI and review events longest.
const (
	budgetFull  = 0.5
	budgetBoard = 0.25
	budgetFloor = 0.1
	PRSlowest   = 30 * time.Minute
)

// prStale is how long a status stays good with left of the budget, for a pull request an active goal tracks or not; 0 reads none.
func prStale(left float64, goal bool) time.Duration {
	switch {
	case left >= budgetFull:
		return PRStale
	case left < budgetFloor:
		return 0
	case !goal && left < budgetBoard:
		return PRSlowest
	}
	slow := (budgetFull - left) / (budgetFull - budgetFloor)
	return PRStale + time.Duration(slow*float64(PRSlowest-PRStale))
}

// RefreshPRs fetches every pull request on the board whose status is missing or stale, active goals' first: in batches from a
// PRBatch, then one at a time, a few at once, for any a batch answered without. A batch that fails ends the cycle's reads, so a
// GitHub outage or an exhausted budget never turns into one request per pull request; they stay due for the next cycle.
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
	pace := PRPace{Left: 1, At: now}
	if bud, ok := c.PRs.(PRBudget); ok {
		if f, reset, ok := bud.Budget(); ok && now.Before(reset) {
			pace.Left, pace.Reset = f, reset
		}
	}
	left := pace.Left
	goal := map[string]bool{}
	tracked, err := c.Store.GoalPRs()
	if err != nil {
		return 0, err
	}
	for id, urls := range tracked {
		if g, ok := b.Org.Goals[id]; ok && b.Org.Active(b.Org.Managers[g.ManagerID]) {
			for _, url := range urls {
				goal[url] = true
			}
		}
	}
	var candidates []string
	seen := map[string]bool{}
	for _, s := range b.Sessions {
		_, managed := b.Org.Managed(s.Key(), b.parents)
		for _, pr := range s.AllPRs() {
			if managed {
				goal[pr.URL] = true
			}
			if !seen[pr.URL] {
				seen[pr.URL] = true
				candidates = append(candidates, pr.URL)
			}
		}
	}
	for _, urls := range tracked {
		for _, url := range urls {
			if _, ok := known[url]; ok && goal[url] && !seen[url] {
				seen[url] = true
				candidates = append(candidates, url)
			}
		}
	}
	var due, later []string
	for _, url := range candidates {
		st, ok := known[url]
		stale := prStale(left, goal[url])
		switch {
		case stale == 0, ok && (st.Closed() || now.Sub(st.Fetched) < stale),
			ok && st.State == PRNotFound && now.Sub(st.Fetched) < PRNotFoundRecheck:
		case goal[url]:
			due = append(due, url)
		default:
			later = append(later, url)
		}
	}
	due = append(due, later...)
	var firstErr error
	n := 0
	if batch, ok := c.PRs.(PRBatch); ok {
		var left []string
		for start := 0; start < len(due); start += PRBatchSize {
			chunk := due[start:min(start+PRBatchSize, len(due))]
			got, err := batch.PRStatuses(chunk)
			if err != nil {
				firstErr, pace.Failed = err, err.Error()
				break
			}
			for _, url := range chunk {
				st, ok := got[url]
				if !ok {
					left = append(left, url)
					continue
				}
				if err := c.Store.SavePRStatus(url, st); err != nil {
					if firstErr == nil {
						firstErr = fmt.Errorf("%s: %w", url, err)
					}
					continue
				}
				n++
			}
		}
		due = left
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 4)
	for _, url := range due {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			st, err := c.PRs.PRStatus(url)
			if err != nil {
				st = known[url]
				st.State, st.Merge.Error, st.Fetched = "UNKNOWN", err.Error(), now
			}
			if st.Fetched.IsZero() {
				st.Fetched = now
			}
			if prev, ok := known[url]; ok && err == nil && st.Merge.Error != "" && st.Merge.OID != "" && st.Merge.OID == prev.Merge.OID {
				st = prev.unreadSince(st)
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
	if err := c.savePace(pace); err != nil && firstErr == nil {
		firstErr = err
	}
	return n, firstErr
}

// unreadSince is prev with what a fetch that could not read the head's checks and threads did read: state, title, facts and the error.
func (prev PRStatus) unreadSince(st PRStatus) PRStatus {
	prev.State, prev.Title, prev.Head, prev.Commits, prev.Fetched = st.State, st.Title, st.Head, st.Commits, st.Fetched
	prev.Merge.Author, prev.Merge.Viewer, prev.Merge.Draft, prev.Merge.Error = st.Merge.Author, st.Merge.Viewer, st.Merge.Draft, st.Merge.Error
	return prev
}

// AllPRs are the session's pull requests and those its crew opened.
func (s Session) AllPRs() []PR {
	out := append([]PR(nil), s.PRs...)
	for _, m := range s.Crew {
		out = append(out, m.PRs...)
	}
	return out
}
