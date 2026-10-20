package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/work"
)

func (f Fetcher) Requests(ctx context.Context) ([]work.Source, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	user, err := f.Run(ctx, "gh", "api", "user", "--jq", ".login")
	if err != nil {
		return nil, fmt.Errorf("GitHub review-request coverage unavailable: %w", err)
	}
	login := strings.TrimSpace(string(user))
	if login == "" {
		return nil, fmt.Errorf("GitHub did not identify the review owner")
	}
	endpoint := "/search/issues?q=" + url.QueryEscape("is:pr is:open user-review-requested:"+login) + "&per_page=100"
	raw, err := f.Run(ctx, "gh", "api", "--paginate", "--slurp", endpoint)
	if err != nil {
		return nil, fmt.Errorf("GitHub review-request coverage unavailable: %w", err)
	}
	var pages []struct {
		Total      int  `json:"total_count"`
		Incomplete bool `json:"incomplete_results"`
		Items      []struct {
			Updated string `json:"updated_at"`
			Pull    struct {
				URL string `json:"html_url"`
			} `json:"pull_request"`
		}
	}
	if err = json.Unmarshal(raw, &pages); err != nil {
		return nil, err
	}
	var out []work.Source
	count := 0
	for _, page := range pages {
		if page.Incomplete || page.Total > 1000 {
			return nil, fmt.Errorf("GitHub search coverage incomplete; narrow the watched repositories")
		}
		for _, item := range page.Items {
			count++
			if item.Pull.URL != "" {
				u, err := url.Parse(item.Pull.URL)
				if err != nil || u.Host != "github.com" {
					return nil, fmt.Errorf("GitHub search returned an invalid pull request URL")
				}
				path := strings.Replace(strings.Trim(u.Path, "/"), "/pull/", "/pulls/", 1)
				raw, err := f.Run(ctx, "gh", "api", "repos/"+path+"/requested_reviewers")
				if err != nil {
					return nil, fmt.Errorf("GitHub direct review-request verification unavailable: %w", err)
				}
				var reviewers struct{ Users []struct{ Login string } }
				if err = json.Unmarshal(raw, &reviewers); err != nil {
					return nil, err
				}
				for _, user := range reviewers.Users {
					if strings.EqualFold(user.Login, login) {
						out = append(out, work.Source{Origin: "github-request", Kind: "github", URL: item.Pull.URL, Requester: "GitHub review assignment to " + login, Question: "Review this pull request", Coverage: "direct GitHub user review-request assignment", RequestVersion: item.Updated})
						break
					}
				}
			}
		}
	}
	if len(pages) == 0 || count != pages[0].Total {
		return nil, fmt.Errorf("GitHub search coverage incomplete; review requests were not reconciled")
	}
	return out, nil
}
