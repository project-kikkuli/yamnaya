package core

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// batchSource reads pull requests in batches and counts every request, as GitHub meters them.
type batchSource struct {
	mu            sync.Mutex
	batches, ones int
	now           func() time.Time
	unreadable    map[string]bool
	down          bool
}

func (s *batchSource) PRStatuses(urls []string) (map[string]PRStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches++
	if s.down {
		return nil, errors.New("GraphQL budget exhausted")
	}
	out := map[string]PRStatus{}
	for _, u := range urls {
		if !s.unreadable[u] {
			out[u] = PRStatus{State: "OPEN", Passed: 1, Fetched: s.now()}
		}
	}
	return out, nil
}

func (s *batchSource) PRStatus(url string) (PRStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ones++
	return PRStatus{State: "OPEN", Fetched: s.now()}, nil
}

// TestOpenPullRequestsStayFreshWithinGitHubsBudget runs four minutes of ym serve's five-second cycle over a board of open pull
// requests and counts the requests it makes: every one stays fresh, in batches, with only what a batch could not read asked alone.
func TestOpenPullRequestsStayFreshWithinGitHubsBudget(t *testing.T) {
	for _, c := range []struct {
		prs, unreadable int
	}{{10, 0}, {1000, 0}, {1000, 3}} {
		t.Run(fmt.Sprintf("%d open, %d unreadable", c.prs, c.unreadable), func(t *testing.T) {
			st, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
			src := &batchSource{now: func() time.Time { return now }, unreadable: map[string]bool{}}
			c2 := &Core{Store: st, PRs: src, Now: func() time.Time { return now }}
			var b Board
			for i := range c.prs {
				url := fmt.Sprintf("https://github.com/acme/app/pull/%d", i+1)
				if i < c.unreadable {
					src.unreadable[url] = true
				}
				b.Sessions = append(b.Sessions, Live{Session: Session{ID: fmt.Sprint(i), Facts: Facts{PRs: []PR{{URL: url}}}}})
			}
			cycles := int(4 * time.Minute / (5 * time.Second))
			for range cycles {
				if _, err := c2.RefreshPRs(b); err != nil {
					t.Fatal(err)
				}
				known, err := st.PRStatuses()
				if err != nil {
					t.Fatal(err)
				}
				for _, s := range b.Sessions {
					if got := known[s.PRs[0].URL]; now.Sub(got.Fetched) >= PRStale {
						t.Fatalf("%s last read %v ago", s.PRs[0].URL, now.Sub(got.Fetched))
					}
				}
				now = now.Add(5 * time.Second)
			}
			// Each pull request is read once per PRStale; a cycle's due ones share batches.
			reads := c.prs * int(4*time.Minute/PRStale)
			if most := cycles + reads/PRBatchSize; src.batches > most {
				t.Errorf("%d batch requests in four minutes, want at most %d", src.batches, most)
			}
			if most := c.unreadable * (int(4*time.Minute/PRStale) + 1); src.ones > most {
				t.Errorf("%d single requests in four minutes, want at most %d", src.ones, most)
			}
		})
	}
}

// TestAFailingBatchNeverFansOutIntoSingleRequests keeps a GitHub outage or an exhausted budget from costing one request per pull
// request: the cycle stops at the first failed batch and every pull request stays due.
func TestAFailingBatchNeverFansOutIntoSingleRequests(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	src := &batchSource{now: func() time.Time { return now }, down: true}
	c := &Core{Store: st, PRs: src, Now: func() time.Time { return now }}
	var b Board
	for i := range 500 {
		b.Sessions = append(b.Sessions, Live{Session: Session{ID: fmt.Sprint(i), Facts: Facts{PRs: []PR{{URL: fmt.Sprintf("https://github.com/acme/app/pull/%d", i+1)}}}}})
	}
	if _, err := c.RefreshPRs(b); err == nil || src.batches != 1 || src.ones != 0 {
		t.Fatalf("err %v, %d batches and %d single requests, want the failure, one batch and none", err, src.batches, src.ones)
	}
}

