package gh

import (
	"cmp"
	"encoding/json"
	"slices"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

const (
	// laterCommits is how many of the base branch's newest 100 commits have their checks read, laterGreen of those whole-green ones first.
	laterCommits = 6
	laterGreen   = 3
)

const mergeBaseQuery = `query($url: URI!, $merge: String!) { resource(url: $url) { ... on PullRequest {
  repository { url }
  mergeCommit { parents(first: 1) { nodes { oid } } }
  baseRef { compare(headRef: $merge) { status } target { ... on Commit { oid history(first: 100) { nodes {
    oid parents(first: 1) { nodes { oid } } statusCheckRollup { state contexts { checkRunCount } } } } } } }
} } }`

const commitJobsQuery = `query($url: URI!, $after: String) { resource(url: $url) { ... on Commit { statusCheckRollup {
  contexts(first: 100, after: $after) { pageInfo { hasNextPage endCursor } nodes {
    ... on CheckRun { name status conclusion checkSuite { app { slug } workflowRun { workflow { name } } } }
    ... on StatusContext { context state } } } } } } }`

type laterCommit struct {
	OID     string `json:"oid"`
	Parents struct {
		Nodes []struct {
			OID string `json:"oid"`
		} `json:"nodes"`
	} `json:"parents"`
	Rollup *struct {
		State    string `json:"state"`
		Contexts struct {
			CheckRunCount int `json:"checkRunCount"`
		} `json:"contexts"`
	} `json:"statusCheckRollup"`
}

// MergeJobs reads, for each job red on merge, its result on later commits of the pull request's base branch and on the merge's parent.
func (s *Source) MergeJobs(url, merge string) ([]core.MergeJob, error) {
	run := s.Batch
	if run == nil {
		run = graphQL
	}
	out, err := run(mergeBaseQuery, map[string]string{"url": url, "merge": merge})
	if err != nil {
		return nil, err
	}
	var v struct {
		Data struct {
			Resource *struct {
				Repository struct {
					URL string `json:"url"`
				} `json:"repository"`
				MergeCommit *struct {
					Parents struct {
						Nodes []struct {
							OID string `json:"oid"`
						} `json:"nodes"`
					} `json:"parents"`
				} `json:"mergeCommit"`
				BaseRef *struct {
					Compare *struct {
						Status string `json:"status"`
					} `json:"compare"`
					Target struct {
						OID     string `json:"oid"`
						History struct {
							Nodes []laterCommit `json:"nodes"`
						} `json:"history"`
					} `json:"target"`
				} `json:"baseRef"`
			} `json:"resource"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, err
	}
	pr := v.Data.Resource
	if pr == nil {
		return nil, nil
	}
	commit := func(oid string) string { return pr.Repository.URL + "/commit/" + oid }
	read := func(oid string) (map[string]string, error) {
		return commitJobs(run, commit(oid), checkContext.evidence)
	}
	statusRed := false
	onMerge, err := commitJobs(run, commit(merge), func(c checkContext) bool {
		statusRed = statusRed || c.Context != "" && result(c.State) == core.JobFail
		return true
	})
	if err != nil {
		return nil, err
	}
	var jobs []core.MergeJob
	for name, res := range onMerge {
		if res == core.JobFail {
			jobs = append(jobs, core.MergeJob{Name: name})
		}
	}
	slices.SortFunc(jobs, func(a, b core.MergeJob) int { return cmp.Compare(a.Name, b.Name) })
	var later []string
	// Base merges land on its first-parent line, so a commit on that line above a merge the branch contains contains it too.
	if b := pr.BaseRef; b != nil && b.Compare != nil && b.Compare.Status == "BEHIND" {
		later = laterOn(b.Target.History.Nodes, b.Target.OID, merge, statusRed)
	}
	for _, oid := range later {
		if allGreen(jobs) {
			break
		}
		on, err := read(oid)
		if err != nil {
			return nil, err
		}
		for i := range jobs {
			res, ran := on[jobs[i].Name]
			if !ran || rank(res) <= rank(jobs[i].Later) {
				continue
			}
			jobs[i].Later, jobs[i].LaterAt = res, oid
		}
	}
	if !allGreen(jobs) && pr.MergeCommit != nil && len(pr.MergeCommit.Parents.Nodes) > 0 {
		parent := pr.MergeCommit.Parents.Nodes[0].OID
		on, err := read(parent)
		if err != nil {
			return nil, err
		}
		for i := range jobs {
			jobs[i].Parent, jobs[i].ParentAt = on[jobs[i].Name], parent
		}
	}
	return jobs, nil
}

// laterOn picks the commits to read: on head's first-parent line above merge, with check runs, or any checks when a status context
// is what went red, whole-green ones first because one clears every job that really ran on it.
func laterOn(history []laterCommit, head, merge string, statusRed bool) []string {
	byOID := make(map[string]laterCommit, len(history))
	for _, c := range history {
		byOID[c.OID] = c
	}
	var green, rest []string
	for oid := head; oid != merge; {
		c, ok := byOID[oid]
		if !ok {
			break
		}
		if c.Rollup != nil && (statusRed || c.Rollup.Contexts.CheckRunCount > 0) {
			if c.Rollup.State == "SUCCESS" && len(green) < laterGreen {
				green = append(green, oid)
			} else {
				rest = append(rest, oid)
			}
		}
		if len(c.Parents.Nodes) == 0 {
			break
		}
		oid = c.Parents.Nodes[0].OID
	}
	picked := append(green, rest...)
	return picked[:min(len(picked), laterCommits)]
}

func allGreen(jobs []core.MergeJob) bool {
	return !slices.ContainsFunc(jobs, func(j core.MergeJob) bool { return j.Later != core.JobPass })
}

// rank orders what later commits say of a job: any green settles it, and a finished red outweighs a run still going.
func rank(res string) int {
	switch res {
	case core.JobPass:
		return 3
	case core.JobFail:
		return 2
	case core.JobPending:
		return 1
	}
	return 0
}

// commitJobs reads the checks on the commit at url that keep accepts, by job. A job run twice on one commit reads as its worse result.
func commitJobs(run func(string, map[string]string) ([]byte, error), url string, keep func(checkContext) bool) (map[string]string, error) {
	jobs := map[string]string{}
	vars := map[string]string{"url": url}
	for {
		out, err := run(commitJobsQuery, vars)
		if err != nil {
			return nil, err
		}
		var v struct {
			Data struct {
				Resource *struct {
					Rollup *struct {
						Contexts struct {
							PageInfo struct {
								HasNextPage bool   `json:"hasNextPage"`
								EndCursor   string `json:"endCursor"`
							} `json:"pageInfo"`
							Nodes []checkContext `json:"nodes"`
						} `json:"contexts"`
					} `json:"statusCheckRollup"`
				} `json:"resource"`
			} `json:"data"`
		}
		if err := json.Unmarshal(out, &v); err != nil {
			return nil, err
		}
		if v.Data.Resource == nil || v.Data.Resource.Rollup == nil {
			return jobs, nil
		}
		contexts := v.Data.Resource.Rollup.Contexts
		for _, n := range contexts.Nodes {
			if !keep(n) {
				continue
			}
			name, res := n.job()
			if prior, ran := jobs[name]; !ran || worse(res, prior) {
				jobs[name] = res
			}
		}
		if !contexts.PageInfo.HasNextPage {
			return jobs, nil
		}
		vars = map[string]string{"url": url, "after": contexts.PageInfo.EndCursor}
	}
}

func worse(a, b string) bool {
	return a == core.JobFail && b != core.JobFail || a == core.JobPending && b == core.JobPass
}
