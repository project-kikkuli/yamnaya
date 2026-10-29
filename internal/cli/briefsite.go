package cli

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/clipboard"
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
	// page renders a brief as its brief.md holds it.
	page func(core.Brief) string
	// Remote says which host the brief is from and how old the read is.
	Remote   string
	Plan     *core.Steer
	Fresh    core.Freshness
	Shown    *core.Brief
	Versions []core.Brief
	Reports  []briefNode
	Now      time.Time
	// Context holds the context revisions, newest first, and ContextText all of them as ym goal context --all prints them; Ask is
	// revision 0.
	Context          []core.Steer
	Ask, ContextText string
	Items            []itemView
}

// Name is what the site calls n: its goal's id, or the chief's.
func (n briefNode) Name() string {
	if n.Kind == "chief" {
		return n.ID
	}
	return n.Kind
}

// Markdown is the shown brief as its brief.md holds it and ym copy gives it.
func (n briefNode) Markdown() string {
	if n.Shown == nil {
		return ""
	}
	return clipboard.Clean(n.page(*n.Shown))
}

// itemView is one item as the site lists it; Text is what ym copy item gives.
type itemView struct {
	Seq                  int64
	Key, ID, Kind, State string
	Question, Text       string
}

func (s *briefSite) itemView(it core.Item) itemView {
	return itemView{Seq: it.Seq, Key: it.Key(), ID: it.Qualified(), Kind: it.Kind, State: it.State, Question: it.Question, Text: clipboard.Clean(s.core.Store.Nicked(s.core.ItemText(it)))}
}

// items lists the open and snoozed items keep matches, oldest first.
func (s *briefSite) items(keep func(core.Item) bool) ([]itemView, error) {
	all, err := s.core.Store.Items(core.ItemFilter{States: []string{core.ItemOpen, core.ItemSnoozed}})
	var out []itemView
	for _, it := range all {
		if keep(it) {
			out = append(out, s.itemView(it))
		}
	}
	return out, err
}

