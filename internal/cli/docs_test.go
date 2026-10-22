package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/intake"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// recordedNotion serves the recorded workspace, with the responses in edits replacing the recorded ones, and appends each request
// it serves to calls when calls is not nil.
func recordedNotion(t *testing.T, edits map[string]string, calls *[]string) *httptest.Server {
	t.Helper()
	raw, err := os.ReadFile("../intake/testdata/notion/workspace.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		switch r.URL.Path {
		case "/comments":
			key += " " + r.URL.Query().Get("block_id")
		case "/search":
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
		if calls != nil {
			*calls = append(*calls, key)
		}
		body := fixtures[key]
		if e, ok := edits[key]; ok {
			body = json.RawMessage(e)
		}
		var probe struct{ Status int }
		if json.Unmarshal(body, &probe) == nil && probe.Status != 0 {
			w.WriteHeader(probe.Status)
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func docKinds(b core.Board) []string {
	var out []string
	for _, n := range b.Needs {
		if n.Doc != nil {
			out = append(out, n.Doc.Kind+" "+n.Doc.Asker)
		}
	}
	slices.Sort(out)
	return out
}

func TestConnectingNotionMakesServeQueueItsAsksUntilTheyResolve(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NOTION_TOKEN", "")
	t.Setenv("NOTION_OWNER_ID", "")
	t.Setenv("NOTION_REVIEWER_ID", "")
	srv := recordedNotion(t, nil, nil)
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	d := &docSweeper{dir: dir, api: srv.URL}
	if err := d.sweep(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if sweeps, _ := st.DocSweeps(); len(sweeps) != 0 {
		t.Fatalf("an unconnected Notion was swept: %+v", sweeps)
	}
	n := intake.NewNotion("secret_test", "")
	n.URL, n.Pace = srv.URL, 0
	if _, err := connectNotion(context.Background(), n, "nobody@example.com", dir); err == nil {
		t.Fatal("connected with an email no Notion person has")
	}
	cfg, err := connectNotion(context.Background(), n, "Rajat@Example.com", dir)
	if err != nil || cfg.OwnerName != "Rajat" {
		t.Fatalf("connect: %+v %v", cfg, err)
	}
	if info, err := os.Stat(filepath.Join(dir, notionFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved connection is not private: %v %v", info, err)
	}
	cycle := func() core.Board {
		if err := serveCycle(c); err != nil {
			t.Fatal(err)
		}
		b, err := c.Board()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if err := d.sweep(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	want := []string{"comment Alice", "mention Alice", "reply Bob", "review Notion"}
	if got := docKinds(cycle()); !slices.Equal(got, want) {
		t.Fatalf("owner queue holds %q, want %q", got, want)
	}
	for _, n := range cycle().Needs {
		if n.Doc != nil && n.Doc.Kind == "review" {
			if err := st.DismissQueue(n.Key()); err != nil {
				t.Fatal(err)
			}
		}
	}
	raw, err := os.ReadFile("../intake/testdata/notion/workspace.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	reviewEdit := `"last_edited_time": "2026-10-20T09:00:00.000Z"`
	if strings.Count(string(fixtures["POST /search"]), reviewEdit) != 1 {
		t.Fatal("the recorded review page's edit time is not unique")
	}
	var calls []string
	edited := recordedNotion(t, map[string]string{
		"POST /search": strings.Replace(string(fixtures["POST /search"]), reviewEdit, `"last_edited_time": "2026-10-21T11:00:00.000Z"`, 1),
	}, &calls)
	d.api, d.notion = edited.URL, nil
	if err := d.sweep(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if got := docKinds(cycle()); !slices.Equal(got, []string{"comment Alice", "mention Alice", "reply Bob"}) {
		t.Fatalf("an edit that left the review request as it was brought back its dismissed row: %q", got)
	}
	for _, unchanged := range []string{"a0000000-0000-4000-8000-00000000000d", "a0000000-0000-4000-8000-00000000000e"} {
		if slices.Contains(calls, "GET /blocks/"+unchanged+"/children") {
			t.Fatalf("a sweep reread page %s, unchanged and with no open ask: %q", unchanged, calls)
		}
	}
	resolved := recordedNotion(t, map[string]string{
		"GET /comments b0000000-0000-4000-8000-0000000000b1": `{"object":"list","results":[],"has_more":false,"next_cursor":null}`,
	}, nil)
	d.api, d.notion = resolved.URL, nil
	if err := d.sweep(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if got := docKinds(cycle()); !slices.Equal(got, []string{"comment Alice", "reply Bob"}) {
		t.Fatalf("after Alice's mention was resolved in Notion the queue holds %q", got)
	}
	for _, tc := range []struct {
		key, report string
	}{
		{"GET /comments a0000000-0000-4000-8000-00000000000b", "1 of 5 pages unreadable"},
		{"POST /search cursor-2", "HTTP 502"},
	} {
		broken := recordedNotion(t, map[string]string{tc.key: `{"object":"error","status":502,"message":"bad gateway"}`,
			"GET /comments b0000000-0000-4000-8000-0000000000b1": `{"object":"list","results":[],"has_more":false,"next_cursor":null}`}, nil)
		d.api, d.notion = broken.URL, nil
		if err := d.sweep(context.Background(), st); err != nil {
			t.Fatal(err)
		}
		if got := docKinds(cycle()); !slices.Equal(got, []string{"comment Alice", "reply Bob"}) {
			t.Fatalf("%s: a page or listing Notion could not read changed the queue: %q", tc.key, got)
		}
		if err := docSweepErrors(st); err == nil || !strings.Contains(err.Error(), tc.report) {
			t.Fatalf("%s: the failure is not reported: %v", tc.key, err)
		}
	}
}
