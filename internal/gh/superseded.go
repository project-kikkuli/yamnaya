package gh

import (
	"encoding/json"
	"fmt"
)

const checkRunsQuery = `query($url: URI!) { resource(url: $url) { ... on PullRequest { last: commits(last: 1) { nodes { commit { statusCheckRollup {
  contexts(first: 100%s) { pageInfo { hasNextPage endCursor } nodes { ... on CheckRun {
    databaseId name conclusion checkSuite { app { slug } workflowRun { workflow { name } } } } } } } } } } } } }`

// superseded counts the failed runs on the pull request's head that a newer run of the same check replaced, reading every run.
func (s *Source) superseded(url string) (int, error) {
	if s.Batch == nil {
		return 0, fmt.Errorf("no query to read the runs of %s", url)
	}
	type check struct{ source, name string }
	failed, newest, newestFailed := map[check]int{}, map[check]int64{}, map[check]bool{}
	for after := ""; ; {
		out, err := s.Batch(fmt.Sprintf(checkRunsQuery, after), map[string]string{"url": url})
		if err != nil {
			return 0, err
		}
		var v struct {
			Data struct {
				Resource struct {
					Last struct {
						Nodes []struct {
							Commit struct {
								Rollup struct {
									Contexts struct {
										PageInfo struct {
											HasNextPage bool   `json:"hasNextPage"`
											EndCursor   string `json:"endCursor"`
										} `json:"pageInfo"`
										Nodes []struct {
											ID         int64  `json:"databaseId"`
											Name       string `json:"name"`
											Conclusion string `json:"conclusion"`
											Suite      struct {
												App struct {
													Slug string `json:"slug"`
												} `json:"app"`
												Run struct {
													Workflow struct {
														Name string `json:"name"`
													} `json:"workflow"`
												} `json:"workflowRun"`
											} `json:"checkSuite"`
										} `json:"nodes"`
									} `json:"contexts"`
								} `json:"statusCheckRollup"`
							} `json:"commit"`
						} `json:"nodes"`
					} `json:"last"`
				} `json:"resource"`
			} `json:"data"`
		}
		if err := json.Unmarshal(out, &v); err != nil {
			return 0, err
		}
		if len(v.Data.Resource.Last.Nodes) == 0 {
			return 0, fmt.Errorf("no head commit in the runs of %s", url)
		}
		contexts := v.Data.Resource.Last.Nodes[0].Commit.Rollup.Contexts
		for _, r := range contexts.Nodes {
			// A status context has no id here and GitHub keeps only its latest state.
			if r.ID == 0 {
				continue
			}
			c := check{r.Suite.App.Slug + "/" + r.Suite.Run.Workflow.Name, r.Name}
			bad := result(r.Conclusion) == "fail"
			if bad {
				failed[c]++
			}
			if r.ID > newest[c] {
				newest[c], newestFailed[c] = r.ID, bad
			}
		}
		if !contexts.PageInfo.HasNextPage {
			break
		}
		after = fmt.Sprintf(", after: %q", contexts.PageInfo.EndCursor)
	}
	n := 0
	for c, f := range failed {
		n += f
		if newestFailed[c] {
			n--
		}
	}
	return n, nil
}
