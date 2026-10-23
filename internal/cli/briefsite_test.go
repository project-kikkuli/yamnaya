package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
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
