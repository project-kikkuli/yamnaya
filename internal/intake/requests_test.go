package intake

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestGitHubIntakeReportsCoverageAndHumanRequestIdentity(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		f := Fetcher{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if args[0] == "api" && args[1] == "user" {
				return []byte("reviewer\n"), nil
			}
			if strings.HasSuffix(args[len(args)-1], "/requested_reviewers") {
				return []byte(`{"users":[{"login":"reviewer"}],"teams":[]}`), nil
			}
			return []byte(fmt.Sprintf(`[{"total_count":1,"incomplete_results":%t,"items":[{"updated_at":"2026-01-02T00:00:00Z","title":"Read CSV","user":{"login":"mira"},"pull_request":{"html_url":"https://github.com/example/query/pull/241"}}]}]`, incomplete)), nil
		}}
		got, err := f.Requests(context.Background())
		if incomplete {
			if err == nil {
				t.Fatal("incomplete search claimed full coverage")
			}
			continue
		}
		if err != nil || len(got) != 1 || got[0] != (Request{URL: "https://github.com/example/query/pull/241", Updated: "2026-01-02T00:00:00Z", Title: "Read CSV", Author: "mira"}) {
			t.Fatalf("request identity lost: %+v %v", got, err)
		}
	}
}

func TestGitHubRequestsRequireAnIndividualReviewerEvenWithStaleSearchResults(t *testing.T) {
	for _, tc := range []struct {
		name, reviewers string
		want            int
	}{
		{"direct", `{"users":[{"login":"reviewer"}],"teams":[]}`, 1},
		{"team only", `{"users":[],"teams":[{"slug":"reviewer"}]}`, 0},
		{"mixed", `{"users":[{"login":"REVIEWER"}],"teams":[{"slug":"developers"}]}`, 1},
		{"another user", `{"users":[{"login":"someone-else"}],"teams":[{"slug":"developers"}]}`, 0},
		{"withdrawn", `{"users":[],"teams":[]}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := Fetcher{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				switch {
				case args[1] == "user":
					return []byte("reviewer"), nil
				case strings.HasSuffix(args[len(args)-1], "/requested_reviewers"):
					return []byte(tc.reviewers), nil
				default:
					endpoint, err := url.Parse(args[len(args)-1])
					if err != nil || endpoint.Query().Get("q") != "is:pr is:open user-review-requested:reviewer" {
						t.Fatalf("search includes indirect assignments: %v", args)
					}
					return []byte(`[{"total_count":1,"items":[{"updated_at":"2026-01-02T00:00:00Z","pull_request":{"html_url":"https://github.com/example/query/pull/241"}}]}]`), nil
				}
			}}
			got, err := f.Requests(context.Background())
			if err != nil || len(got) != tc.want {
				t.Fatalf("requests = %+v, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestAuthoredListsEveryOpenPullRequestTheOwnerWroteOrFailsWhole(t *testing.T) {
	for _, tc := range []struct {
		name, page string
		want       int
		fail       bool
	}{
		{"two", `[{"total_count":2,"items":[{"title":"A","user":{"login":"owner"},"pull_request":{"html_url":"https://github.com/a/b/pull/1"}},{"title":"B","user":{"login":"owner"},"pull_request":{"html_url":"https://github.com/c/d/pull/2"}}]}]`, 2, false},
		{"none", `[{"total_count":0,"items":[]}]`, 0, false},
		{"incomplete", `[{"total_count":2,"incomplete_results":true,"items":[]}]`, 0, true},
		{"short page", `[{"total_count":3,"items":[{"pull_request":{"html_url":"https://github.com/a/b/pull/1"}}]}]`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := Fetcher{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if args[1] == "user" {
					return []byte("owner\n"), nil
				}
				endpoint, err := url.Parse(args[len(args)-1])
				if err != nil || endpoint.Query().Get("q") != "is:pr is:open author:owner" {
					t.Fatalf("search is not the owner's open pull requests: %v", args)
				}
				return []byte(tc.page), nil
			}}
			got, err := f.Authored(context.Background())
			if (err != nil) != tc.fail || len(got) != tc.want {
				t.Fatalf("authored = %+v, %v; want %d, fail %v", got, err, tc.want, tc.fail)
			}
		})
	}
}
