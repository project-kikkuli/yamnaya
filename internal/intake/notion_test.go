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

// sweep reads every page the integration lists, as one sweep does on first connecting.
func sweep(n *Notion, keep []string) ([]DocAsk, int, error) {
	pages, err := n.Pages(context.Background(), keep)
	if err != nil {
		return nil, 0, err
	}
	var asks []DocAsk
	for _, p := range pages {
		got, err := n.PageAsks(context.Background(), p)
		if err != nil {
			return asks, len(pages), err
		}
		asks = append(asks, got...)
	}
	return asks, len(pages), nil
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
		}, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, _ := workspace(t, nil)
			asks, pages, err := sweep(n, tc.keep)
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
	asks, _, err := sweep(n, nil)
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

func TestAPageNotionCannotReadFailsAloneAndAListingThatCannotFinishFailsWhole(t *testing.T) {
	for _, tc := range []struct {
		key   string
		pages []string
	}{
		{"GET /comments b0000000-0000-4000-8000-0000000000b3", []string{"a000000000004000800000000000000a"}},
		{"GET /comments a0000000-0000-4000-8000-00000000000a cc-2", []string{"a000000000004000800000000000000a"}},
		{"GET /databases/db000000-0000-4000-8000-0000000000db", []string{"a000000000004000800000000000000c", "a000000000004000800000000000000d"}},
		{"POST /search cursor-2", nil},
	} {
		t.Run(tc.key, func(t *testing.T) {
			n, _ := workspace(t, map[string]int{tc.key: http.StatusInternalServerError})
			pages, err := n.Pages(context.Background(), nil)
			if tc.pages == nil {
				if err == nil || pages != nil {
					t.Fatalf("a listing that failed part way returned %d pages, err %v", len(pages), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range pages {
				asks, err := n.PageAsks(context.Background(), p)
				if broken := slices.Contains(tc.pages, p.Key); broken != (err != nil) || broken && asks != nil {
					t.Fatalf("page %s: %d asks, err %v", p.Key, len(asks), err)
				}
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

func TestAPageIsWatchedWhenItIsTiedToTheOwner(t *testing.T) {
	n, _ := workspace(t, nil)
	pages, err := n.Pages(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	watched := map[string]bool{}
	for _, p := range pages {
		r, err := n.ReadPage(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		watched[p.Key] = r.Watch
		if !slices.Contains(r.Blocks, "a0000000-0000-4000-8000-0000000000"+p.Key[len(p.Key)-2:]) {
			t.Fatalf("page %s read without its own block: %q", p.Key, r.Blocks)
		}
	}
	for key, why := range map[string]string{
		"a000000000004000800000000000000a": "the owner created it",
		"a000000000004000800000000000000b": "the owner writes in its threads",
		"a000000000004000800000000000000c": "it lists the owner as reviewer",
		"a000000000004000800000000000000e": "it lists the owner as approver",
	} {
		if !watched[key] {
			t.Errorf("page %s is not watched though %s", key, why)
		}
	}
}
