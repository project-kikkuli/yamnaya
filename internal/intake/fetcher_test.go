package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestNotionRevisionsIncludeNestedAndPaginatedEvidence(t *testing.T) {
	id := "11111111222233334444555555555555"
	version := 1
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("Notion-Version") == "" {
			t.Error("Notion protocol headers missing")
		}
		calls[r.URL.RequestURI()]++
		switch r.URL.Path {
		case "/comments":
			fmt.Fprint(w, `{"results":[],"has_more":false}`)
		case "/pages/" + id:
			fmt.Fprint(w, `{"last_edited_time":"2026-01-01T00:00:00.000Z","properties":{"Name":{"type":"title","title":[{"plain_text":"Permission model"}]}}}`)
		case "/blocks/" + id + "/children":
			if r.URL.Query().Get("start_cursor") == "next" {
				fmt.Fprint(w, `{"results":[{"id":"last","paragraph":{"rich_text":[{"plain_text":"final page"}]}}],"has_more":false}`)
			} else {
				fmt.Fprint(w, `{"results":[{"id":"child","has_children":true}],"has_more":true,"next_cursor":"next"}`)
			}
		case "/blocks/child/children":
			fmt.Fprintf(w, `{"results":[{"id":"example","last_edited_time":"2026-01-0%dT00:00:00.000Z","paragraph":{"rich_text":[{"plain_text":"nested example revision %d"}]}}],"has_more":false}`, version, version)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := Fetcher{Client: srv.Client(), NotionURL: srv.URL, NotionToken: "fixture-token"}
	source := work.Source{Kind: "notion", URL: "https://www.notion.so/" + id, Requester: "Mira", Question: "Accept the inheritance rule?"}
	first, err := f.Fetch(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	version = 2
	second, err := f.Fetch(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if first.Version == second.Version || first.Order >= second.Order {
		t.Fatal("nested source change was invisible")
	}
	if !strings.Contains(second.Evidence, "final page") || !strings.Contains(second.Evidence, "nested example revision 2") || second.Requester != "Mira" {
		t.Fatalf("incomplete evidence: %+v", second)
	}
	if len(calls) != 5 {
		t.Fatalf("source traversal did not reach the whole registered document: %v", calls)
	}
}

func TestSourceFailuresDoNotProduceReadyOrEmptyReviews(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer srv.Close()
			f := Fetcher{Client: srv.Client(), NotionURL: srv.URL, NotionToken: "fixture"}
			r, err := f.Fetch(context.Background(), work.Source{Kind: "notion", URL: "https://notion.so/11111111222233334444555555555555"})
			if err == nil || r.Version != "" || r.State == work.Ready {
				t.Fatalf("source failure represented as review evidence: %+v %v", r, err)
			}
		})
	}
}

func TestGitHubSourceVersionsIgnoreCheckChurnAndTrackTheReviewedContent(t *testing.T) {
	content := map[string]any{"title": "Index change", "body": "Keep old index", "headRefOid": "abc", "updatedAt": "2026-01-01T00:00:00Z", "state": "OPEN", "author": map[string]string{"login": "developer"}, "statusCheckRollup": []string{"running"}}
	f := Fetcher{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) { return json.Marshal(content) }}
	source := work.Source{Kind: "github", URL: "https://github.com/example/query/pull/241", Requester: "owner"}
	a, err := f.Fetch(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	content["statusCheckRollup"] = []string{"passing"}
	b, err := f.Fetch(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if a.Version != b.Version {
		t.Fatal("CI polling invalidated a source-identical review")
	}
	content["headRefOid"] = "def"
	content["updatedAt"] = "2026-01-02T00:00:00Z"
	c, err := f.Fetch(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if c.Version == a.Version || c.Order <= a.Order {
		t.Fatal("head change was not a new source revision")
	}
}

func TestGitHubIntakeReportsCoverageAndHumanRequestIdentity(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		f := Fetcher{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if args[0] == "api" && args[1] == "user" {
				return []byte("reviewer\n"), nil
			}
			return []byte(fmt.Sprintf(`[{"total_count":1,"incomplete_results":%t,"items":[{"updated_at":"2026-01-02T00:00:00Z","pull_request":{"html_url":"https://github.com/example/query/pull/241"}}]}]`, incomplete)), nil
		}}
		got, err := f.Requests(context.Background())
		if incomplete {
			if err == nil {
				t.Fatal("incomplete search claimed full coverage")
			}
			continue
		}
		if err != nil || len(got) != 1 || got[0].Requester != "GitHub review assignment to reviewer" || got[0].RequestVersion == "" {
			t.Fatalf("request identity lost: %+v %v", got, err)
		}
	}
}

func TestEquivalentSourceLinksHaveOneIdentity(t *testing.T) {
	for _, tc := range []struct{ kind, a, b string }{{"notion", "https://workspace.notion.site/design-11111111222233334444555555555555?foo=bar", "https://www.notion.so/11111111-2222-3333-4444-555555555555"}, {"github", "https://github.com/example/query/pull/241/?tab=files", "https://github.com/example/query/pull/241"}} {
		a, err := Identity(tc.kind, tc.a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := Identity(tc.kind, tc.b)
		if err != nil || a != b {
			t.Fatalf("same source had different identities: %s %s %v", a, b, err)
		}
	}
}
