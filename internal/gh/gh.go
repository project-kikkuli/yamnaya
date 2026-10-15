// Package gh fetches pull request status with the gh CLI.
package gh

import (
	"encoding/json"
	"errors"
	"os/exec"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// Source asks GitHub for a pull request's state, checks and review threads in one GraphQL query.
type Source struct {
	Run func(url string) ([]byte, error)
	Now func() time.Time
}

func New() *Source { return &Source{Run: query, Now: time.Now} }

const prQuery = `query($url: URI!) { resource(url: $url) { ... on PullRequest {
  state title reviewDecision headRefName
  commits { totalCount }
  reviewThreads(first: 100) { totalCount nodes { isResolved } }
  last: commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 100) { nodes {
    ... on CheckRun { status conclusion } ... on StatusContext { state } } } } } } }
} } }`

func query(url string) ([]byte, error) {
	return exec.Command("gh", "api", "graphql", "-f", "url="+url, "-f", "query="+prQuery).Output()
}

type check struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

type prJSON struct {
	State          string `json:"state"`
	Title          string `json:"title"`
	ReviewDecision string `json:"reviewDecision"`
	HeadRefName    string `json:"headRefName"`
	Commits        struct {
		TotalCount int `json:"totalCount"`
	} `json:"commits"`
	ReviewThreads struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			IsResolved bool `json:"isResolved"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
	Last struct {
		Nodes []struct {
			Commit struct {
				Rollup *struct {
					Contexts struct {
						Nodes []check `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"last"`
}

func (s *Source) PRStatus(url string) (core.PRStatus, error) {
	out, err := s.Run(url)
	if err != nil {
		return core.PRStatus{}, err
	}
	var v struct {
		Data struct {
			Resource *prJSON `json:"resource"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return core.PRStatus{}, err
	}
	pr := v.Data.Resource
	if pr == nil {
		return core.PRStatus{}, errors.New("no pull request at " + url)
	}
	st := core.PRStatus{State: pr.State, Title: pr.Title, Review: pr.ReviewDecision, Head: pr.HeadRefName,
		Commits: pr.Commits.TotalCount, Threads: pr.ReviewThreads.TotalCount, Fetched: s.Now()}
	for _, t := range pr.ReviewThreads.Nodes {
		if !t.IsResolved {
			st.Unresolved++
		}
	}
	// Threads past the first page are counted unresolved until they can be read.
	st.Unresolved += st.Threads - len(pr.ReviewThreads.Nodes)
	for _, n := range pr.Last.Nodes {
		if n.Commit.Rollup == nil {
			continue
		}
		for _, c := range n.Commit.Rollup.Contexts.Nodes {
			switch result(c) {
			case "pass":
				st.Passed++
			case "fail":
				st.Failed++
			default:
				st.Pending++
			}
		}
	}
	return st, nil
}

// result folds a check run's conclusion or a status context's state into pass, fail or pending.
func result(c check) string {
	v := c.Conclusion
	if v == "" {
		v = c.State
	}
	switch v {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return "pass"
	case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
		return "fail"
	}
	return "pending"
}
