package cli

import (
	"bytes"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

var copyButtonRe = regexp.MustCompile(`data-copy="([^"]*)" aria-label="Copy ([^"]*)"`)

func TestCopyButtonsCarryTheTextBriefMdAndTheTerminalShow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", dir)
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	g, m, err := c.NewGoal(`ship the "importer" & don't <break> it`, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	m.Session = "manager-session"
	if err := st.SaveManager(m); err != nil {
		t.Fatal(err)
	}
	b := core.Brief{
		Assessment:  "Parser's \"fast path\" is <done> & merged.\n\n  - indented line\n\ttabbed line",
		Changed:     "Line one\nLine two",
		Uncertainty: "none",
		Next:        "ship v2 -> prod",
		WaitingOn:   "CI's 'lint' job",
	}
	if b, err = c.WriteBrief("manager-session", b); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"CSV only, no \"JSON\"", "CSV & TSV;\n  keep <headers>"} {
		if _, err := c.SteerContext(core.Board{}, g.ID, text, core.Owner); err != nil {
			t.Fatal(err)
		}
	}
	it, err := st.AddItem(core.Item{Kind: "decision", Question: `Keep "legacy" <xml> & csv?`, Rec: "drop it", Session: "agent-session", Recipient: m.ID,
		Options: []core.Option{{Label: "keep", Effect: "both stay"}, {Label: "drop", Effect: "csv only"}}})
	if err != nil {
		t.Fatal(err)
	}
	briefMd, err := os.ReadFile(m.BriefPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&briefSite{core: c})
	defer srv.Close()
	buttons := func(path string) map[string]string {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		got := map[string]string{}
		for _, m := range copyButtonRe.FindAllStringSubmatch(string(body), -1) {
			got[html.UnescapeString(m[2])] = html.UnescapeString(m[1])
		}
		return got
	}
	ym := func(args ...string) string {
		t.Helper()
		root := NewRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("ym %v: %v\n%s", args, err, out.String())
		}
		return out.String()
	}
	itemLabel := "item " + it.Qualified()
	brief := buttons("/b/" + g.ID)
	for _, tc := range []struct {
		page, label, want string
	}{
		{"/b/" + g.ID, "brief", ym("copy", "--stdout", "brief", g.ID)},
		{"/b/" + g.ID, "brief", string(briefMd)},
		{"/b/" + g.ID, "Assessment", b.Assessment},
		{"/b/" + g.ID, "What changed", b.Changed},
		{"/b/" + g.ID, "Uncertainty", b.Uncertainty},
		{"/b/" + g.ID, "Next", b.Next},
		{"/b/" + g.ID, "Waiting on", b.WaitingOn},
		{"/b/" + g.ID, "revision 1", "CSV only, no \"JSON\""},
		{"/b/" + g.ID, "revision 2", "CSV & TSV;\n  keep <headers>"},
		{"/b/" + g.ID, "context", ym("goal", "context", "--all", g.ID)},
		{"/b/" + g.ID, itemLabel, ym("copy", "--stdout", "item", it.Key())},
		{"/i/" + strconv.FormatInt(it.Seq, 10), itemLabel, ym("copy", "--stdout", "item", it.Key())},
		{"/", g.ID + " brief", ym("copy", "--stdout", "brief", g.ID)},
	} {
		t.Run(tc.page+" "+tc.label, func(t *testing.T) {
			got := brief
			if tc.page != "/b/"+g.ID {
				got = buttons(tc.page)
			}
			if p, ok := got[tc.label]; !ok {
				t.Fatalf("no copy button %q; have %v", tc.label, got)
			} else if p != tc.want {
				t.Fatalf("copy %q gives\n%q\nwant\n%q", tc.label, p, tc.want)
			}
		})
	}
}
