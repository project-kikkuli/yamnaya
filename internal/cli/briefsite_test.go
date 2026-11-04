package cli

import (
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/spf13/cobra"
)

func TestBriefPagesShowEachVersionAndRollReportsUpUnderTheirChief(t *testing.T) {
	dir := t.TempDir()
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	g, m, err := c.NewGoal("ship the importer", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	chief, err := c.NewChief("data", "", dir, "", []string{m.ID})
	if err != nil {
		t.Fatal(err)
	}
	for id, session := range map[string]string{m.ID: "manager-session", chief.ID: "chief-session"} {
		mg, _ := st.Manager(id)
		mg.Session = session
		if err := st.SaveManager(mg); err != nil {
			t.Fatal(err)
		}
	}
	write := func(session, assessment string) {
		t.Helper()
		if _, err := c.WriteBrief(session, core.Brief{Assessment: assessment, Changed: "x", Uncertainty: "none", Next: "next of " + assessment, WaitingOn: "CI"}); err != nil {
			t.Fatal(err)
		}
	}
	write("manager-session", "parser drafted")
	write("manager-session", "parser merged")
	write("chief-session", "data work on track")
	if _, err := c.SteerContext(core.Board{}, g.ID, "CSV only", core.Owner); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&briefSite{core: c})
	defer srv.Close()
	get := func(path string) string {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, res.StatusCode, body)
		}
		return string(body)
	}
	for _, tc := range []struct {
		path      string
		want, not []string
	}{
		{"/", []string{"chief data", "ship the importer", "parser merged", "steer r1 not adopted"}, nil},
		{"/b/" + g.ID, []string{"parser merged", "Brief v2", "CSV only", "not adopted yet", "?v=1"}, []string{"parser drafted"}},
		{"/b/" + m.ID + "?v=1", []string{"parser drafted", "Brief v1"}, []string{"parser merged"}},
		{"/b/" + c.SelfName() + "/" + m.ID, []string{"parser merged", "Brief v2"}, nil},
		{"/b/" + chief.ID, []string{"data work on track", "Reports", "parser merged", "next of parser merged"}, nil},
	} {
		t.Run(tc.path, func(t *testing.T) {
			body := get(tc.path)
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("page lacks %q", w)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(body, n) {
					t.Errorf("page shows %q", n)
				}
			}
		})
	}
	if res, _ := http.Get(srv.URL + "/b/goal99"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown goal answered %d, want 404", res.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/b/"+g.ID, nil)
	req.Host = "rebind.example.com"
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("a request for a foreign name answered %v, %v; want 403", res, err)
	}
}

func TestBriefSiteAnswersOnlyRequestsToAnAddress(t *testing.T) {
	dir := t.TempDir()
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site := &briefSite{core: &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}}
	for _, tc := range []struct {
		host string
		want int
	}{
		{"127.0.0.1:7477", http.StatusOK},
		{"localhost:7477", http.StatusOK},
		{"LOCALHOST", http.StatusOK},
		{"[::1]:7477", http.StatusOK},
		{"100.64.0.7:7477", http.StatusOK},
		{"rebind.example.com:7477", http.StatusForbidden},
		{"localhost.example.com", http.StatusForbidden},
		{"127.0.0.1.nip.io:7477", http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = tc.host
		rec := httptest.NewRecorder()
		site.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("Host %s: status %d, want %d", tc.host, rec.Code, tc.want)
		}
	}
}

