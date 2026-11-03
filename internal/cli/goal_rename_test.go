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

func TestGoalRenameRenamesAGoalOrChiefAndItsBriefPageAnswersToEveryRef(t *testing.T) {
	for _, tc := range []struct {
		name, ref, to, err string
	}{
		{name: "a chief by its name", ref: "data", to: "data-platform"},
		{name: "a chief by its id", ref: "mgr1", to: "data-platform"},
		{name: "its own name again", ref: "data", to: "data"},
		{name: "an invalid name", ref: "data", to: "Data Platform", err: "kebab-case"},
		{name: "another host's chief", ref: "box/mgr1", to: "data-platform", err: "rename it there"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("YAMNAYA_STATE_DIR", dir)
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
			root := NewRoot()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs([]string{"goal", "rename", tc.ref, tc.to})
			err = root.Execute()
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("ym goal rename %s %s = %v, want an error saying %q", tc.ref, tc.to, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ym goal rename: %v\n%s", err, out.String())
			}
			srv := httptest.NewServer(&briefSite{core: c})
			defer srv.Close()
			for _, ref := range []string{tc.to, chief.ID} {
				res, err := http.Get(srv.URL + "/b/" + ref)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(res.Body)
				res.Body.Close()
				if page := html.UnescapeString(string(body)); res.StatusCode != http.StatusOK || !strings.Contains(page, "Chief "+tc.to) {
					t.Fatalf("/b/%s answered %d without naming chief %s:\n%s", ref, res.StatusCode, tc.to, page)
				}
			}
		})
	}
}
