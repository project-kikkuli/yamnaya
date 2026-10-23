package core

import (
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
}

func (s *batchSource) PRStatuses(urls []string) (map[string]PRStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches++
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