// budgetSource is a batchSource that reports a fixed share of the account's budget left and counts reads per pull request.
type budgetSource struct {
	batchSource
	left  float64
	reset time.Time
	reads map[string]int
}

func (s *budgetSource) Budget() (float64, time.Time, bool) { return s.left, s.reset, true }

func (s *budgetSource) PRStatuses(urls []string) (map[string]PRStatus, error) {
	for _, u := range urls {
		s.reads[u]++
	}
	return s.batchSource.PRStatuses(urls)
}

// TestReadsSlowAsTheBudgetFallsAndGoalsKeepTheirsLongest runs an hour of ym serve's cycle at a fixed share of the account's
// budget left: full pace above half, slower and slower below it, the board's other pull requests at the slowest pace from a
// quarter, and every read stopping at a tenth.
func TestReadsSlowAsTheBudgetFallsAndGoalsKeepTheirsLongest(t *testing.T) {
	for _, c := range []struct {
		left               float64
		goalMin, goalMax   int
		boardMin, boardMax int
	}{
		{0.9, 29, 31, 29, 31},
		{0.5, 29, 31, 29, 31},
		{0.3, 3, 5, 3, 5},
		{0.2, 2, 4, 2, 3},
		{0.05, 0, 0, 0, 0},
	} {
		t.Run(fmt.Sprintf("%.0f%% left", c.left*100), func(t *testing.T) {
			st, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
			src := &budgetSource{batchSource: batchSource{now: func() time.Time { return now }}, left: c.left, reset: now.Add(2 * time.Hour), reads: map[string]int{}}
			core := &Core{Store: st, PRs: src, Now: func() time.Time { return now }}
			b := Board{Org: Org{Managers: map[string]Manager{"m1": {ID: "m1"}}, Goals: map[string]Goal{}, Sessions: map[string]string{"goal-agent": "m1"}}}
			for i := range 2 {
				var prs []PR
				for j := range 5 {
					prs = append(prs, PR{URL: fmt.Sprintf("https://github.com/acme/app/pull/%d", i*10+j)})
				}
				b.Sessions = append(b.Sessions, Live{Session: Session{ID: []string{"goal-agent", "own"}[i], Facts: Facts{PRs: prs}}})
			}
			for _, pr := range b.Sessions[0].PRs {
				st.SavePRStatus(pr.URL, PRStatus{State: "OPEN", Fetched: now.Add(-time.Hour)})
			}
			for _, pr := range b.Sessions[1].PRs {
				st.SavePRStatus(pr.URL, PRStatus{State: "OPEN", Fetched: now.Add(-time.Hour)})
			}
			for range int(time.Hour / (5 * time.Second)) {
				if _, err := core.RefreshPRs(b); err != nil {
					t.Fatal(err)
				}
				now = now.Add(5 * time.Second)
			}
			for i, want := range [][2]int{{c.goalMin, c.goalMax}, {c.boardMin, c.boardMax}} {
				for _, pr := range b.Sessions[i].PRs {
					if got := src.reads[pr.URL]; got < want[0] || got > want[1] {
						t.Errorf("%s read %d times in the hour, want %d to %d", pr.URL, got, want[0], want[1])
					}
				}
			}
		})
	}
}

func TestPullRequestsTheOwnerWroteStayFreshWithNoSessionOnTheBoard(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	src := &batchSource{now: func() time.Time { return now }, unreadable: map[string]bool{}}
	c := &Core{Store: st, PRs: src, Now: func() time.Time { return now }}
	url := "https://github.com/acme/august/pull/3"
	if err := st.SyncAuthored(map[string]string{url: "Ship it"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := c.RefreshPRs(Board{}); err != nil {
			t.Fatal(err)
		}
		got, _ := st.PRStatuses()
		if got[url].Fetched != now {
			t.Fatalf("status fetched %v, want %v", got[url].Fetched, now)
		}
		now = now.Add(PRStale)
	}
}
