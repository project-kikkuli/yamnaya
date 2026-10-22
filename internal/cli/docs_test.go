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
	"time"

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

func recordedNow() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) }

func docKinds(b core.Board) []string {
	var out []string
	for _, n := range b.Needs {
		for _, d := range n.Docs {
			out = append(out, d.Kind+" "+d.Asker)
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
	d := &docSweeper{dir: dir, api: srv.URL, now: recordedNow}
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
			if err := st.DismissQueue(n.DismissKeys()...); err != nil {
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
	for _, unchanged := range []string{"a0000000-0000-4000-8000-00000000000a", "a0000000-0000-4000-8000-00000000000b",
		"a0000000-0000-4000-8000-00000000000d", "a0000000-0000-4000-8000-00000000000e"} {
		if slices.Contains(calls, "GET /blocks/"+unchanged+"/children") {
			t.Fatalf("a sweep reread the blocks of page %s, whose edit and comments had not changed: %q", unchanged, calls)
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

func notionComment(id, discussion, by, text, at string, mention string) string {
	parts := `{"type":"text","text":{"content":"` + text + `"},"plain_text":"` + text + `"}`
	if mention != "" {
		parts = `{"type":"mention","mention":{"type":"user","user":{"object":"user","id":"` + mention + `"}},"plain_text":"@Rajat"},` + parts
	}
	return `{"object":"comment","id":"` + id + `","parent":{"type":"page_id","page_id":"a0000000-0000-4000-8000-00000000000e"},` +
		`"discussion_id":"` + discussion + `","created_time":"` + at + `","last_edited_time":"` + at + `",` +
		`"created_by":{"object":"user","id":"` + by + `"},"rich_text":[` + parts + `]}`
}

func TestANewCommentOnAnUneditedPageTheOwnerIsInReachesTheQueueAtTheNextRecheck(t *testing.T) {
	const (
		owner, carol = "aaaaaaaa-0000-4000-8000-000000000001", "aaaaaaaa-0000-4000-8000-000000000004"
		signed       = "a0000000-0000-4000-8000-00000000000e"
		thread       = "d0000000-0000-4000-8000-0000000000e1"
	)
	dir := t.TempDir()
	t.Setenv("NOTION_TOKEN", "secret_test")
	t.Setenv("NOTION_OWNER_ID", owner)
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	answered := notionComment("c0000000-0000-4000-8000-0000000000e1", thread, carol, "Is this final?", "2026-10-19T09:00:00.000Z", "") + "," +
		notionComment("c0000000-0000-4000-8000-0000000000e2", thread, owner, "Yes", "2026-10-19T10:00:00.000Z", "")
	list := func(comments string) string {
		return `{"object":"list","results":[` + comments + `],"has_more":false,"next_cursor":null}`
	}
	raw, err := os.ReadFile("../intake/testdata/notion/workspace.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	var search struct{ Results []json.RawMessage }
	if err = json.Unmarshal(fixtures["POST /search cursor-2"], &search); err != nil {
		t.Fatal(err)
	}
	page := ""
	for _, r := range search.Results {
		if strings.Contains(string(r), `"id": "`+signed+`"`) {
			page = string(r)
		}
	}
	if page == "" {
		t.Fatal("the recorded search has no signed page")
	}
	before := map[string]string{"GET /comments " + signed: list(answered), "GET /pages/" + intake.NotionKey(signed): page}
	after := map[string]string{"GET /pages/" + intake.NotionKey(signed): page, "GET /comments " + signed: list(answered + "," +
		notionComment("c0000000-0000-4000-8000-0000000000e3", thread, carol, " one more question", "2026-10-22T09:00:00.000Z", owner))}
	queued := func() []string {
		if err := serveCycle(c); err != nil {
			t.Fatal(err)
		}
		b, err := c.Board()
		if err != nil {
			t.Fatal(err)
		}
		return docKinds(b)
	}
	base := []string{"comment Alice", "mention Alice", "reply Bob", "review Notion"}
	d := &docSweeper{dir: dir, recheck: time.Hour, now: recordedNow}
	for _, step := range []struct {
		name      string
		edits     map[string]string
		recheck   bool
		want      []string
		reread    bool
		commented bool
	}{
		{"first sweep reads every page", before, false, base, true, true},
		{"recheck finds the comments unchanged", before, true, base, false, true},
		{"a new comment before the recheck is due", after, false, base, false, false},
		{"the recheck finds the new comment", after, true, append([]string{"mention Carol"}, base...), true, true},
	} {
		var calls []string
		srv := recordedNotion(t, step.edits, &calls)
		d.api, d.notion = srv.URL, nil
		if step.recheck {
			d.checked = time.Time{}
		}
		if err := d.sweep(context.Background(), st); err != nil {
			t.Fatalf("%s: %v after %q", step.name, err, calls)
		}
		got := queued()
		slices.Sort(got)
		want := append([]string{}, step.want...)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("%s: queue %q, want %q", step.name, got, want)
		}
		if r := slices.Contains(calls, "GET /blocks/"+signed+"/children"); r != step.reread {
			t.Fatalf("%s: page reread %v, want %v: %q", step.name, r, step.reread, calls)
		}
		if r := slices.Contains(calls, "GET /comments "+signed); r != step.commented {
			t.Fatalf("%s: comments read %v, want %v", step.name, r, step.commented)
		}
	}
}

func TestTheRecheckSkipsOwnerPagesNotEditedWithinTheWindow(t *testing.T) {
	const (
		owner, carol = "aaaaaaaa-0000-4000-8000-000000000001", "aaaaaaaa-0000-4000-8000-000000000004"
		signed       = "a0000000-0000-4000-8000-00000000000e"
		thread       = "d0000000-0000-4000-8000-0000000000e1"
	)
	dir := t.TempDir()
	t.Setenv("NOTION_TOKEN", "secret_test")
	t.Setenv("NOTION_OWNER_ID", owner)
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	page := `{"object":"page","id":"` + signed + `","created_time":"2026-09-01T00:00:00.000Z","last_edited_time":"2026-09-15T00:00:00.000Z",` +
		`"created_by":{"object":"user","id":"aaaaaaaa-0000-4000-8000-000000000003"},"parent":{"type":"workspace","workspace":true},` +
		`"archived":false,"in_trash":false,"properties":{"Name":{"id":"title","type":"title","title":[{"plain_text":"Old spec"}]}},` +
		`"url":"https://www.notion.so/Old-spec-a000000000004000800000000000000e"}`
	comments := `{"object":"list","results":[` +
		notionComment("c0000000-0000-4000-8000-0000000000e1", thread, carol, "Is this final?", "2026-09-15T00:00:00.000Z", "") + "," +
		notionComment("c0000000-0000-4000-8000-0000000000e2", thread, owner, "Yes", "2026-09-15T01:00:00.000Z", "") + `],"has_more":false,"next_cursor":null}`
	edits := map[string]string{
		"POST /search":                           `{"object":"list","results":[` + page + `],"has_more":false,"next_cursor":null}`,
		"GET /comments " + signed:                comments,
		"GET /pages/" + intake.NotionKey(signed): page,
	}
	d := &docSweeper{dir: dir, recheck: time.Hour}
	for _, step := range []struct {
		name    string
		now     time.Time
		checked bool
	}{
		{"the page is read while edited within the window", time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), true},
		{"the recheck reads its comments while it is within the window", time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), true},
		{"the recheck leaves it once its last edit falls outside the window", time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), false},
	} {
		var calls []string
		srv := recordedNotion(t, edits, &calls)
		now := step.now
		d.api, d.notion, d.now, d.checked = srv.URL, nil, func() time.Time { return now }, time.Time{}
		if err := d.sweep(context.Background(), st); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got := slices.Contains(calls, "GET /comments "+signed); got != step.checked {
			t.Fatalf("%s: comments read %v, want %v: %q", step.name, got, step.checked, calls)
		}
	}
}

func TestAnOpenAskOnAPageEditedBeforeTheWindowClosesWhenItsThreadResolvesOrAges(t *testing.T) {
	const (
		owner, carol = "aaaaaaaa-0000-4000-8000-000000000001", "aaaaaaaa-0000-4000-8000-000000000004"
		old          = "a0000000-0000-4000-8000-00000000000e"
		thread       = "d0000000-0000-4000-8000-0000000000e1"
	)
	t.Setenv("NOTION_TOKEN", "secret_test")
	t.Setenv("NOTION_OWNER_ID", owner)
	page := `{"object":"page","id":"` + old + `","created_time":"2026-09-01T00:00:00.000Z","last_edited_time":"2026-09-25T00:00:00.000Z",` +
		`"created_by":{"object":"user","id":"aaaaaaaa-0000-4000-8000-000000000003"},"parent":{"type":"workspace","workspace":true},` +
		`"archived":false,"in_trash":false,"properties":{"Name":{"id":"title","type":"title","title":[{"plain_text":"Old spec"}]}},` +
		`"url":"https://www.notion.so/Old-spec-a000000000004000800000000000000e"}`
	open := `{"object":"list","results":[` +
		notionComment("c0000000-0000-4000-8000-0000000000e1", thread, owner, "Is this final?", "2026-09-25T00:00:00.000Z", "") + "," +
		notionComment("c0000000-0000-4000-8000-0000000000e2", thread, carol, "Not yet, see section 3", "2026-09-26T00:00:00.000Z", "") +
		`],"has_more":false,"next_cursor":null}`
	resolved := `{"object":"list","results":[],"has_more":false,"next_cursor":null}`
	type step struct {
		comments string
		now      time.Time
		want     int
	}
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }
	for _, tc := range []struct {
		name  string
		steps []step
	}{
		{"Carol resolves the thread after the page's edit left the window", []step{{open, day(9, 27), 1}, {resolved, day(10, 20), 0}}},
		{"the ask closes once Carol's comment leaves the window, the thread unchanged", []step{{open, day(9, 27), 1}, {open, day(10, 15), 1}, {open, day(10, 20), 0}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			d := &docSweeper{dir: dir, recheck: time.Hour}
			for i, s := range tc.steps {
				srv := recordedNotion(t, map[string]string{
					"POST /search":                        `{"object":"list","results":[` + page + `],"has_more":false,"next_cursor":null}`,
					"GET /pages/" + old:                   page,
					"GET /pages/" + intake.NotionKey(old): page,
					"GET /comments " + old:                s.comments,
				}, nil)
				now := s.now
				d.api, d.notion, d.now, d.checked = srv.URL, nil, func() time.Time { return now }, time.Time{}
				if err := d.sweep(context.Background(), st); err != nil {
					t.Fatalf("step %d: %v", i, err)
				}
				if asks, err := st.OpenDocAsks(); err != nil || len(asks) != s.want {
					t.Fatalf("step %d: %d open asks, want %d (%v)", i, len(asks), s.want, err)
				}
			}
		})
	}
}
