package cli

import (
	"bytes"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func TestGoalContextAndTheBriefSiteShowAChiefsRemitAsRevisionZero(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", dir)
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	remit := "Get the importer & exporter shipping CSV."
	chief, err := c.NewChief("data", remit, dir, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	chief.Session = "chief-session"
	if err := st.SaveManager(chief); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WriteBrief("chief-session", core.Brief{Assessment: "planning", Changed: "x", Uncertainty: "none", Next: "goals", WaitingOn: "none"}); err != nil {
		t.Fatal(err)
	}
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"goal", "context", chief.ID})
	if err := root.Execute(); err != nil {
		t.Fatalf("ym goal context: %v\n%s", err, out.String())
	}
	if want := chief.ID + " revision 0, the ask it started with:\n\n" + remit + "\n"; out.String() != want {
		t.Fatalf("ym goal context %s = %q, want %q", chief.ID, out.String(), want)
	}
	srv := httptest.NewServer(&briefSite{core: c})
	defer srv.Close()
	res, err := http.Get(srv.URL + "/b/" + chief.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if page := html.UnescapeString(string(body)); !strings.Contains(page, "Revision 0, the ask it started with") || !strings.Contains(page, `<div class="plan">`+remit+`</div>`) {
		t.Fatalf("the brief site does not show the remit as revision 0:\n%s", page)
	}
}
