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
	endpoint := "/search/issues?q=" + url.QueryEscape("is:pr is:open review-requested:"+login) + "&per_page=100"
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
	for _, page := range pages {
		if page.Incomplete || page.Total > 1000 {
			return nil, fmt.Errorf("GitHub search coverage incomplete; narrow the watched repositories")
		}
		for _, item := range page.Items {
			if item.Pull.URL != "" {
				out = append(out, work.Source{Kind: "github", URL: item.Pull.URL, Requester: "GitHub review assignment to " + login, Question: "Review this pull request", Coverage: "explicit GitHub review-request assignment", RequestVersion: item.Updated})
			}
		}
	}
	return out, nil
}