type indexData struct {
	Roots []briefNode
	Items []itemView
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
	if !directHost(r.Host) {
		http.Error(w, "briefs answer only a request to an address, not a name", http.StatusForbidden)
		return
	}
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
		n := briefNode{ID: m.ID, Title: m.Name, page: func(b core.Brief) string { return o.BriefPage(m, b) }, Kind: "chief", Standing: s.standOf(m.ID), Artifact: m.ArtifactURL, Session: m.Session,
			Fresh: fresh[m.ID], Now: time.Now()}
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
	if host, rest, ok := core.SplitRemote(ref); ok && ref != r.URL.Path {
		if host != s.core.SelfName() {
			s.serveRemote(w, r, host, rest)
			return
		}
		ref = rest
	}
	if r.URL.Path == "/" {
		var d indexData
		seen := map[string]bool{}
		for _, m := range o.Ordered() {
			if o.Active(m) && (m.ParentID == "" || !o.Active(o.Managers[m.ParentID])) {
				d.Roots = append(d.Roots, build(m, 64, seen))
			}
		}
		if d.Items, err = s.items(func(it core.Item) bool { return it.State == core.ItemOpen && it.Recipient == core.Owner }); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		render(w, indexPage, d)
		return
	}
	if seq, ok := strings.CutPrefix(r.URL.Path, "/i/"); ok {
		it, err := s.core.Store.Item("#" + seq)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		render(w, itemPage, s.itemView(it))
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
	if n.Context, err = s.core.Store.Steers(m.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if n.Ask = o.Goals[m.GoalID].Ask; len(n.Context) > 0 || n.Ask != "" {
		var sb strings.Builder
		writeContext(&sb, m.ID, n.Ask, n.Context)
		n.ContextText = clipboard.Clean(sb.String())
	}
	slices.Reverse(n.Context)
	if n.Items, err = s.items(func(it core.Item) bool {
		return it.Recipient == m.ID || it.Preparer == m.ID || (m.Session != "" && it.Session == m.Session)
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	render(w, briefPage, n)
}

// remoteRead bounds how long a brief page waits on another host before showing what ym last read from it.
const remoteRead = 5 * time.Second

// serveRemote shows the brief of manager or goal ref on host, read from the snapshot that host's ym serve keeps, or from the
// last one ym read when the host does not answer in time.
func (s *briefSite) serveRemote(w http.ResponseWriter, r *http.Request, host, ref string) {
	known, err := s.core.Store.KnownHosts()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !slices.ContainsFunc(known, func(h core.KnownHost) bool { return h.Name == host }) {
		http.NotFound(w, r)
		return
	}
	st := s.core.Store
	hosts := newRemoteHosts([]string{host}, remoteRead, func(h string, at time.Time, raw []byte) { st.KeepBoard(h, at, raw) })
	hosts.timeout = remoteRead
	if at, raw, ok, _ := st.KeptBoard(host); ok {
		hosts.keep(host, at, raw)
	}
	rb := hosts.Boards()[0]
	id := strings.ToLower(ref)
	for _, g := range rb.Goals {
		if g.ID == id {
			id = g.ManagerID
		}
	}
	var m *core.Manager
	for i := range rb.Managers {
		if rb.Managers[i].ID == id {
			m = &rb.Managers[i]
		}
	}
	if m == nil {
		http.Error(w, fmt.Sprintf("%s: no goal or chief %s on %s as of %s", rb.StateAt(time.Now()), ref, host, rb.At.Local().Format(time.Kitchen)), http.StatusNotFound)
		return
	}
	n := briefNode{ID: core.RemoteKey(host, m.ID), Title: m.Name, page: func(b core.Brief) string { return b.Markdown(m.Name) }, Kind: "chief", Standing: rb.Standing[m.ID], Artifact: m.ArtifactURL, Session: m.Session,
		Fresh: rb.Fresh[m.ID], Now: time.Now()}
	for _, g := range rb.Goals {
		if g.ID == m.GoalID {
			n.Kind, n.Title, n.Repo = core.RemoteKey(host, g.ID), g.Ask, g.Repo
			n.page = g.BriefPage
			if g.ArtifactURL != "" {
				n.Artifact = g.ArtifactURL
			}
		}
	}
	n.Remote = fmt.Sprintf("On %s, as its ym serve saw it %s.", host, n.Age(rb.At))
	if rb.Err != nil || rb.Late(n.Now) {
		n.Remote += " " + host + ": " + rb.StateAt(n.Now)
	}
	if n.Shown = n.Fresh.Brief; n.Shown != nil {
		n.Versions = []core.Brief{*n.Shown}
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("v")); err == nil && (n.Shown == nil || n.Shown.Version != v) {
		http.Error(w, fmt.Sprintf("only the latest brief of %s is read from %s", n.ID, host), http.StatusNotFound)
		return
	}
	render(w, briefPage, n)
}

// directHost reports whether host names the machine by address or as localhost, which no other site's DNS can point here.
func directHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil
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

// siteStyle carries the look and the copy buttons. The page reloads every 30 seconds only while no text is selected, so a
// reload never drops a selection the owner is copying.
const siteStyle = `<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<style>
:root{--bg:#fbfaf7;--fg:#1f2328;--dim:#6a737d;--line:#e3e1db;--warn:#9a6700;--bad:#cf222e;--ok:#1a7f37;--card:#fff}
@media (prefers-color-scheme:dark){:root{--bg:#14161a;--fg:#e6e6e6;--dim:#8b949e;--line:#2d3139;--warn:#d29922;--bad:#f85149;--ok:#3fb950;--card:#1b1e23}}
body{background:var(--bg);color:var(--fg);font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0 auto;max-width:880px;padding:24px 16px}
a{color:inherit}h1{font-size:22px;margin:0 0 4px}h2{font-size:13px;letter-spacing:.04em;color:var(--dim);margin:20px 0 4px}
.meta{color:var(--dim);font-size:13px}.tag{display:inline-block;border:1px solid var(--line);border-radius:10px;padding:0 8px;font-size:12px;margin-right:4px}
.stale,.unadopted,.quiet{color:var(--warn);border-color:var(--warn)}.stuck,.down{color:var(--bad);border-color:var(--bad)}.done,.fresh{color:var(--ok);border-color:var(--ok)}
.card{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:12px 16px;margin:12px 0}
.sec{white-space:pre-wrap;margin:0}.plan{white-space:pre-wrap;border-left:3px solid var(--line);padding-left:12px}
.copy{user-select:none;-webkit-user-select:none;font-size:12px;font-weight:normal;letter-spacing:0;color:var(--dim);background:none;border:1px solid var(--line);border-radius:6px;padding:0 8px;margin-left:8px;cursor:pointer;vertical-align:middle}
.copy:hover{color:var(--fg)}
ul{padding-left:18px}li{margin:6px 0}
</style>
<script>
document.addEventListener("click", e => {
	const b = e.target.closest("[data-copy]");
	if (!b) return;
	const t = b.dataset.copy, done = () => { b.textContent = "copied"; setTimeout(() => b.textContent = "copy", 1500) };
	(navigator.clipboard ? navigator.clipboard.writeText(t) : Promise.reject()).then(done, () => {
		const a = document.createElement("textarea");
		a.value = t;
		document.body.append(a);
		a.select();
		document.execCommand("copy");
		a.remove();
		done();
	});
});
setInterval(() => { if (getSelection().isCollapsed) location.reload() }, 30000);
</script>`

type copyButton struct{ Label, Text string }

var siteFuncs = template.FuncMap{"copy": func(label, text string) copyButton { return copyButton{label, text} }}

const tags = `{{define "copy"}}<button class="copy" type="button" data-copy="{{.Text}}" aria-label="Copy {{.Label}}">copy</button>{{end}}` +
	`{{define "tags"}}<span class="tag {{.Standing}}">{{.Standing}}</span>{{if .Fresh.Brief}}{{if .Fresh.Stale}}<span class="tag stale">brief v{{.Fresh.Brief.Version}} stale</span>{{else}}<span class="tag fresh">brief v{{.Fresh.Brief.Version}}, {{.Age .Fresh.Brief.CreatedAt}}</span>{{end}}{{else}}<span class="tag stale">no brief</span>{{end}}{{if .Plan}}{{if .Plan.Adopted}}<span class="tag">plan r{{.Plan.Revision}}</span>{{else}}<span class="tag unadopted">steer r{{.Plan.Revision}} not adopted</span>{{end}}{{end}}{{end}}` +
	`{{define "items"}}{{range .}}<div class="card"><a href="/i/{{.Seq}}"><b>{{.ID}}</b></a> <span class="tag">{{.Kind}}</span><span class="tag">{{.State}}</span>{{template "copy" (copy (print "item " .ID) .Text)}}<p class="sec">{{.Question}}</p></div>{{end}}{{end}}`

var indexPage = template.Must(template.New("index").Funcs(siteFuncs).Parse(tags + `<!doctype html><html><head><title>ym briefs</title>` + siteStyle + `</head><body>
<h1>Briefs</h1><p class="meta">Every goal and chief ym runs. Refreshes every 30 seconds while no text is selected.</p>
{{define "tree"}}<ul>{{range .}}<li><a href="/b/{{.ID}}"><b>{{if eq .Kind "chief"}}chief {{.Title}}{{else}}{{.Kind}}{{end}}</b></a> {{if ne .Kind "chief"}}{{.Title}}{{end}}<br>{{template "tags" .}}{{if .Fresh.Brief}}{{template "copy" (copy (print .Name " brief") .Markdown)}}<div class="meta">{{.Fresh.Brief.Assessment}}</div>{{end}}{{if .Reports}}{{template "tree" .Reports}}{{end}}</li>{{end}}</ul>{{end}}
{{if .Roots}}{{template "tree" .Roots}}{{else}}<p>No goals. Start one with <code>ym goal "&lt;ask&gt;"</code>.</p>{{end}}
{{if .Items}}<h2>Open items for you</h2>{{template "items" .Items}}{{end}}
</body></html>`))

var itemPage = template.Must(template.New("item").Funcs(siteFuncs).Parse(tags + `<!doctype html><html><head><title>item {{.ID}}</title>` + siteStyle + `</head><body>
<p class="meta"><a href="/">all briefs</a></p>
<h1>Item {{.ID}}{{template "copy" (copy (print "item " .ID) .Text)}}</h1>
<p class="meta">{{.Key}} · {{.Kind}} · {{.State}}</p>
<div class="card"><p class="sec">{{.Text}}</p></div>
</body></html>`))

var briefPage = template.Must(template.New("brief").Funcs(siteFuncs).Parse(tags + `<!doctype html><html><head><title>{{if eq .Kind "chief"}}chief {{.Title}}{{else}}{{.Kind}}{{end}} brief</title>` + siteStyle + `</head><body>
<p class="meta"><a href="/">all briefs</a></p>
<h1>{{if eq .Kind "chief"}}Chief {{.Title}}{{else}}{{.Kind}}: {{.Title}}{{end}}</h1>
<p class="meta">{{.ID}}{{if .Repo}} · {{.Repo}}{{end}}{{if .Artifact}} · <a href="{{.Artifact}}">artifact</a>{{end}}</p>
<p>{{template "tags" .}}</p>
{{if .Remote}}<p class="meta">{{.Remote}}</p>{{end}}
{{if .Fresh.Stale}}{{if .Fresh.Brief}}<p class="meta">This brief predates {{.Fresh.BehindWhat}} ({{.Age .Fresh.Behind}}); its manager has been asked to refresh it.</p>{{end}}{{end}}
{{with .Shown}}<div class="card"><p class="meta">Brief v{{.Version}}, {{$.Age .CreatedAt}}{{template "copy" (copy "brief" $.Markdown)}}</p>{{range .Sections}}<h2>{{index . 0}}{{template "copy" (copy (index . 0) (index . 1))}}</h2><p class="sec">{{index . 1}}</p>{{end}}</div>
{{else}}<p>No brief recorded yet.</p>{{end}}
{{if .ContextText}}<h2>Context{{template "copy" (copy "context" .ContextText)}}</h2>
{{range .Context}}<div class="card"><p class="meta">Revision {{.Revision}} by {{.By}}, {{$.Age .CreatedAt}}, {{if .Adopted}}adopted{{else}}not adopted yet{{end}}{{template "copy" (copy (print "revision " .Revision) .Text)}}</p><div class="plan">{{.Text}}</div></div>
{{else}}<div class="card"><p class="meta">Revision 0, the ask it started with{{template "copy" (copy "revision 0" $.Ask)}}</p><div class="plan">{{$.Ask}}</div></div>{{end}}{{end}}
{{if .Items}}<h2>Open items</h2>{{template "items" .Items}}{{end}}
{{define "report"}}<div class="card"><a href="/b/{{.ID}}"><b>{{if eq .Kind "chief"}}chief {{.Title}}{{else}}{{.Kind}}: {{.Title}}{{end}}</b></a>{{if .Fresh.Brief}}{{template "copy" (copy (print .Name " brief") .Markdown)}}{{end}}<br>{{template "tags" .}}
{{with .Fresh.Brief}}<h2>Assessment</h2><p class="sec">{{.Assessment}}</p><h2>Next</h2><p class="sec">{{.Next}}</p><h2>Waiting on</h2><p class="sec">{{.WaitingOn}}</p>{{end}}
{{range .Reports}}{{template "report" .}}{{end}}</div>{{end}}
{{if .Reports}}<h2>Reports</h2>{{range .Reports}}{{template "report" .}}{{end}}{{end}}
{{if .Versions}}<h2>Versions</h2><p class="meta">{{range .Versions}}<a href="?v={{.Version}}">v{{.Version}}</a> {{end}}</p>{{end}}
</body></html>`))
