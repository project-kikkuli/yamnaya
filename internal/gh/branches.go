package gh

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func (s *Source) PRsForBranches(branches []core.PRBranch) (map[core.PRBranch][]core.PR, error) {
	if s.Batch == nil {
		return nil, errors.New("no branch discovery query")
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
	for i, branch := range branches {
		if i > 0 {
			q.WriteString(", ")
		}
		fmt.Fprintf(&q, "$o%d: String!, $r%d: String!, $h%d: String!", i, i, i)
		owner, repo, _ := strings.Cut(branch.Repo, "/")
		vars[fmt.Sprintf("o%d", i)], vars[fmt.Sprintf("r%d", i)], vars[fmt.Sprintf("h%d", i)] = owner, repo, branch.Head
	}
	q.WriteString(") { rateLimit { limit remaining resetAt }")
	for i := range branches {
		fmt.Fprintf(&q, " b%d: repository(owner: $o%d, name: $r%d) { pullRequests(headRefName: $h%d, states: OPEN, first: 100) { nodes { url number headRepository { nameWithOwner } } } }", i, i, i, i)
	}
	q.WriteString(" }")
	out, err := s.Batch(q.String(), vars)
	var reply struct {
		Data   map[string]json.RawMessage
		Errors []struct{ Type string }
	}
	if jerr := json.Unmarshal(out, &reply); jerr != nil {
		return nil, errors.Join(err, jerr)
	}
	for _, e := range reply.Errors {
		if e.Type == "RATE_LIMITED" {
			s.pause(s.Now().Add(rateLimitWait))
			return nil, ErrPaused
		}
	}
	if reply.Data == nil {
		return nil, errors.Join(err, errors.New("no data in branch discovery reply"))
	}
	var budget struct {
		Limit, Remaining int
		ResetAt          time.Time
	}
	if json.Unmarshal(reply.Data["rateLimit"], &budget) == nil && budget.Limit > 0 {
		s.budgetMu.Lock()
		s.left, s.reset = float64(budget.Remaining)/float64(budget.Limit), budget.ResetAt
		s.budgetMu.Unlock()
	}
	found := map[core.PRBranch][]core.PR{}
	for i, branch := range branches {
		var repo *struct {
			PullRequests struct {
				Nodes []struct {
					URL            string
					Number         int
					HeadRepository struct{ NameWithOwner string }
				}
			}
		}
		if json.Unmarshal(reply.Data[fmt.Sprintf("b%d", i)], &repo) != nil || repo == nil {
			continue
		}
		found[branch] = nil
		for _, pr := range repo.PullRequests.Nodes {
			if strings.EqualFold(pr.HeadRepository.NameWithOwner, branch.Repo) {
				found[branch] = append(found[branch], core.PR{URL: pr.URL, Repo: branch.Repo, Number: pr.Number})
			}
		}
	}
	return found, nil
}
