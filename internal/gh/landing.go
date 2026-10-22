package gh

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

const landingQuery = `query($url: URI!) { resource(url: $url) { ... on PullRequest {
  state
  mergeCommit { oid statusCheckRollup { contexts {
    checkRunCountsByState { state count } statusContextCountsByState { state count } } } }
  reviewThreads(first: 100) { totalCount nodes { isResolved } }
} } }`

func landingRun(url string) ([]byte, error) {
	out, err := exec.Command("gh", "api", "graphql", "-f", "url="+url, "-f", "query="+landingQuery).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
	}
	return out, err
}

// Landing reads whether the pull request at url merged, how CI went on its merge commit, and how many review threads stay open.
func (s *Source) Landing(url string) (core.Landing, error) {
	run := s.LandingRun
	if run == nil {
		run = landingRun
	}
	out, err := run(url)
	if err != nil {
		return core.Landing{}, err
	}
	var v struct {
		Data struct {
			Resource *struct {
				State       string `json:"state"`
				MergeCommit *struct {
					OID    string `json:"oid"`
					Rollup *struct {
						Contexts struct {
							Runs     []count `json:"checkRunCountsByState"`
							Statuses []count `json:"statusContextCountsByState"`
						} `json:"contexts"`
					} `json:"statusCheckRollup"`
				} `json:"mergeCommit"`
				ReviewThreads struct {
					TotalCount int `json:"totalCount"`
					Nodes      []struct {
						IsResolved bool `json:"isResolved"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"resource"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return core.Landing{}, err
	}
	pr := v.Data.Resource
	if pr == nil {
		return core.Landing{State: core.PRGone}, nil
	}
	l := core.Landing{State: pr.State}
	for _, t := range pr.ReviewThreads.Nodes {
		if !t.IsResolved {
			l.Unresolved++
		}
	}
	l.Unresolved += pr.ReviewThreads.TotalCount - len(pr.ReviewThreads.Nodes)
	if mc := pr.MergeCommit; mc != nil {
		l.MergeCommit = mc.OID
		if mc.Rollup != nil {
			for _, c := range append(mc.Rollup.Contexts.Runs, mc.Rollup.Contexts.Statuses...) {
				switch result(c.State) {
				case "pass":
					l.Passed += c.Count
				case "fail":
					l.Failed += c.Count
				default:
					l.Pending += c.Count
				}
			}
		}
	}
	return l, nil
}
