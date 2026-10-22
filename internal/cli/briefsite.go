package cli

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// briefSite serves every goal's and chief's brief at a stable local page; standings come from the serve loop's latest board.
type briefSite struct {
	core *core.Core
	mu   sync.Mutex
	// standing maps manager ids to how they stood on the latest board.
	standing map[string]string
}

func (s *briefSite) observe(b core.Board) {
	st := map[string]string{}
	var walk func(core.Node)
	walk = func(n core.Node) {
		st[n.Manager.ID] = n.Standing
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, n := range b.Tree {
		walk(n)
	}
	s.mu.Lock()
	s.standing = st
	s.mu.Unlock()
}

func (s *briefSite) standOf(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.standing[id]
}

// listen serves the site on addr until ctx ends.
func (s *briefSite) listen(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("briefs at %s: %w", addr, err)
	}
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type briefNode struct {
	ID, Title, Kind, Repo, Standing, Artifact, Session string
	Plan                                               *core.Steer
	Fresh                                              core.Freshness
	Shown                                              *core.Brief
	Versions                                           []core.Brief
	Reports                                            []briefNode
	Now                                                time.Time
}

func (n briefNode) Age(t time.Time) string {
	d := n.Now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return t.Local().Format("Jan 2 15:04")
}

func (s *briefSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o, err := s.core.LoadOrg()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fresh, err := s.core.Freshness(o)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	steers, err := s.core.Store.LatestSteers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var build func(core.Manager, int, map[string]bool) briefNode
	build = func(m core.Manager, depth int, seen map[string]bool) briefNode {
		seen[m.ID] = true
		n := briefNode{ID: m.ID, Title: m.Name, Kind: "chief", Standing: s.standOf(m.ID), Artifact: m.ArtifactURL, Session: m.Session, Fresh: fresh[m.ID], Now: time.Now()}
		if g, ok := o.Goals[m.GoalID]; ok {
			n.Kind, n.Title, n.Repo = g.ID, g.Ask, g.Repo
			if g.ArtifactURL != "" {
				n.Artifact = g.ArtifactURL
			}
			if n.Standing == "" {
				n.Standing = g.State
			}
		}
		if st, ok := steers[m.ID]; ok {
			n.Plan = &st
		}
		n.Shown = n.Fresh.Brief
		if depth > 0 {
			for _, c := range o.Ordered() {
				if c.ParentID == m.ID && !seen[c.ID] && o.Active(c) {
					n.Reports = append(n.Reports, build(c, depth-1, seen))
				}
			}
		}
		return n
	}
	ref := strings.TrimPrefix(r.URL.Path, "/b/")
	if r.URL.Path == "/" {
		var roots []briefNode
		seen := map[string]bool{}
		for _, m := range o.Ordered() {
			if o.Active(m) && (m.ParentID == "" || !o.Active(o.Managers[m.ParentID])) {
				roots = append(roots, build(m, 64, seen))
			}
		}
		render(w, indexPage, roots)
		return
	}
	if ref == r.URL.Path || ref == "" {
		http.NotFound(w, r)
		return
	}
	m, ok := findManager(o, ref)
	if !ok {
		http.NotFound(w, r)
		return
	}
	n := build(m, 64, map[string]bool{})
	if n.Versions, err = s.core.Store.Briefs(m.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("v")); err == nil {
		n.Shown = nil
		for i := range n.Versions {
			if n.Versions[i].Version == v {
				n.Shown = &n.Versions[i]
			}
		}
		if n.Shown == nil {
			http.NotFound(w, r)
			return
		}
	}
	render(w, briefPage, n)
}

func findManager(o core.Org, ref string) (core.Manager, bool) {
	ref = strings.ToLower(ref)
	if m, ok := o.Managers[ref]; ok {
		return m, true
	}
	if g, ok := o.Goals[ref]; ok {
		m, ok := o.Managers[g.ManagerID]
		return m, ok
	}
	return core.Manager{}, false
}

func render(w http.ResponseWriter, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

const siteStyle = `<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta http-equiv="refresh" content="30">
<style>
:root{--bg:#fbfaf7;--fg:#1f2328;--dim:#6a737d;--line:#e3e1db;--warn:#9a6700;--bad:#cf222e;--ok:#1a7f37;--card:#fff}
@media (prefers-color-scheme:dark){:root{--bg:#14161a;--fg:#e6e6e6;--dim:#8b949e;--line:#2d3139;--warn:#d29922;--bad:#f85149;--ok:#3fb950;--card:#1b1e23}}
body{background:var(--bg);color:var(--fg);font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0 auto;max-width:880px;padding:24px 16px}
a{color:inherit}h1{font-size:22px;margin:0 0 4px}h2{font-size:13px;letter-spacing:.06em;text-transform:uppercase;color:var(--dim);margin:20px 0 4px}
.meta{color:var(--dim);font-size:13px}.tag{display:inline-block;border:1px solid var(--line);border-radius:10px;padding:0 8px;font-size:12px;margin-right:4px}
.stale,.unadopted,.quiet{color:var(--warn);border-color:var(--warn)}.stuck,.down{color:var(--bad);border-color:var(--bad)}.done,.fresh{color:var(--ok);border-color:var(--ok)}
.card{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:12px 16px;margin:12px 0}
.sec{white-space:pre-wrap;margin:0}.plan{white-space:pre-wrap;border-left:3px solid var(--line);padding-left:12px}
ul{padding-left:18px}li{margin:6px 0}
</style>`

const tags = `{{define "tags"}}<span class="tag {{.Standing}}">{{.Standing}}</span>{{if .Fresh.Brief}}{{if .Fresh.Stale}}<span class="tag stale">brief v{{.Fresh.Brief.Version}} stale</span>{{else}}<span class="tag fresh">brief v{{.Fresh.Brief.Version}}, {{.Age .Fresh.Brief.CreatedAt}}</span>{{end}}{{else}}<span class="tag stale">no brief</span>{{end}}{{if .Plan}}{{if .Plan.Adopted}}<span class="tag">plan r{{.Plan.Revision}}</span>{{else}}<span class="tag unadopted">steer r{{.Plan.Revision}} not adopted</span>{{end}}{{end}}{{end}}`

var indexPage = template.Must(template.New("index").Parse(tags + `<!doctype html><html><head><title>ym briefs</title>` + siteStyle + `</head><body>
<h1>Briefs</h1><p class="meta">Every goal and chief ym runs. Refreshes every 30 seconds.</p>
{{define "tree"}}<ul>{{range .}}<li><a href="/b/{{.ID}}"><b>{{if eq .Kind "chief"}}chief {{.Title}}{{else}}{{.Kind}}{{end}}</b></a> {{if ne .Kind "chief"}}{{.Title}}{{end}}<br>{{template "tags" .}}{{if .Fresh.Brief}}<div class="meta">{{.Fresh.Brief.Assessment}}</div>{{end}}{{if .Reports}}{{template "tree" .Reports}}{{end}}</li>{{end}}</ul>{{end}}
{{if .}}{{template "tree" .}}{{else}}<p>No goals. Start one with <code>ym goal "&lt;ask&gt;"</code>.</p>{{end}}
</body></html>`))

var briefPage = template.Must(template.New("brief").Parse(tags + `<!doctype html><html><head><title>{{if eq .Kind "chief"}}chief {{.Title}}{{else}}{{.Kind}}{{end}} brief</title>` + siteStyle + `</head><body>
<p class="meta"><a href="/">all briefs</a></p>
<h1>{{if eq .Kind "chief"}}Chief {{.Title}}{{else}}{{.Kind}}: {{.Title}}{{end}}</h1>
<p class="meta">{{.ID}}{{if .Repo}} · {{.Repo}}{{end}}{{if .Artifact}} · <a href="{{.Artifact}}">artifact</a>{{end}}</p>
<p>{{template "tags" .}}</p>
{{if .Fresh.Stale}}{{if .Fresh.Brief}}<p class="meta">This brief predates {{.Fresh.BehindWhat}} ({{.Age .Fresh.Behind}}); its manager has been asked to refresh it.</p>{{end}}{{end}}
{{if .Plan}}<h2>Plan, revision {{.Plan.Revision}} by {{.Plan.By}}{{if not .Plan.Adopted}}, not adopted yet{{end}}</h2><div class="plan">{{.Plan.Text}}</div>{{end}}
{{with .Shown}}<div class="card"><p class="meta">Brief v{{.Version}}, {{$.Age .CreatedAt}}</p>{{range .Sections}}<h2>{{index . 0}}</h2><p class="sec">{{index . 1}}</p>{{end}}</div>
{{else}}<p>No brief recorded yet.</p>{{end}}
{{define "report"}}<div class="card"><a href="/b/{{.ID}}"><b>{{if eq .Kind "chief"}}chief {{.Title}}{{else}}{{.Kind}}: {{.Title}}{{end}}</b></a><br>{{template "tags" .}}
{{with .Fresh.Brief}}<h2>Assessment</h2><p class="sec">{{.Assessment}}</p><h2>Next</h2><p class="sec">{{.Next}}</p><h2>Waiting on</h2><p class="sec">{{.WaitingOn}}</p>{{end}}
{{range .Reports}}{{template "report" .}}{{end}}</div>{{end}}
{{if .Reports}}<h2>Reports</h2>{{range .Reports}}{{template "report" .}}{{end}}{{end}}
{{if .Versions}}<h2>Versions</h2><p class="meta">{{range .Versions}}<a href="?v={{.Version}}">v{{.Version}}</a> {{end}}</p>{{end}}
</body></html>`))
