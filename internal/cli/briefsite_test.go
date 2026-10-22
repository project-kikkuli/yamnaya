package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
	chief, err := c.NewChief("data", "", dir, []string{m.ID})
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
}
