package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

var siteLink = regexp.MustCompile(`(?:href|src)="(/[^"]*)"`)

func TestBriefSiteServesUnderThePrefixItsPublicURLNames(t *testing.T) {
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
	if _, err := c.NewChief("data", "", dir, "", []string{m.ID}); err != nil {
		t.Fatal(err)
	}
	it, err := st.AddItem(core.Item{Kind: "decision", Question: "keep csv?", Session: "agent-session", Recipient: core.Owner})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		public, prefix, host string
	}{
		{"", "", "127.0.0.1:7477"},
		{"http://127.0.0.1:7477/", "", "127.0.0.1:7477"},
		{"https://box.example/ym/", "/ym", "box.example"},
		{"https://box.example/ym", "/ym", "127.0.0.1:7477"},
		{"https://Box.Example:8443/a/b/", "/a/b", "box.example:8443"},
	} {
		t.Run(tc.public, func(t *testing.T) {
			site := &briefSite{core: c, public: tc.public}
			get := func(path string) (int, string) {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Host = tc.host
				rec := httptest.NewRecorder()
				site.ServeHTTP(rec, req)
				return rec.Code, rec.Body.String()
			}
			pages := []string{"/", "/b/" + g.ID, "/b/" + m.ID, fmt.Sprintf("/i/%d", it.Seq)}
			for _, p := range pages {
				for _, path := range []string{p, tc.prefix + p} {
					code, body := get(path)
					if code != http.StatusOK {
						t.Fatalf("GET %s = %d: %s", path, code, body)
					}
					links := siteLink.FindAllStringSubmatch(body, -1)
					if len(links) == 0 {
						t.Fatalf("GET %s links nowhere:\n%s", path, body)
					}
					for _, l := range links {
						if !strings.HasPrefix(l[1], tc.prefix+"/") {
							t.Errorf("GET %s links %s outside %s/", path, l[1], tc.prefix)
						}
						if code, _ := get(l[1]); code != http.StatusOK {
							t.Errorf("GET %s links %s, which answers %d", path, l[1], code)
						}
					}
				}
			}
			if tc.prefix != "" {
				if code, _ := get(tc.prefix); code != http.StatusOK {
					t.Errorf("GET %s = %d, want the index", tc.prefix, code)
				}
				if code, _ := get(tc.prefix + "x/b/" + g.ID); code != http.StatusNotFound {
					t.Errorf("GET %sx/b/%s = %d, want 404 for a path that only starts like the prefix", tc.prefix, g.ID, code)
				}
			}
			if code, body := get(tc.prefix + "/site.js"); code != http.StatusOK || !strings.Contains(body, "data-copy") {
				t.Errorf("GET %s/site.js = %d: %s", tc.prefix, code, body)
			}
		})
	}
}

func TestBriefSiteAnswersThePublicURLsHostOnly(t *testing.T) {
	dir := t.TempDir()
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	for _, tc := range []struct {
		public, host string
		want         int
	}{
		{"", "box.example", http.StatusForbidden},
		{"http://127.0.0.1:7477/", "box.example", http.StatusForbidden},
		{"https://box.example/ym/", "box.example", http.StatusOK},
		{"https://box.example/ym/", "BOX.example:443", http.StatusOK},
		{"https://box.example/ym/", "127.0.0.1:7477", http.StatusOK},
		{"https://box.example/ym/", "other.example", http.StatusForbidden},
		{"https://box.example/ym/", "box.example.attacker.test", http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodGet, "/ym/", nil)
		req.Host = tc.host
		rec := httptest.NewRecorder()
		(&briefSite{core: c, public: tc.public}).ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("public %q, Host %s: status %d, want %d", tc.public, tc.host, rec.Code, tc.want)
		}
	}
}
