package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Request is an open pull request GitHub asks the owner to review as an individual reviewer.
type Request struct {
	URL, Updated string
	// Title and Author are the pull request's title and the login of who opened it.
	Title, Author string
}

// Requests lists the owner's direct review requests, failing rather than returning a partial list.
func (f Fetcher) Requests(ctx context.Context) ([]Request, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	login, found, err := f.search(ctx, "user-review-requested")
	if err != nil {
		return nil, fmt.Errorf("GitHub review-request coverage unavailable: %w", err)
	}
	var out []Request
	for _, r := range found {
		u, err := url.Parse(r.URL)
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
				out = append(out, r)
				break
			}
		}
	}
	return out, nil
}

// Authored lists the owner's own open pull requests in every repository, failing rather than returning a partial list.
func (f Fetcher) Authored(ctx context.Context) ([]Request, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, found, err := f.search(ctx, "author")
	if err != nil {
		return nil, fmt.Errorf("GitHub authored pull request coverage unavailable: %w", err)
	}
	return found, nil
}

// search lists the open pull requests whose qualifier names the owner, with the owner's login.
func (f Fetcher) search(ctx context.Context, qualifier string) (string, []Request, error) {
	user, err := f.Run(ctx, "gh", "api", "user", "--jq", ".login")
	if err != nil {
		return "", nil, err
	}
	login := strings.TrimSpace(string(user))
	if login == "" {
		return "", nil, fmt.Errorf("GitHub did not identify the owner")
	}
	endpoint := "/search/issues?q=" + url.QueryEscape("is:pr is:open "+qualifier+":"+login) + "&per_page=100"
	raw, err := f.Run(ctx, "gh", "api", "--paginate", "--slurp", endpoint)
	if err != nil {
		return "", nil, err
	}
	var pages []struct {
		Total      int  `json:"total_count"`
		Incomplete bool `json:"incomplete_results"`
		Items      []struct {
			Updated string `json:"updated_at"`
			Title   string `json:"title"`
			User    struct {
				Login string `json:"login"`
			} `json:"user"`
			Pull struct {
				URL string `json:"html_url"`
			} `json:"pull_request"`
		}
	}
	if err = json.Unmarshal(raw, &pages); err != nil {
		return "", nil, err
	}
	var out []Request
	count := 0
	for _, page := range pages {
		if page.Incomplete || page.Total > 1000 {
			return "", nil, fmt.Errorf("GitHub search coverage incomplete; narrow the watched repositories")
		}
		for _, item := range page.Items {
			count++
			if item.Pull.URL != "" {
				out = append(out, Request{URL: item.Pull.URL, Updated: item.Updated, Title: item.Title, Author: item.User.Login})
			}
		}
	}
	if len(pages) == 0 || count != pages[0].Total {
		return "", nil, fmt.Errorf("GitHub search coverage incomplete; pull requests were not reconciled")
	}
	return login, out, nil
}