func TestARemoteGoalsBriefOpensFromItsHostsSnapshotOrTheLastOneRead(t *testing.T) {
	far := t.TempDir()
	fst, err := core.OpenStore(filepath.Join(far, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fst.Close()
	fc := &core.Core{Store: fst, StateDir: far, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	g, m, err := fc.NewGoal("cut CI to five minutes", far, "")
	if err != nil {
		t.Fatal(err)
	}
	m.Session = "far-manager"
	if err := fst.SaveManager(m); err != nil {
		t.Fatal(err)
	}
	if _, err := fc.WriteBrief("far-manager", core.Brief{Assessment: "caesar chain is the long pole", Changed: "x", Uncertainty: "none", Next: "split shards", WaitingOn: "CI"}); err != nil {
		t.Fatal(err)
	}
	b, err := fc.Board()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSnapshot(fc, far, b); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := os.ReadFile(snapshotPath(far))
	ssh := filepath.Join(t.TempDir(), "ssh")
	answer := func(script string) {
		if err := os.WriteFile(ssh, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("YAMNAYA_SSH_BIN", ssh)
	near := t.TempDir()
	st, err := core.OpenStore(filepath.Join(near, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.RecordHost("box", time.Now()); err != nil {
		t.Fatal(err)
	}
	site := &briefSite{core: &core.Core{Store: st, StateDir: near, Procs: func() ([]proc.Proc, error) { return nil, nil }}}
	for _, c := range []struct {
		name, ssh, path string
		kept            bool
		code            int
		want            []string
	}{
		{"host answers, by goal", "exec cat " + snapshotPath(far), "/b/box/" + g.ID, false, 200, []string{"caesar chain is the long pole", "On box, as its ym serve saw it just now."}},
		{"host answers, by manager", "exec cat " + snapshotPath(far), "/b/box/" + m.ID, false, 200, []string{"caesar chain is the long pole"}},
		{"host down, last read kept", "exit 255", "/b/box/" + g.ID, true, 200, []string{"caesar chain is the long pole", "box: no answer"}},
		{"no such goal there", "exec cat " + snapshotPath(far), "/b/box/goal99", false, 404, nil},
		{"host ym never reached", "exec cat " + snapshotPath(far), "/b/elsewhere/" + g.ID, false, 404, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			answer(c.ssh)
			if c.kept {
				if err := st.KeepBoard("box", time.Now().Add(-3*time.Minute), snapshot); err != nil {
					t.Fatal(err)
				}
			}
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			req.Host = "127.0.0.1:7477"
			rec := httptest.NewRecorder()
			site.ServeHTTP(rec, req)
			body, _ := io.ReadAll(rec.Body)
			if rec.Code != c.code {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.code, body)
			}
			for _, w := range c.want {
				if !strings.Contains(string(body), w) {
					t.Errorf("page lacks %q:\n%s", w, body)
				}
			}
		})
	}
}

func TestTheFrontPageShowsTheQueueAndChiefsWhoseParentIsOnAnotherHost(t *testing.T) {
	dir := t.TempDir()
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	chief, err := c.NewChief("data", "", dir, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	chief.ParentID = "otherhost/mgr51"
	if err := st.SaveManager(chief); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddItem(core.Item{Kind: core.KindGo, Scope: dir, Question: "build it?", DoLine: "build the queue", Session: "s", Recipient: core.Owner}); err != nil {
		t.Fatal(err)
	}
	if err := st.SyncReviews([]core.ReviewRequest{{URL: "https://github.com/acme/vibe-apps/pull/3384", Updated: "2026-11-01T00:00:00Z", Title: "payer groups", Author: "teammate"}}); err != nil {
		t.Fatal(err)
	}
	site := &briefSite{core: c}
	b, err := c.Board()
	if err != nil {
		t.Fatal(err)
	}
	site.observe(b)
	srv := httptest.NewServer(site)
	defer srv.Close()
	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	body := html.UnescapeString(string(raw))
	if strings.Contains(body, "No goals") || !strings.Contains(body, "chief data") {
		t.Errorf("a chief under another host's chief is missing:\n%s", body)
	}
	for i, n := range b.Needs {
		line, _ := core.RowLine(n, core.RankLabels(len(b.Needs))[i], core.QueueWidth, time.Now())
		if line = strings.TrimRight(line, "✓▸"); !strings.Contains(body, line) {
			t.Errorf("page lacks the row the board draws, %q:\n%s", line, body)
		}
	}
	if len(b.Needs) != 2 || !strings.Contains(body, "NEXT 2") || !strings.Contains(body, "ym item go") || !strings.Contains(body, "vibe-apps#3384") {
		t.Errorf("queue missing (%d needs):\n%s", len(b.Needs), body)
	}
}

type siteMerger struct {
	merged []string
}

func (f *siteMerger) PRStatus(string) (core.PRStatus, error) { return core.PRStatus{}, nil }

func (f *siteMerger) Merge(url, head string) error {
	f.merged = append(f.merged, url+"@"+head)
	return nil
}

// doWorld is a site with one go item, one of the owner's pull requests and one teammate's review request, on fakes only.
type doWorld struct {
	c      *core.Core
	site   *briefSite
	srv    *httptest.Server
	merger *siteMerger
	item   core.Item
}

const (
	ownPR  = "https://github.com/acme/repo/pull/1"
	matePR = "https://github.com/acme/repo/pull/2"
)

func newDoWorld(t *testing.T) *doWorld {
	t.Helper()
	dir := t.TempDir()
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	w := &doWorld{merger: &siteMerger{}}
	w.c = &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }, PRs: w.merger}
	if w.item, err = st.AddItem(core.Item{Kind: core.KindGo, Scope: dir, Question: "build it?", DoLine: "build the queue", Rec: "yes", Session: "s", Recipient: core.Owner}); err != nil {
		t.Fatal(err)
	}
	if err := st.SyncAuthored(map[string]string{ownPR: "mine"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePRStatus(ownPR, ownStatus("head1")); err != nil {
		t.Fatal(err)
	}
	if err := st.SyncReviews([]core.ReviewRequest{{URL: matePR, Updated: "2026-11-01T00:00:00Z", Title: "theirs", Author: "teammate"}}); err != nil {
		t.Fatal(err)
	}
	w.site = &briefSite{core: w.c}
	w.observe(t)
	w.srv = httptest.NewServer(w.site)
	t.Cleanup(w.srv.Close)
	return w
}

func ownStatus(head string) core.PRStatus {
	return core.PRStatus{State: "OPEN", Title: "mine", Review: "APPROVED", Passed: 1, Fetched: time.Now(),
		Merge: core.PRMergeFacts{Author: "me", Viewer: "me", OID: head, Mergeable: "MERGEABLE", MergeState: "CLEAN"}}
}

func (w *doWorld) observe(t *testing.T) core.Board {
	t.Helper()
	b, err := w.c.Board()
	if err != nil {
		t.Fatal(err)
	}
	w.site.observe(b)
	return b
}

func (w *doWorld) page(t *testing.T) string {
	t.Helper()
	res, err := http.Get(w.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return string(raw)
}

// action is the form the page renders for the row whose text holds has: its id and token.
func (w *doWorld) action(t *testing.T, has string) (id, token string) {
	t.Helper()
	for _, row := range strings.Split(w.page(t), "<details")[1:] {
		if !strings.Contains(row, has) {
			continue
		}
		m := regexp.MustCompile(`action="/do/([0-9a-f]+)"><input type="hidden" name="token" value="([0-9a-f]+)"`).FindStringSubmatch(row)
		if m == nil {
			t.Fatalf("row %q has no form:\n%s", has, row)
		}
		return m[1], m[2]
	}
	t.Fatalf("no row %q", has)
	return "", ""
}

func (w *doWorld) post(t *testing.T, id, token, origin string) int {
	t.Helper()
	return w.postTo(t, "/do/", id, url.Values{"token": {token}}, origin)
}

func (w *doWorld) postTo(t *testing.T, route, id string, form url.Values, origin string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, w.srv.URL+route+id, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestThePagesCheckMarkActsOnlyOnTheRowTheOwnerRead(t *testing.T) {
	for _, tc := range []struct {
		name, row string
		// between changes the world after the page rendered.
		between func(t *testing.T, w *doWorld)
		token   func(string) string
		origin  func(string) string
		want    int
		// did says whether the item was answered and which head merged.
		answered bool
		merged   []string
	}{
		{name: "a go item goes ahead", row: "build the queue", want: http.StatusSeeOther, answered: true},
		{name: "my pull request merges at the head I read", row: "repo#1", want: http.StatusSeeOther, merged: []string{ownPR + "@head1"}},
		{name: "a wrong token does nothing", row: "build the queue", token: func(string) string { return "nope" }, want: http.StatusForbidden},
		{name: "another site does nothing", row: "repo#1", origin: func(string) string { return "https://evil.example" }, want: http.StatusForbidden},
		{name: "no origin does nothing", row: "repo#1", origin: func(string) string { return "" }, want: http.StatusForbidden},
		{name: "a new head since the page does nothing", row: "repo#1", want: http.StatusConflict, between: func(t *testing.T, w *doWorld) {
			if err := w.c.Store.SavePRStatus(ownPR, ownStatus("head2")); err != nil {
				t.Fatal(err)
			}
			w.observe(t)
		}},
		{name: "a new do-line since the page does nothing", row: "build the queue", want: http.StatusConflict, between: func(t *testing.T, w *doWorld) {
			if _, err := w.c.Prepare(w.observe(t), w.item.Key(), core.Prepared{Do: "build something else"}); err != nil {
				t.Fatal(err)
			}
			w.observe(t)
		}},
		{name: "a row reranked since the page still acts on itself", row: "build the queue", want: http.StatusSeeOther, answered: true, between: func(t *testing.T, w *doWorld) {
			if _, err := w.c.Store.RankRow("pr:"+ownPR, "top", true); err != nil {
				t.Fatal(err)
			}
			w.observe(t)
		}},
		{name: "an item answered elsewhere does nothing more", row: "build the queue", want: http.StatusBadGateway, answered: true, between: func(t *testing.T, w *doWorld) {
			if _, err := w.c.Store.Transition(w.item.Key(), core.ItemAnswered, core.GoAhead); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newDoWorld(t)
			id, token := w.action(t, tc.row)
			origin := w.srv.URL
			if tc.between != nil {
				tc.between(t, w)
			}
			if tc.token != nil {
				token = tc.token(token)
			}
			if tc.origin != nil {
				origin = tc.origin(origin)
			}
			if got := w.post(t, id, token, origin); got != tc.want {
				t.Fatalf("POST answered %d, want %d", got, tc.want)
			}
			it, err := w.c.Store.Item(w.item.Key())
			if err != nil {
				t.Fatal(err)
			}
			if (it.State == core.ItemAnswered) != tc.answered {
				t.Fatalf("item is %s, want answered %v", it.State, tc.answered)
			}
			if !slices.Equal(w.merger.merged, tc.merged) {
				t.Fatalf("merged %v, want %v", w.merger.merged, tc.merged)
			}
			if tc.want == http.StatusSeeOther {
				if body := w.page(t); strings.Contains(body, "do/"+id) {
					t.Fatalf("the row acted on is still offered:\n%s", body)
				}
				if got := w.post(t, id, token, origin); got != http.StatusConflict {
					t.Fatalf("a second tap answered %d", got)
				}
			}
		})
	}
}

func TestAPageOffersNoActionOnATeammatesPullRequestAndGetDoesNothing(t *testing.T) {
	w := newDoWorld(t)
	for _, row := range strings.Split(w.page(t), "<details")[1:] {
		if !strings.Contains(row, "repo#2") {
			continue
		}
		for _, route := range []string{"/do/", "/accept/", "/reply/", "/done/"} {
			if strings.Contains(row, route) {
				t.Fatalf("a teammate's row offers %s:\n%s", route, row)
			}
		}
	}
	id, _ := w.action(t, "repo#1")
	res, err := http.Get(w.srv.URL + "/do/" + id)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed || len(w.merger.merged) != 0 {
		t.Fatalf("GET answered %d, merged %v", res.StatusCode, w.merger.merged)
	}
	if got := w.post(t, core.Need{Review: &core.ReviewRequest{URL: matePR}}.RowID(), "", w.srv.URL); got != http.StatusForbidden {
		t.Fatalf("POST without a token answered %d", got)
	}
}

func TestTheFormsOriginMustBeThisSitesHostAndPort(t *testing.T) {
	for _, tc := range []struct {
		name, public, host, origin string
		want                       bool
	}{
		{"the address it reached", "http://127.0.0.1:7477/", "127.0.0.1:7477", "http://127.0.0.1:7477", true},
		{"a tunnel's port, as the phone's browser reached it", "http://127.0.0.1:7477/", "localhost:9000", "http://localhost:9000", true},
		{"a proxy keeping the host", "https://ym.example/ym/", "ym.example", "https://ym.example", true},
		{"a proxy rewriting the host to the listener", "https://ym.example/ym/", "127.0.0.1:7477", "https://ym.example", true},
		{"a proxy naming its port", "https://ym.example:8443/ym/", "127.0.0.1:7477", "https://ym.example:8443", true},
		{"a host header with the default port", "https://ym.example/", "ym.example:443", "https://ym.example", true},
		{"another port on this address", "http://127.0.0.1:7477/", "127.0.0.1:7477", "http://127.0.0.1:9999", false},
		{"another port on the public host", "https://ym.example/", "127.0.0.1:7477", "https://ym.example:9999", false},
		{"another scheme's default port on the public host", "https://ym.example/", "127.0.0.1:7477", "http://ym.example", false},
		{"another site", "https://ym.example/", "ym.example", "https://evil.example", false},
		{"no origin", "https://ym.example/", "ym.example", "", false},
		{"an opaque origin", "https://ym.example/", "ym.example", "null", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/do/x", nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := (&briefSite{public: tc.public}).sameOrigin(r); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestARowActedOnReturnsToThePageWhenReopened(t *testing.T) {
	w := newDoWorld(t)
	id, token := w.action(t, "build the queue")
	if got := w.post(t, id, token, w.srv.URL); got != http.StatusSeeOther {
		t.Fatalf("POST answered %d", got)
	}
	if strings.Contains(w.page(t), "do/"+id) {
		t.Fatal("the row acted on is still offered")
	}
	w.observe(t)
	if strings.Contains(w.page(t), "do/"+id) {
		t.Fatal("an answered row came back")
	}
	if _, err := w.c.Reopen(w.item.Key()); err != nil {
		t.Fatal(err)
	}
	w.observe(t)
	if !strings.Contains(w.page(t), "do/"+id) {
		t.Fatal("a reopened row stays hidden")
	}
}

func TestTheCliAndPageDrawTheSameRowAndTheButtonNamesWhatItDoes(t *testing.T) {
	for _, tc := range []struct {
		name, do string
		button   string
		mark     string
	}{
		{"a do-line that answers names itself", "Rotate the 3 exposed credsok", "Rotate the 3 exposed credsok", "✓"},
		{"a do-line that asks shows the arrow and the rec's button", "Pick how the proof is caught", "accept rec", "▸"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newDoWorld(t)
			_, err := w.c.Store.AddItem(core.Item{Kind: core.KindDecision, Scope: w.item.Scope, Question: "q", DoLine: tc.do, Rec: "the rec", Session: "another-session"})
			if err != nil {
				t.Fatal(err)
			}
			w.observe(t)
			cmd := itemQueueCmd(func(run runFunc) func(*cobra.Command, []string) error {
				return func(cmd *cobra.Command, args []string) error { return run(&env{core: w.c}, cmd, args) }
			})
			var out strings.Builder
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"--width", "40"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			var cli string
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.Contains(line, tc.do) {
					cli = line
				}
			}
			if cli == "" || !strings.HasSuffix(cli, tc.mark) {
				t.Fatalf("ym item queue row %q of:\n%s", cli, out.String())
			}
			page := w.page(t)
			var row string
			for _, r := range strings.Split(page, "<details")[1:] {
				if strings.Contains(r, tc.do) {
					row = r
				}
			}
			line := regexp.MustCompile(`<span class="line">([^<]*)</span>`).FindStringSubmatch(row)
			if line == nil || html.UnescapeString(line[1])+tc.mark != cli {
				t.Fatalf("page row %q, cli row %q", line, cli)
			}
			if !strings.Contains(row, `class="act" type="submit">accept rec</button>`) {
				t.Fatalf("row %s has no accept rec button", row)
			}
			if tc.mark == "✓" && !strings.Contains(row, `aria-label="`+tc.button+`"`) {
				t.Fatalf("check mark does not name %q: %s", tc.button, row)
			}
			if strings.Contains(row, `class="check"`) != (tc.mark == "✓") {
				t.Fatalf("mark %s disagrees with the page's button: %s", tc.mark, row)
			}
		})
	}
}

func (w *doWorld) rowOf(t *testing.T, has string) string {
	t.Helper()
	for _, row := range strings.Split(w.page(t), "<details")[1:] {
		if strings.Contains(row, has) {
			return row
		}
	}
	t.Fatalf("no row %q", has)
	return ""
}

func TestAnOpenedRowsFormsActThroughTheSamePathsAndGuards(t *testing.T) {
	const chore, decision = "Wake the laptop", "Use the new store"
	for _, tc := range []struct {
		name, row, route string
		form             url.Values
		origin           string
		badToken         bool
		stale            func(t *testing.T, w *doWorld, it core.Item)
		want             int
		state, answer    string
		snoozed, pr      bool
	}{
		{name: "reply answers the item", row: chore, route: "/reply/", form: url.Values{"text": {"it is up"}}, want: http.StatusSeeOther, state: core.ItemAnswered, answer: "it is up"},
		{name: "a reply needs words", row: chore, route: "/reply/", form: url.Values{"text": {"  "}}, want: http.StatusBadRequest, state: core.ItemOpen},
		{name: "done answers the item", row: chore, route: "/done/", want: http.StatusSeeOther, state: core.ItemAnswered, answer: "done"},
		{name: "done is not for a row with a check mark", row: decision, route: "/done/", want: http.StatusConflict, state: core.ItemOpen},
		{name: "later snoozes an item", row: chore, route: "/later/", form: url.Values{"when": {"1h"}}, want: http.StatusSeeOther, state: core.ItemSnoozed, snoozed: true},
		{name: "later takes only its three", row: chore, route: "/later/", form: url.Values{"when": {"forever"}}, want: http.StatusBadRequest, state: core.ItemOpen},
		{name: "later snoozes a pull request row", row: "repo#1", route: "/later/", form: url.Values{"when": {"tonight"}}, want: http.StatusSeeOther, pr: true},
		{name: "accept answers with the rec on show", row: decision, route: "/accept/", want: http.StatusSeeOther, state: core.ItemAnswered, answer: "the long rec"},
		{name: "the check mark answers with the row's words", row: decision, route: "/do/", want: http.StatusSeeOther, state: core.ItemAnswered, answer: "Yes: " + decision},
		{name: "another port does nothing", row: chore, route: "/done/", origin: "http://127.0.0.1:9999", want: http.StatusForbidden, state: core.ItemOpen},
		{name: "a wrong token does nothing", row: chore, route: "/reply/", form: url.Values{"text": {"x"}}, badToken: true, want: http.StatusForbidden, state: core.ItemOpen},
		{name: "a row changed since the page does nothing", row: chore, route: "/done/", want: http.StatusConflict, state: core.ItemOpen, stale: func(t *testing.T, w *doWorld, it core.Item) {
			if _, err := w.c.Prepare(w.observe(t), it.Key(), core.Prepared{Do: "Wake it now"}); err != nil {
				t.Fatal(err)
			}
			w.observe(t)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newDoWorld(t)
			file := func(it core.Item) core.Item {
				it.Scope, it.Rec = w.item.Scope, "the long rec"
				got, err := w.c.Store.AddItem(it)
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			target := file(core.Item{Kind: core.KindChore, Question: "wake it", DoLine: chore, Session: "chore-session"})
			if tc.row == decision {
				target = file(core.Item{Kind: core.KindDecision, Question: "which store?", DoLine: decision, Session: "decision-session"})
			}
			w.observe(t)
			row := w.rowOf(t, tc.row)
			m := regexp.MustCompile(`name="token" value="([0-9a-f]+)"`).FindStringSubmatch(row)
			id := regexp.MustCompile(`action="/(?:accept|later)/([0-9a-f]+)"`).FindStringSubmatch(row)
			if m == nil || id == nil {
				t.Fatalf("row has no forms: %s", row)
			}
			form := url.Values{"token": {m[1]}}
			if tc.badToken {
				form.Set("token", "nope")
			}
			for k, v := range tc.form {
				form[k] = v
			}
			if tc.stale != nil {
				tc.stale(t, w, target)
			}
			origin := tc.origin
			if origin == "" {
				origin = w.srv.URL
			}
			if got := w.postTo(t, tc.route, id[1], form, origin); got != tc.want {
				t.Fatalf("POST answered %d, want %d", got, tc.want)
			}
			if tc.pr {
				w.observe(t)
				b, err := w.c.Board()
				if err != nil {
					t.Fatal(err)
				}
				if !slices.ContainsFunc(b.Later, func(n core.Need) bool { return n.PR != nil && n.PR.URL == ownPR }) {
					t.Fatalf("the pull request row did not fold: later %d", len(b.Later))
				}
				if len(w.merger.merged) != 0 {
					t.Fatalf("later merged %v", w.merger.merged)
				}
				return
			}
			got, err := w.c.Store.Item(target.Key())
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tc.state || got.Answer != tc.answer {
				t.Fatalf("item %s answered %q, want %s %q", got.State, got.Answer, tc.state, tc.answer)
			}
			if tc.snoozed && got.SnoozeUntil.IsZero() {
				t.Fatal("snoozed with no time to return")
			}
		})
	}
}

func TestARowsFormsMatchWhatItCanDo(t *testing.T) {
	for _, tc := range []struct {
		name string
		item func(dir string) core.Item
		end  bool
		has  []string
		not  []string
	}{
		{"a decision with a check mark", func(dir string) core.Item {
			return core.Item{Kind: core.KindDecision, Scope: dir, Question: "q", DoLine: "Use the store", Rec: "r", Session: "a"}
		}, false, []string{`aria-label="Use the store"`, ">accept rec<", `/later/`, `/reply/`}, []string{`/done/`}},
		{"an arrow chore", func(dir string) core.Item {
			return core.Item{Kind: core.KindChore, Scope: dir, Question: "q", DoLine: "Use the store", Session: "a"}
		}, false, []string{`/done/`, `/later/`, `/reply/`}, []string{`class="check"`, ">accept rec<", `action="/do/`}},
		{"an item whose goal ended keeps reply and done, not the one tap", func(dir string) core.Item {
			return core.Item{Kind: core.KindDecision, Scope: dir, Question: "q", DoLine: "Use the store", Rec: "r", Session: "a", Impact: core.ImpactProd}
		}, true, []string{`/done/`, `/later/`, `/reply/`}, []string{`class="check"`, `action="/do/`, `/accept/`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newDoWorld(t)
			it := tc.item(w.item.Scope)
			if tc.end {
				g, _, err := w.c.NewGoal("ship", w.c.StateDir, "")
				if err != nil {
					t.Fatal(err)
				}
				it.Dossier.Goal = g.ID
				g.State = core.GoalDone
				if err := w.c.Store.SaveGoal(g); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := w.c.Store.AddItem(it); err != nil {
				t.Fatal(err)
			}
			w.observe(t)
			row := w.rowOf(t, "Use the store")
			for _, want := range tc.has {
				if !strings.Contains(row, want) {
					t.Errorf("row lacks %s: %s", want, row)
				}
			}
			for _, not := range tc.not {
				if strings.Contains(row, not) {
					t.Errorf("row has %s: %s", not, row)
				}
			}
		})
	}
}

func TestThePagesLaterReadsTheBrowsersClockAndKeepsTheHostsWithoutOne(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	for _, tc := range []struct {
		name, tz string
		loc      *time.Location
	}{
		{"a browser's zone", "America/New_York", ny},
		{"no zone", "", nil},
		{"a bad zone", "Mars/Olympus", nil},
		{"a path", "../../etc/passwd", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newDoWorld(t)
			it, err := w.c.Store.AddItem(core.Item{Kind: core.KindChore, Scope: w.item.Scope, Question: "q", DoLine: "Wake the laptop", Session: "another-session"})
			if err != nil {
				t.Fatal(err)
			}
			w.observe(t)
			row := w.rowOf(t, "Wake the laptop")
			if !strings.Contains(row, `name="tz"`) {
				t.Fatalf("no zone field: %s", row)
			}
			id := regexp.MustCompile(`action="/later/([0-9a-f]+)"`).FindStringSubmatch(row)
			tok := regexp.MustCompile(`name="token" value="([0-9a-f]+)"`).FindStringSubmatch(row)
			before := time.Now()
			form := url.Values{"token": {tok[1]}, "when": {"tomorrow"}}
			if tc.tz != "" {
				form.Set("tz", tc.tz)
			}
			if got := w.postTo(t, "/later/", id[1], form, w.srv.URL); got != http.StatusSeeOther {
				t.Fatalf("POST answered %d", got)
			}
			got, err := w.c.Store.Item(it.Key())
			if err != nil {
				t.Fatal(err)
			}
			want, err := core.SnoozeUntil(before, "tomorrow", tc.loc)
			if err != nil {
				t.Fatal(err)
			}
			if !got.SnoozeUntil.Equal(want) {
				t.Fatalf("back %v, want %v", got.SnoozeUntil, want)
			}
		})
	}
}

func TestThePagesMoveRanksAsTheOwnerUnderTheSameGuards(t *testing.T) {
	const first, second = "repo#1", "Wake the laptop"
	for _, tc := range []struct {
		name, row, dir, origin string
		badToken               bool
		stale                  bool
		want                   int
		leads                  string
	}{
		{name: "top on an item", row: second, dir: "top", want: http.StatusSeeOther, leads: second},
		{name: "top on a pull request", row: first, dir: "top", want: http.StatusSeeOther, leads: first},
		{name: "a bad direction", row: second, dir: "sideways", want: http.StatusBadRequest},
		{name: "another port", row: second, dir: "top", origin: "http://127.0.0.1:9999", want: http.StatusForbidden},
		{name: "a wrong token", row: second, dir: "top", badToken: true, want: http.StatusForbidden},
		{name: "a row changed since the page", row: second, dir: "top", stale: true, want: http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newDoWorld(t)
			it, err := w.c.Store.AddItem(core.Item{Kind: core.KindChore, Scope: w.item.Scope, Question: "q", DoLine: second, Session: "another-session"})
			if err != nil {
				t.Fatal(err)
			}
			w.observe(t)
			row := w.rowOf(t, tc.row)
			id := regexp.MustCompile(`action="/rank/([0-9a-f]+)"`).FindStringSubmatch(row)
			tok := regexp.MustCompile(`name="token" value="([0-9a-f]+)"`).FindStringSubmatch(row)
			if id == nil || tok == nil {
				t.Fatalf("no rank form: %s", row)
			}
			form := url.Values{"token": {tok[1]}, "dir": {tc.dir}}
			if tc.badToken {
				form.Set("token", "nope")
			}
			if tc.stale {
				if _, err := w.c.Prepare(w.observe(t), it.Key(), core.Prepared{Do: "Wake it now"}); err != nil {
					t.Fatal(err)
				}
				w.observe(t)
			}
			origin := tc.origin
			if origin == "" {
				origin = w.srv.URL
			}
			if got := w.postTo(t, "/rank/", id[1], form, origin); got != tc.want {
				t.Fatalf("POST answered %d, want %d", got, tc.want)
			}
			if tc.want != http.StatusSeeOther {
				got, err := w.c.Store.Item(it.Key())
				if err != nil || got.Pin != 0 {
					t.Fatalf("pin %d, %v; a refused rank changed it", got.Pin, err)
				}
				return
			}
			rows := strings.Split(w.page(t), "<details")[1:]
			if !strings.Contains(rows[0], tc.leads) {
				t.Fatalf("the first row, before any scan, is not %q: %s", tc.leads, rows[0])
			}
			if tc.row == second {
				got, err := w.c.Store.Item(it.Key())
				if err != nil || got.Pin == 0 || !got.PinOwner {
					t.Fatalf("pin %d owner %v, %v; want the owner's", got.Pin, got.PinOwner, err)
				}
			}
			if len(w.merger.merged) != 0 {
				t.Fatalf("a rank merged %v", w.merger.merged)
			}
		})
	}
}

func TestACheckMarkIsNotInsideTheRowThatOpens(t *testing.T) {
	w := newDoWorld(t)
	for _, want := range []string{"build the queue", "repo#1"} {
		row := w.rowOf(t, want)
		summary := row[:strings.Index(row, "</summary>")]
		if strings.Contains(summary, "<button") {
			t.Fatalf("a button in the summary takes taps aimed at the text: %s", summary)
		}
		if !strings.Contains(row[strings.Index(row, "</details>"):], `class="check"`) {
			t.Fatalf("no check mark beside the row: %s", row)
		}
	}
}
