package intake

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

const owner = "aaaaaaaa-0000-4000-8000-000000000001"

// workspace serves the recorded Notion responses in testdata, with broken overriding any of them by request key.
func workspace(t *testing.T, broken map[string]int) (*Notion, *[]string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/notion/workspace.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret_test" || r.Header.Get("Notion-Version") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		key := r.Method + " " + r.URL.Path
		switch {
		case r.URL.Path == "/comments":
			key += " " + r.URL.Query().Get("block_id")
		case r.URL.Path == "/search":
			var body struct {
				Cursor string `json:"start_cursor"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			key += " " + body.Cursor
		}
		if c := r.URL.Query().Get("start_cursor"); c != "" {
			key += " " + c
		}
		key = strings.TrimSpace(key)
		calls = append(calls, key)
		if code, ok := broken[key]; ok {
			w.WriteHeader(code)
			w.Write([]byte(`{"object":"error","message":"boom"}`))
			return
		}
		body, ok := fixtures[key]
		if !ok {
			t.Errorf("no recorded response for %s", key)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var probe struct{ Status int }
		if json.Unmarshal(body, &probe) == nil && probe.Status != 0 {
			w.WriteHeader(probe.Status)
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	n := NewNotion("secret_test", owner)
	n.URL, n.Pace = srv.URL, 0
	n.Now = func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) }
	return n, &calls
}

func summary(asks []DocAsk) []string {
	var out []string
	for _, a := range asks {
		out = append(out, a.Key+" "+a.Kind+" "+a.Asker)
	}
	slices.Sort(out)
	return out
}

func TestNotionSweepFindsOnlyAsksAddressedToTheOwner(t *testing.T) {
	for _, tc := range []struct {
		name  string
		keep  []string
		want  []string
		pages int
	}{
		{"recent pages", nil, []string{
			"notion-review:a000000000004000800000000000000c review Notion",
			"notion:d0000000000040008000000000000001 comment Alice",
			"notion:d0000000000040008000000000000002 mention Alice",
			"notion:d0000000000040008000000000000006 reply Bob",
		}, 5},
		{"an old page with an open ask and a page no longer shared", []string{"a0000000-0000-4000-8000-00000000000f", "a0000000-0000-4000-8000-000000000099"}, []string{
			"notion-review:a000000000004000800000000000000c review Notion",
			"notion:d0000000000040008000000000000001 comment Alice",
			"notion:d0000000000040008000000000000002 mention Alice",
			"notion:d0000000000040008000000000000006 reply Bob",
			"notion:d0000000000040008000000000000008 mention Carol",
		}, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, _ := workspace(t, nil)
			asks, pages, err := n.Asks(context.Background(), tc.keep)
			if err != nil {
				t.Fatal(err)
			}
			if got := summary(asks); !slices.Equal(got, tc.want) || pages != tc.pages {
				t.Fatalf("asks\n%s\n%d pages; want\n%s\n%d pages", strings.Join(got, "\n"), pages, strings.Join(tc.want, "\n"), tc.pages)
			}
		})
	}
}

func TestNotionAsksCarryTheirThreadLinkAndWhatTheyMention(t *testing.T) {
	n, _ := workspace(t, nil)
	asks, _, err := n.Asks(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]DocAsk{}
	for _, a := range asks {
		byKey[a.Key] = a
	}
	mention := byKey["notion:d0000000000040008000000000000002"]
	if mention.URL != "https://www.notion.so/Launch-plan-a000000000004000800000000000000a?d=d0000000000040008000000000000002" ||
		mention.Title != "Launch plan" || mention.Text != "@Rajat is PR 7 the final cut?" || mention.Page != "a000000000004000800000000000000a" ||
		!strings.Contains(mention.Evidence, `on "Ship with https://github.com/acme/app/pull/7"`) ||
		!slices.Contains(mention.Refs, "https://github.com/acme/app/pull/7") {
		t.Fatalf("mention lost its source or evidence: %+v", mention)
	}
	reply := byKey["notion:d0000000000040008000000000000006"]
	if reply.Evidence != "you: Why the delay?\nBob: Vendor slipped a week" || !slices.Contains(reply.Refs, "a000000000004000800000000000000a") {
		t.Fatalf("reply evidence %q refs %q", reply.Evidence, reply.Refs)
	}
	if r := byKey["notion-review:a000000000004000800000000000000c"]; r.Text != "Reviewer lists you; status In review" {
		t.Fatalf("review ask %+v", r)
	}
	if v := byKey["notion:d0000000000040008000000000000001"].Version; v == "" {
		t.Fatal("ask has no version")
	}
}

func TestANotionSweepThatCannotReadEveryPageReturnsNothing(t *testing.T) {
	for _, key := range []string{
		"GET /comments b0000000-0000-4000-8000-0000000000b3",
		"POST /search cursor-2",
		"GET /comments a0000000-0000-4000-8000-00000000000a cc-2",
		"GET /databases/db000000-0000-4000-8000-0000000000db",
	} {
		t.Run(key, func(t *testing.T) {
			n, _ := workspace(t, map[string]int{key: http.StatusInternalServerError})
			asks, _, err := n.Asks(context.Background(), nil)
			if err == nil || asks != nil {
				t.Fatalf("partial sweep returned %d asks, err %v", len(asks), err)
			}
		})
	}
}

func TestPeopleFindsTheOwnerByEmail(t *testing.T) {
	n, _ := workspace(t, nil)
	people, err := n.People(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if people[owner] != "rajat@example.com\tRajat" || len(people) != 2 {
		t.Fatalf("people %q", people)
	}
	n.Token = "wrong"
	if _, err := n.People(context.Background()); err == nil || !strings.Contains(err.Error(), "rejected the token") {
		t.Fatalf("a bad token was accepted: %v", err)
	}
}
