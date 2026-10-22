// Package gh fetches pull request status with the gh CLI.
package gh

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// Source reads PR lifecycle over REST and readiness details over GraphQL.
type Source struct {
	REST     func(endpoint string) ([]byte, error)
	viewerMu sync.Mutex
	viewer   string
	Run      func(url string) ([]byte, error)
	// Repo reports whether GitHub has repository owner/name.
	Repo func(repo string) (bool, error)
	Now  func() time.Time
	// LandingRun runs the landing query for a pull request URL; nil runs gh.
	LandingRun func(url string) ([]byte, error)
}

func New() *Source { return &Source{Run: query, REST: rest, Repo: repoExists, Now: time.Now} }

func repoExists(repo string) (bool, error) {
	out, err := exec.Command("gh", "api", "repos/"+repo, "--silent").CombinedOutput()
	if err != nil && strings.Contains(string(out), "HTTP 404") {
		return false, nil
	}
	return err == nil, err
}

const prQuery = `query($url: URI!) { resource(url: $url) { ... on PullRequest {
  state title reviewDecision headRefName headRefOid isDraft mergeable mergeStateStatus
  commits { totalCount }
  reviewThreads(first: 100) { totalCount nodes { isResolved } }
  author { login }
  latestReviews(first: 50) { nodes { state body author { __typename login } commit { oid } } }
  last: commits(last: 1) { nodes { commit { statusCheckRollup { contexts {
    checkRunCountsByState { state count } statusContextCountsByState { state count } } } } } }
  baseRef { target { ... on Commit { statusCheckRollup { state } } } }
} } }`

func query(url string) ([]byte, error) {
	out, err := exec.Command("gh", "api", "graphql", "-f", "url="+url, "-f", "query="+prQuery).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
	}
	return out, err
}

type count struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

type prJSON struct {
	HeadRefOID     string `json:"headRefOid"`
	Draft          bool   `json:"isDraft"`
	Mergeable      string `json:"mergeable"`
	MergeState     string `json:"mergeStateStatus"`
	State          string `json:"state"`
	Title          string `json:"title"`
	ReviewDecision string `json:"reviewDecision"`
	HeadRefName    string `json:"headRefName"`
	Commits        struct {
		TotalCount int `json:"totalCount"`
	} `json:"commits"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	LatestReviews struct {
		Nodes []struct {
			State  string `json:"state"`
			Body   string `json:"body"`
			Author struct {
				Type  string `json:"__typename"`
				Login string `json:"login"`
			} `json:"author"`
			Commit struct {
				OID string `json:"oid"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"latestReviews"`
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
						Runs     []count `json:"checkRunCountsByState"`
						Statuses []count `json:"statusContextCountsByState"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"last"`
	BaseRef *struct {
		Target struct {
			Rollup *struct {
				State string `json:"state"`
			} `json:"statusCheckRollup"`
		} `json:"target"`
	} `json:"baseRef"`
}

func (s *Source) details(url string) (core.PRStatus, error) {
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
		if prs := core.PRsIn(url); len(prs) > 0 {
			if ok, err := s.Repo(prs[0].Repo); err == nil && !ok {
				return core.PRStatus{State: core.PRGone, Fetched: s.Now()}, nil
			}
		}
		return core.PRStatus{}, errors.New("no pull request at " + url)
	}
	st := core.PRStatus{Merge: core.PRMergeFacts{OID: pr.HeadRefOID, Draft: pr.Draft, Mergeable: pr.Mergeable, MergeState: pr.MergeState}, State: pr.State, Title: pr.Title, Review: pr.ReviewDecision, Head: pr.HeadRefName,
		Commits: pr.Commits.TotalCount, Threads: pr.ReviewThreads.TotalCount, Fetched: s.Now()}
	for _, t := range pr.ReviewThreads.Nodes {
		if !t.IsResolved {
			st.Unresolved++
		}
	}
	// Threads past the first page are counted unresolved until they can be read.
	st.Unresolved += st.Threads - len(pr.ReviewThreads.Nodes)
	// A reviewer's standing change request, or a person's top-level comment on the current head, is open feedback like a thread;
	// a bot's top-level review is a summary of the threads it opened.
	for _, r := range pr.LatestReviews.Nodes {
		if r.State == "CHANGES_REQUESTED" || (r.State == "COMMENTED" && r.Author.Type != "Bot" && strings.TrimSpace(r.Body) != "" && r.Commit.OID == pr.HeadRefOID && !strings.EqualFold(r.Author.Login, pr.Author.Login)) {
			st.Unresolved++
		}
	}
	for _, n := range pr.Last.Nodes {
		if n.Commit.Rollup == nil {
			continue
		}
		ctx := n.Commit.Rollup.Contexts
		for _, c := range append(ctx.Runs, ctx.Statuses...) {
			switch result(c.State) {
			case "pass":
				st.Passed += c.Count
			case "fail":
				st.Failed += c.Count
			default:
				st.Pending += c.Count
			}
		}
	}
	// A head with no checks yet, on a base branch that runs them, is waiting for its checks to register.
	if st.Passed+st.Failed+st.Pending == 0 && pr.State == "OPEN" && pr.BaseRef != nil && pr.BaseRef.Target.Rollup != nil {
		st.Pending = 1
	}
	return st, nil
}

// result folds a check run or status context state into pass, fail or pending.
func result(state string) string {
	switch state {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return "pass"
	case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
		return "fail"
	}
	return "pending"
}
