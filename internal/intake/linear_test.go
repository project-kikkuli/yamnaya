package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestLinearSnapshotWalksPagesAndRefreshesCommentEvidence(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "secret" {
			t.Error("missing authorization")
		}
		var req struct {
			Query     string
			Variables linearVars
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		switch {
		case strings.Contains(req.Query, "YamnayaViewer"):
			fmt.Fprint(w, `{"data":{"viewer":{"id":"owner","name":"R","organization":{"id":"org","name":"O"}}}}`)
		case strings.Contains(req.Query, "YamnayaAssigned"):
			counts["assigned"]++
			if req.Variables.After == nil {
				fmt.Fprint(w, `{"data":{"issues":{"nodes":[{"id":"a","identifier":"ENG-1","updatedAt":"same","assignee":{"id":"owner"},"state":{"type":"started"}}],"pageInfo":{"hasNextPage":true,"endCursor":"page2"}}}}`)
			} else {
				fmt.Fprint(w, `{"data":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}`)
			}
		case strings.Contains(req.Query, "YamnayaComments"):
			counts["comments"]++
			if req.Variables.After == nil {
				fmt.Fprint(w, `{"data":{"issue":{"comments":{"nodes":[{"id":"c1","body":"one"}],"pageInfo":{"hasNextPage":true,"endCursor":"c2"}}}}}`)
			} else {
				fmt.Fprint(w, `{"data":{"issue":{"comments":{"nodes":[{"id":"c2","body":"two"}],"pageInfo":{"hasNextPage":false}}}}}`)
			}
		case strings.Contains(req.Query, "YamnayaRelations"):
			counts["relations"]++
			fmt.Fprint(w, `{"data":{"issue":{"relations":{"nodes":[{"id":"r","type":"blocks","issue":{"id":"a"},"relatedIssue":{"id":"b","identifier":"ENG-2"}}],"pageInfo":{"hasNextPage":false}}}}}`)
		default:
			t.Errorf("unexpected query %s", req.Query)
		}
	}))
	defer server.Close()
	client := NewLinear("secret")
	client.URL = server.URL
	client.Now = func() time.Time { return now }
	first, err := client.Snapshot(context.Background(), core.LinearSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Issues) != 1 || len(first.Issues[0].Comments) != 2 || len(first.Issues[0].Relations) != 2 || counts["assigned"] != 2 {
		t.Fatalf("incomplete pagination: %+v / %+v", first, counts)
	}
	if _, err = client.Snapshot(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if counts["comments"] != 2 {
		t.Fatal("unchanged recent evidence fetched repeatedly")
	}
	now = now.Add(16 * time.Minute)
	if _, err = client.Snapshot(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if counts["comments"] != 4 {
		t.Fatal("comments never refresh independently of issue update")
	}
}

func TestLinearRejectsPartialResponsesAndHonorsRateLimit(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{{"partial", `{"data":{"viewer":{"id":"owner"}},"errors":[{"message":"incomplete"}]}`, 200}, {"limited", `{}`, 429}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			now := time.Now()
			client := NewLinear("secret")
			client.URL = server.URL
			client.Now = func() time.Time { return now }
			if _, err := client.Identity(context.Background()); err == nil {
				t.Fatal("accepted incomplete source")
			}
			if tc.status == 429 {
				if _, err := client.Identity(context.Background()); err == nil {
					t.Fatal("ignored backoff")
				}
				if calls != 1 || !client.Next.Equal(now.Add(120*time.Second)) {
					t.Fatal("rate limit caused more requests")
				}
			}
		})
	}
}

func TestLinearReassignmentRetainsWatchedIssueAndUnavailableEvidence(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint(unavailable), func(t *testing.T) {
			now := time.Now()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct{ Query string }
				json.NewDecoder(r.Body).Decode(&req)
				switch {
				case strings.Contains(req.Query, "YamnayaViewer"):
					fmt.Fprint(w, `{"data":{"viewer":{"id":"owner","organization":{"id":"org"}}}}`)
				case strings.Contains(req.Query, "YamnayaAssigned"):
					fmt.Fprint(w, `{"data":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}`)
				case strings.Contains(req.Query, "YamnayaIssue"):
					if unavailable {
						fmt.Fprint(w, `{"data":{"issue":null}}`)
					} else {
						fmt.Fprint(w, `{"data":{"issue":{"id":"a","identifier":"ENG-1","updatedAt":"same","assignee":{"id":"other"},"state":{"type":"completed"}}}}`)
					}
				default:
					t.Errorf("unexpected query %s", req.Query)
				}
			}))
			defer server.Close()
			client := NewLinear("secret")
			client.URL = server.URL
			client.Now = func() time.Time { return now }
			previous := core.LinearSnapshot{OwnerID: "owner", WorkspaceID: "org", Issues: []core.LinearIssue{{ID: "a", Identifier: "ENG-1", UpdatedAt: "same", Assigned: true, Watched: true, HydratedAt: now, Comments: []core.LinearComment{{ID: "evidence"}}}}}
			got, err := client.Snapshot(context.Background(), previous)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Issues) != 1 || got.Issues[0].Assigned || !got.Issues[0].Watched || got.Issues[0].Unavailable != unavailable || len(got.Issues[0].Comments) != 1 {
				t.Fatalf("lost tracked evidence: %+v", got)
			}
		})
	}
}
