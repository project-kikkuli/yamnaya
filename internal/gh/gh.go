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
	// Batch runs one GraphQL query with string variables; nil reads each pull request alone.
	Batch func(query string, vars map[string]string) ([]byte, error)
	// Repo reports whether GitHub has repository owner/name.
	Repo func(repo string) (bool, error)
	Now  func() time.Time
	// LandingRun runs the landing query for a pull request URL; nil runs gh.
	LandingRun func(url string) ([]byte, error)

	budgetMu sync.Mutex
	// paused is when batches may run again, after the account's GraphQL budget fell to the reserve or ran out.
	paused time.Time
}

// Reserve is the share of the account's hourly GraphQL budget ym leaves to the owner and their agents: below it, ym reads no
// pull requests until the budget resets. Every ym reads the same account budget, so all of them stop together.
const Reserve = 0.5

// rateLimitWait is how long batches stop after GitHub refused one for its rate limit without saying when the budget resets.
const rateLimitWait = 5 * time.Minute

// ErrPaused is PRStatuses' answer while the account's GraphQL budget is at the reserve.
var ErrPaused = errors.New("GitHub GraphQL budget at ym's reserve; waiting for it to reset")

func New() *Source {
	return &Source{Run: query, Batch: graphQL, REST: rest, Repo: repoExists, Now: time.Now}
}

func repoExists(repo string) (bool, error) {
	out, err := exec.Command("gh", "api", "repos/"+repo, "--silent").CombinedOutput()
	if err != nil && strings.Contains(string(out), "HTTP 404") {
		return false, nil
	}
	return err == nil, err
}

const prFields = `... on PullRequest {
  state title reviewDecision headRefName headRefOid isDraft mergeable mergeStateStatus
  commits { totalCount }
  reviewThreads(first: 100) { totalCount nodes { isResolved } }
  author { login }
  latestReviews(first: 50) { nodes { state body author { __typename login } commit { oid } } }
  last: commits(last: 1) { nodes { commit { statusCheckRollup { contexts {
    checkRunCountsByState { state count } statusContextCountsByState { state count } } } } } }
  baseRef { target { ... on Commit { statusCheckRollup { state } } } }
}`

const prQuery = `query($url: URI!) { resource(url: $url) { ` + prFields + ` } }`

func query(url string) ([]byte, error) {
	out, err := graphQL(prQuery, map[string]string{"url": url})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// graphQL runs a query with gh; on a failed query it still returns what GitHub answered, which names each part that failed.
func graphQL(q string, vars map[string]string) ([]byte, error) {
	args := []string{"api", "graphql", "-f", "query=" + q}
	for k, v := range vars {
		args = append(args, "-f", k+"="+v)
	}
	out, err := exec.Command("gh", args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
	}
	return out, err
}

// PRStatuses reads up to core.PRBatchSize pull requests and the signed-in account in one GraphQL query. A pull request the query
// could not read is missing from the result; the caller reads it alone.
func (s *Source) PRStatuses(urls []string) (map[string]core.PRStatus, error) {
	if s.Batch == nil {
		return nil, errors.New("no batch query")
	}
	s.budgetMu.Lock()
	paused := s.paused
	s.budgetMu.Unlock()
	if s.Now().Before(paused) {
		return nil, ErrPaused
	}
	var q strings.Builder
	q.WriteString("query(")
	vars := map[string]string{}
	for i, u := range urls {
		if i > 0 {
			q.WriteString(", ")
		}
		fmt.Fprintf(&q, "$u%d: URI!", i)
		vars[fmt.Sprintf("u%d", i)] = u
	}
	q.WriteString(") { viewer { login } rateLimit { limit remaining resetAt }")
	for i := range urls {
		fmt.Fprintf(&q, " p%d: resource(url: $u%d) { %s }", i, i, prFields)
	}
	q.WriteString(" }")
	out, err := s.Batch(q.String(), vars)
	var v struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []struct {
			Type string `json:"type"`
		} `json:"errors"`
	}
	jerr := json.Unmarshal(out, &v)
	limited := err != nil && strings.Contains(strings.ToLower(err.Error()), "rate limit")
	for _, e := range v.Errors {
		limited = limited || e.Type == "RATE_LIMITED"
	}
	if limited {
		s.pause(s.Now().Add(rateLimitWait))
	}
	if jerr != nil || v.Data == nil {
		return nil, errors.Join(err, jerr, errors.New("no data in the batch reply"))
	}
	var budget struct {
		Limit     int       `json:"limit"`
		Remaining int       `json:"remaining"`
		ResetAt   time.Time `json:"resetAt"`
	}
	if raw, ok := v.Data["rateLimit"]; ok && json.Unmarshal(raw, &budget) == nil && budget.Limit > 0 && float64(budget.Remaining) < Reserve*float64(budget.Limit) {
		s.pause(budget.ResetAt)
	}
	var viewer struct{ Login string }
	if raw, ok := v.Data["viewer"]; ok {
		_ = json.Unmarshal(raw, &viewer)
	}
	if viewer.Login != "" {
		s.viewerMu.Lock()
		s.viewer = viewer.Login
		s.viewerMu.Unlock()
	}
	got := map[string]core.PRStatus{}
	for i, u := range urls {
		var pr *prJSON
		if raw, ok := v.Data[fmt.Sprintf("p%d", i)]; !ok || json.Unmarshal(raw, &pr) != nil || pr == nil || pr.State == "" {
			continue
		}
		st := s.status(pr)
		st.Merge.Author, st.Merge.Viewer = pr.Author.Login, viewer.Login
		got[u] = st
	}
	return got, nil
}

func (s *Source) pause(until time.Time) {
	s.budgetMu.Lock()
	defer s.budgetMu.Unlock()
	if until.After(s.paused) {
		s.paused = until
	}
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
	return s.status(pr), nil
}

func (s *Source) status(pr *prJSON) core.PRStatus {
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
	// A head with no checks yet, on a base branch that runs them, is waiting for its checks to register, unless it conflicts with
	// its base: GitHub runs no pull request checks on it until it is rebased.
	if st.Passed+st.Failed+st.Pending == 0 && pr.State == "OPEN" && pr.Mergeable != "CONFLICTING" && pr.BaseRef != nil && pr.BaseRef.Target.Rollup != nil {
		st.Pending = 1
	}
	return st
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
