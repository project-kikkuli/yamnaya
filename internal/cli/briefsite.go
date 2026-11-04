package cli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
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
	// board is the latest board; the queue reads it, so a page load does no scan of its own.
	board *core.Board
	// secret is the token this serve puts in its forms; acted holds when each row was acted on, which keeps it off the page until a later read shows how it stands.
	secret string
	acted  map[string]time.Time
	// public is the URL a proxy publishes the site at, as core.BriefsURL gives it; its path prefixes every link, and its host is
	// answered along with addresses.
	public string
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
	shown, err := s.core.WithHostRows(b)
	if err != nil {
		shown = b
	}
	s.mu.Lock()
	s.standing = st
	s.board = &shown
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
	org              core.Org
}

// By names who wrote a context revision.
func (n briefNode) By(st core.Steer) string { return n.org.SteerBy(st) }

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
	Seq                        int64
	Key, ID, Lead, Kind, State string
	Question, Text             string
}

func (s *briefSite) itemView(it core.Item) itemView {
	o, _ := s.core.LoadOrg()
	return itemView{Seq: it.Seq, Key: it.Key(), ID: it.Ref(), Lead: o.ItemLead(it), Kind: it.Kind, State: it.State, Question: it.Question, Text: clipboard.Clean(s.core.Store.Nicked(s.core.ItemText(it)))}
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
	Queue queueView
}

// queueView is the owner's queue as the board draws it, from the same renderer.
type queueView struct {
	Header string
	Rows   []queueRowView
	Later  []queueRowView
}

// queueRowView is one row; Command is the ym command its check mark stands for, Link what its ▸ opens. Act names what POST /do/<ID>
// does, empty when the row has nothing to do; Check says the closed row's mark does it.
type queueRowView struct {
	Line, Mark, Side, Command, Link, LinkText string
	ID, Token, Act, CheckLabel                string
	Check, Later, Reply, Done, Rank           bool
	Detail                                    []string
}

func queueRow(n core.Need, label, token string, now time.Time) queueRowView {
	line, side := core.RowLine(n, label, 200, now)
	body, mark := line[:len(line)-len("▸")], line[len(line)-len("▸"):]
	v := queueRowView{Line: body, Mark: mark, Side: side, Detail: core.RowDetail(n, core.QueueWidth, now), Check: n.Check() != core.CheckNone}
	v.ID, v.Token = n.RowID(), token
	switch n.Accept() {
	case core.CheckGo:
		v.Act = "go ahead"
	case core.CheckAccept:
		v.Act = "accept rec"
	case core.CheckMerge:
		v.Act = "merge"
	}
	v.CheckLabel = v.Act
	if n.Check() == core.CheckAccept {
		v.CheckLabel = n.DoLine()
	}
	v.Later, v.Reply, v.Done, v.Rank = canLater(n, now), canReply(n), canDone(n), !n.Unrouted
	switch {
	case n.Item != nil:
		v.Link, v.LinkText = fmt.Sprintf("/i/%d", n.Item.Seq), "full item"
		switch n.Accept() {
		case core.CheckGo:
			v.Command = fmt.Sprintf("ym item go '%s'", n.Item.Key())
		case core.CheckAccept:
			v.Command = fmt.Sprintf("ym item answer '%s' %s", n.Item.Key(), shellQuote(n.Item.Rec))
		}
	case n.PR != nil:
		v.Link, v.LinkText = n.PR.URL, "open on GitHub"
	case n.Review != nil:
		v.Link, v.LinkText = n.Review.URL, "open on GitHub"
	case n.Doc != nil:
		v.Link, v.LinkText = n.Doc.URL, "open document"
	}
	return v
}

// latest is the serve loop's board; before its first one it reads one.
func (s *briefSite) latest() (core.Board, error) {
	s.mu.Lock()
	b := s.board
	s.mu.Unlock()
	if b != nil {
		return *b, nil
	}
	board, err := s.core.Board()
	if err != nil {
		return board, err
	}
	return s.core.WithHostRows(board)
}

// token is this serve's form token, made on first use.
func (s *briefSite) token() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.secret == "" {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return "", err
		}
		s.secret = hex.EncodeToString(raw)
	}
	return s.secret, nil
}

// unacted drops the rows an action took until a read made since shows how they stand, so a row reopened later comes back.
func (s *briefSite) unacted(ns []core.Need, at time.Time) []core.Need {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(ns), func(n core.Need) bool {
		id := n.RowID()
		when, ok := s.acted[id]
		if !ok {
			return false
		}
		read := at
		if !n.Read.IsZero() {
			read = n.Read
		}
		if read.After(when) {
			delete(s.acted, id)
			return false
		}
		return true
	})
}

func (s *briefSite) queue() (queueView, error) {
	b, err := s.latest()
	if err != nil {
		return queueView{}, err
	}
	token, err := s.token()
	if err != nil {
		return queueView{}, err
	}
	now, needs, later := time.Now(), s.unacted(b.Needs, b.At), s.unacted(b.Later, b.At)
	v := queueView{Header: core.QueueHeader(needs, len(later))}
	for i, label := range core.RankLabels(len(needs)) {
		v.Rows = append(v.Rows, queueRow(needs[i], label, token, now))
	}
	for _, n := range later {
		v.Later = append(v.Later, queueRow(n, core.LaterLabel, token, now))
	}
	return v, nil
}

func canLater(n core.Need, now time.Time) bool {
	if n.Item != nil {
		return n.Item.State == core.ItemOpen && !n.Unrouted
	}
	return !n.Mark.Until.After(now)
}

func canReply(n core.Need) bool { return n.Item != nil && !n.Unrouted }

func canDone(n core.Need) bool {
	return canReply(n) && n.Check() == core.CheckNone && !n.AsksTheOwner()
}

// sameOrigin reports whether the browser says the form came from this site: the address the request reached, or the public URL a
// proxy publishes it at, port included.
func (s *briefSite) sameOrigin(r *http.Request) bool {
	u, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || u.Host == "" {
		return false
	}
	origin := hostPort(u.Scheme, u.Host)
	if origin == hostPort(u.Scheme, r.Host) {
		return true
	}
	public, err := url.Parse(s.public)
	return err == nil && public.Host != "" && origin == hostPort(public.Scheme, public.Host)
}

// hostPort is host with the port its scheme implies when it names none.
func hostPort(scheme, host string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return strings.ToLower(host)
	}
	port := "80"
	if scheme == "https" {
		port = "443"
	}
	return strings.ToLower(net.JoinHostPort(strings.Trim(host, "[]"), port))
}

var errBadForm = errors.New("bad form")

// serveRow does act to the row named id, which binds the row's words and head, so a page older than the queue acts on the same row as it
// read or on nothing.
func (s *briefSite) serveRow(w http.ResponseWriter, r *http.Request, id string, hide bool, can func(core.Need) bool, act func(core.Board, core.Need) error) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "post only", http.StatusMethodNotAllowed)
		return
	}
	token, err := s.token()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !s.sameOrigin(r) || subtle.ConstantTimeCompare([]byte(r.PostFormValue("token")), []byte(token)) != 1 {
		http.Error(w, "this page is out of date or not from this site; reload it", http.StatusForbidden)
		return
	}
	b, err := s.latest()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var need *core.Need
	for _, ns := range [][]core.Need{s.unacted(b.Needs, b.At), s.unacted(b.Later, b.At)} {
		for i := range ns {
			if ns[i].RowID() == id && can(ns[i]) {
				need = &ns[i]
			}
		}
	}
	if need == nil {
		http.Error(w, "this row changed since you read it, so nothing was done; go back and reload", http.StatusConflict)
		return
	}
	if err := act(b, *need); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errBadForm) {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	if hide {
		s.mu.Lock()
		if s.acted == nil {
			s.acted = map[string]time.Time{}
		}
		s.acted[id] = time.Now()
		s.mu.Unlock()
	}
	http.Redirect(w, r, s.prefix()+"/", http.StatusSeeOther)
}

func browserZone(name string) *time.Location {
	if name == "" {
		return nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil
	}
	return loc
}

func (s *briefSite) showRank(n core.Need) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.board == nil {
		return
	}
	if b, err := s.core.Reranked(*s.board, n); err == nil {
		s.board = &b
	}
}

// serveRowAction answers the forms of a queue row; each takes the path the board's key for the same act takes.
func (s *briefSite) serveRowAction(w http.ResponseWriter, r *http.Request, path string) bool {
	if id, ok := strings.CutPrefix(path, "/do/"); ok {
		s.serveRow(w, r, id, true, func(n core.Need) bool { return n.Check() != core.CheckNone }, func(b core.Board, n core.Need) error {
			_, err := s.core.DoCheck(b, n)
			return err
		})
		return true
	}
	if id, ok := strings.CutPrefix(path, "/accept/"); ok {
		s.serveRow(w, r, id, true, func(n core.Need) bool { return n.Accept() != core.CheckNone }, func(b core.Board, n core.Need) error {
			_, err := s.core.DoAccept(b, n)
			return err
		})
		return true
	}
	if id, ok := strings.CutPrefix(path, "/later/"); ok {
		s.serveRow(w, r, id, true, func(n core.Need) bool { return canLater(n, time.Now()) }, func(_ core.Board, n core.Need) error {
			when := r.PostFormValue("when")
			if when != "1h" && when != "tonight" && when != "tomorrow" {
				return fmt.Errorf("%w: later takes 1h, tonight or tomorrow", errBadForm)
			}
			return s.core.SnoozeNeedIn(n, when, browserZone(r.PostFormValue("tz")))
		})
		return true
	}
	if id, ok := strings.CutPrefix(path, "/rank/"); ok {
		s.serveRow(w, r, id, false, func(n core.Need) bool { return !n.Unrouted }, func(b core.Board, n core.Need) error {
			dir := r.PostFormValue("dir")
			if dir != "top" && dir != "up" && dir != "down" {
				return fmt.Errorf("%w: rank takes top, up or down", errBadForm)
			}
			if err := s.core.RankNeed(b, n, dir); err != nil {
				return err
			}
			s.showRank(n)
			return nil
		})
		return true
	}
	if id, ok := strings.CutPrefix(path, "/reply/"); ok {
		s.serveRow(w, r, id, true, canReply, func(b core.Board, n core.Need) error {
			text := strings.TrimSpace(r.PostFormValue("text"))
			if text == "" {
				return fmt.Errorf("%w: a reply needs words", errBadForm)
			}
			_, _, err := s.core.Answer(b, n.Item.Key(), text)
			return err
		})
		return true
	}
	if id, ok := strings.CutPrefix(path, "/done/"); ok {
		s.serveRow(w, r, id, true, canDone, func(b core.Board, n core.Need) error {
			_, _, err := s.core.Answer(b, n.Item.Key(), "done")
			return err
		})
		return true
	}
	return false
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
	if !directHost(r.Host) && !s.publicHost(r.Host) {
		http.Error(w, "briefs answer only a request to an address, or to the host YAMNAYA_BRIEFS_URL names", http.StatusForbidden)
		return
	}
	path := s.route(r.URL.Path)
	if path == "/site.js" {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		fmt.Fprint(w, siteScript)
		return
	}
	if s.serveRowAction(w, r, path) {
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
		n := briefNode{ID: m.Shown(), Title: m.Name, page: func(b core.Brief) string { return o.BriefPage(m, b) }, Kind: "chief", Standing: s.standOf(m.ID), Artifact: m.ArtifactURL, Session: m.Session,
			Fresh: fresh[m.ID], Now: time.Now()}
		if g, ok := o.Goals[m.GoalID]; ok {
			n.Kind, n.Title, n.Repo = g.Shown(), g.Ask, g.Repo
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
	ref := strings.TrimPrefix(path, "/b/")
	if host, rest, ok := core.SplitRemote(ref); ok && ref != path {
		if host != s.core.SelfName() {
			s.serveRemote(w, r, host, rest)
			return
		}
		ref = rest
	}
	if path == "/" {
		var d indexData
		seen := map[string]bool{}
		for _, m := range o.Ordered() {
			parent, here := o.Managers[m.ParentID]
			if o.Active(m) && (!here || !o.Active(parent)) {
				d.Roots = append(d.Roots, build(m, 64, seen))
			}
		}
		if d.Queue, err = s.queue(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.render(w, indexPage, d)
		return
	}
	if seq, ok := strings.CutPrefix(path, "/i/"); ok {
		it, err := s.core.Store.Item("#" + seq)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.render(w, itemPage, s.itemView(it))
		return
	}
	if ref == path || ref == "" {
		http.NotFound(w, r)
		return
	}
	m, ok := findManager(o, ref)
	if !ok {
		http.NotFound(w, r)
		return
	}
	n := build(m, 64, map[string]bool{})
	n.org = o
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
	if n.Ask = o.Ask(m); len(n.Context) > 0 || n.Ask != "" {
		var sb strings.Builder
		writeContext(&sb, o, m.Shown(), n.Ask, n.Context)
		n.ContextText = clipboard.Clean(sb.String())
	}
	slices.Reverse(n.Context)
	if n.Items, err = s.items(func(it core.Item) bool {
		return it.Recipient == m.ID || it.Preparer == m.ID || (m.Session != "" && it.Session == m.Session)
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, briefPage, n)
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
	st := s.core.Store
	reads := slices.ContainsFunc(known, func(h core.KnownHost) bool { return h.Name == host })
	at, raw, kept, _ := st.KeptBoard(host)
	if !reads && !kept {
		http.NotFound(w, r)
		return
	}
	hosts := newRemoteHosts([]string{host}, remoteRead, func(h string, at time.Time, raw []byte) { st.KeepBoard(h, at, raw) })
	hosts.timeout, hosts.stored = remoteRead, keptBoardOf(st)
	if kept {
		hosts.keep(host, at, raw)
	}
	// A host that only relays here cannot be read, and the rows it last sent are all there is.
	rb := hosts.kept[host]
	if reads {
		rb = hosts.Boards()[0]
	}
	id := strings.ToLower(ref)
	for _, g := range rb.Goals {
		if g.ID == id || g.Name == id {
			id = g.ManagerID
		}
	}
	var m *core.Manager
	for i := range rb.Managers {
		if rb.Managers[i].ID == id || m == nil && rb.Managers[i].Name == id {
			m = &rb.Managers[i]
		}
	}
	if m == nil {
		http.Error(w, fmt.Sprintf("%s: no goal or chief %s on %s as of %s", rb.StateAt(time.Now()), ref, host, rb.At.Local().Format(time.Kitchen)), http.StatusNotFound)
		return
	}
	n := briefNode{ID: core.RemoteKey(host, m.Shown()), Title: m.Name, page: func(b core.Brief) string { return b.Markdown(m.Name) }, Kind: "chief", Standing: rb.Standing[m.ID], Artifact: m.ArtifactURL, Session: m.Session,
		Fresh: rb.Fresh[m.ID], Now: time.Now()}
	for _, g := range rb.Goals {
		if g.ID == m.GoalID {
			n.Kind, n.Title, n.Repo = core.RemoteKey(host, g.Shown()), g.Ask, g.Repo
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
	s.render(w, briefPage, n)
}

// prefix is the path the proxy in front publishes the site under, "" at the root.
func (s *briefSite) prefix() string {
	u, err := url.Parse(s.public)
	if err != nil {
		return ""
	}
	return strings.TrimRight(u.Path, "/")
}

// publicHost reports whether host is the one the site's public URL names.
func (s *briefSite) publicHost(host string) bool {
	u, err := url.Parse(s.public)
	if err != nil || u.Hostname() == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.EqualFold(host, u.Hostname())
}

// route is path below the site's prefix, whether the proxy in front kept the prefix or stripped it.
func (s *briefSite) route(path string) string {
	if rest, ok := strings.CutPrefix(path, s.prefix()); ok && (rest == "" || rest[0] == '/') {
		path = rest
	}
	if path == "" {
		return "/"
	}
	return path
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
	m, err := o.Lookup(ref)
	return m, err == nil
}

func (s *briefSite) render(w http.ResponseWriter, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, err := t.Clone()
	if err == nil {
		pre := s.prefix()
		err = t.Funcs(template.FuncMap{"at": func(path string) string { return pre + path }}).Execute(w, data)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// siteStyle carries the look and loads siteScript.
const siteStyle = `<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<style>
:root{--bg:#fbfaf7;--fg:#1f2328;--dim:#6a737d;--line:#e3e1db;--warn:#9a6700;--bad:#cf222e;--ok:#1a7f37;--card:#fff}
@media (prefers-color-scheme:dark){:root{--bg:#14161a;--fg:#e6e6e6;--dim:#8b949e;--line:#2d3139;--warn:#d29922;--bad:#f85149;--ok:#3fb950;--card:#1b1e23}}
body{background:var(--bg);color:var(--fg);font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0 auto;max-width:880px;padding:24px 16px;overflow-wrap:anywhere}
a{color:inherit}h1{font-size:22px;margin:0 0 4px}h2{font-size:13px;letter-spacing:.04em;color:var(--dim);margin:20px 0 4px}
.meta{color:var(--dim);font-size:13px}.tag{display:inline-block;border:1px solid var(--line);border-radius:10px;padding:0 8px;font-size:12px;margin-right:4px}
.stale,.unadopted,.quiet{color:var(--warn);border-color:var(--warn)}.stuck,.down{color:var(--bad);border-color:var(--bad)}.done,.fresh{color:var(--ok);border-color:var(--ok)}
.card{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:12px 16px;margin:12px 0}
.sec{white-space:pre-wrap;margin:0}.plan{white-space:pre-wrap;border-left:3px solid var(--line);padding-left:12px}
.copy{user-select:none;-webkit-user-select:none;font-size:12px;font-weight:normal;letter-spacing:0;color:var(--dim);background:none;border:1px solid var(--line);border-radius:6px;padding:0 8px;margin-left:8px;cursor:pointer;vertical-align:middle}
.copy:hover{color:var(--fg)}
.q{font:min(14px,calc((100vw - 32px)/24.6))/1.6 ui-monospace,SFMono-Regular,Menlo,monospace;margin:4px 0 16px;overflow-wrap:normal}.q .line{white-space:pre}.q summary{list-style:none;cursor:pointer;white-space:pre;overflow:hidden}.q summary::-webkit-details-marker{display:none}
.q .side{display:none;color:var(--dim)}@media (min-width:800px){.q .side{display:inline}}.q .open{border-left:3px solid var(--line);padding-left:8px;margin:2px 0 8px;color:var(--dim);white-space:normal}.q .open div{margin:2px 0}
.q .qrow{position:relative}.q .check{position:absolute;top:0;left:calc(39ch - 8px);font:inherit;line-height:inherit;background:none;border:0;color:var(--ok);padding:0 8px;margin:0;cursor:pointer}.q .act{font:inherit;color:var(--ok);background:none;border:1px solid var(--ok);border-radius:6px;min-height:44px;min-width:44px;padding:0 12px;cursor:pointer}.q .alt{color:var(--fg);border-color:var(--line)}.q .open form{display:flex;flex-wrap:wrap;align-items:center;gap:8px;margin:8px 0}.q .say{font:inherit;color:var(--fg);background:var(--card);border:1px solid var(--line);border-radius:6px;min-height:44px;min-width:0;flex:1 1 60%;padding:0 8px;box-sizing:border-box}@media (pointer:coarse){.q{font-size:min(14px,calc((100vw - 60px)/23.6))}.q summary{display:flex;align-items:center;min-height:44px;margin-right:-16px}.q summary .line{flex:none}.q summary .line+.line{display:flex;align-items:center;justify-content:center;min-width:44px;min-height:44px}.q .has-check summary{width:39ch;margin-right:0}.q .has-check .slot,.q .side{display:none}.q .check{left:39ch;display:flex;align-items:center;justify-content:center;min-width:44px;min-height:44px;padding:0;box-sizing:border-box}}
ul{padding-left:18px}li{margin:6px 0}
</style>
<script src="{{at "/site.js"}}" defer></script>`

// siteScript carries the copy buttons, and reloads the page every 30 seconds only while no text is selected and no queue row is
// open, so a reload never drops what the owner is reading or copying. It is a file of its own, which a proxy's same-origin script policy lets run.
const siteScript = `document.querySelectorAll('input[name="tz"]').forEach(i => { try { i.value = Intl.DateTimeFormat().resolvedOptions().timeZone } catch {} });
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
setInterval(() => { if (getSelection().isCollapsed && !document.querySelector(".q details[open]")) location.reload() }, 30000);
`

type copyButton struct{ Label, Text string }

var siteFuncs = template.FuncMap{"copy": func(label, text string) copyButton { return copyButton{label, text} }, "at": func(path string) string { return path }}

const tags = `{{define "copy"}}<button class="copy" type="button" data-copy="{{.Text}}" aria-label="Copy {{.Label}}">copy</button>{{end}}` +
	`{{define "tags"}}<span class="tag {{.Standing}}">{{.Standing}}</span>{{if .Fresh.Brief}}{{if .Fresh.Stale}}<span class="tag stale">brief v{{.Fresh.Brief.Version}} stale</span>{{else}}<span class="tag fresh">brief v{{.Fresh.Brief.Version}}, {{.Age .Fresh.Brief.CreatedAt}}</span>{{end}}{{else}}<span class="tag stale">no brief</span>{{end}}{{if .Plan}}{{if .Plan.Adopted}}<span class="tag">plan r{{.Plan.Revision}}</span>{{else}}<span class="tag unadopted">steer r{{.Plan.Revision}} not adopted</span>{{end}}{{end}}{{end}}` +
	`{{define "items"}}{{range .}}<div class="card"><a href="{{at "/i/"}}{{.Seq}}"><b>{{.Lead}}</b></a> <span class="tag">{{.Kind}}</span><span class="tag">{{.State}}</span>{{template "copy" (copy (print "item " .ID) .Text)}}<p class="sec">{{.Question}}</p><p class="meta">{{.ID}}</p></div>{{end}}{{end}}`

var indexPage = template.Must(template.New("index").Funcs(siteFuncs).Parse(tags + `<!doctype html><html><head><title>ym briefs</title>` + siteStyle + `</head><body>
<h1>Needs you</h1>
{{define "queuerow"}}<div class="qrow{{if .Check}} has-check{{end}}"><details name="queue"><summary><span class="line">{{.Line}}</span>{{if .Check}}<span class="line slot"> </span>{{else}}<span class="line">{{.Mark}}</span>{{end}}<span class="side">{{.Side}}</span></summary><div class="open">{{range .Detail}}<div>{{.}}</div>{{end}}{{if .Check}}<form id="do-{{.ID}}" method="post" action="{{at "/do/"}}{{.ID}}"><input type="hidden" name="token" value="{{.Token}}"></form>{{end}}{{if .Act}}<form method="post" action="{{at "/accept/"}}{{.ID}}"><input type="hidden" name="token" value="{{.Token}}"><button class="act" type="submit">{{.Act}}</button></form>{{end}}{{if .Done}}<form method="post" action="{{at "/done/"}}{{.ID}}"><input type="hidden" name="token" value="{{.Token}}"><button class="act" type="submit">done</button></form>{{end}}{{if .Later}}<form method="post" action="{{at "/later/"}}{{.ID}}"><input type="hidden" name="token" value="{{.Token}}"><input type="hidden" name="tz"><span>later</span><button class="act alt" type="submit" name="when" value="1h">1h</button><button class="act alt" type="submit" name="when" value="tonight">tonight</button><button class="act alt" type="submit" name="when" value="tomorrow">tomorrow</button></form>{{end}}{{if .Rank}}<form method="post" action="{{at "/rank/"}}{{.ID}}"><input type="hidden" name="token" value="{{.Token}}"><span>move</span><button class="act alt" type="submit" name="dir" value="top">top</button><button class="act alt" type="submit" name="dir" value="up">up</button><button class="act alt" type="submit" name="dir" value="down">down</button></form>{{end}}{{if .Reply}}<form method="post" action="{{at "/reply/"}}{{.ID}}"><input type="hidden" name="token" value="{{.Token}}"><input class="say" type="text" name="text" required aria-label="reply" placeholder="reply"><button class="act alt" type="submit">reply</button></form>{{end}}{{if .Command}}<div><code>{{.Command}}</code>{{template "copy" (copy "command" .Command)}}</div>{{end}}{{if .Link}}<div><a href="{{if eq (slice .Link 0 1) "/"}}{{at .Link}}{{else}}{{.Link}}{{end}}">{{.LinkText}}</a></div>{{end}}</div></details>{{if .Check}}<button class="check" type="submit" form="do-{{.ID}}" aria-label="{{.CheckLabel}}">{{.Mark}}</button>{{end}}</div>{{end}}
<div class="q"><div class="line">{{.Queue.Header}}</div>{{range .Queue.Rows}}{{template "queuerow" .}}{{end}}
{{if .Queue.Later}}<details><summary>later {{len .Queue.Later}} ▾</summary>{{range .Queue.Later}}{{template "queuerow" .}}{{end}}</details>{{end}}</div>
<h1>Briefs</h1><p class="meta">Every goal and chief ym runs. Refreshes every 30 seconds while no text is selected and no row is open.</p>
{{define "tree"}}<ul>{{range .}}<li><a href="{{at "/b/"}}{{.ID}}"><b>{{if eq .Kind "chief"}}chief {{.Title}}{{else}}{{.Kind}}{{end}}</b></a> {{if ne .Kind "chief"}}{{.Title}}{{end}}<br>{{template "tags" .}}{{if .Fresh.Brief}}{{template "copy" (copy (print .Name " brief") .Markdown)}}<div class="meta">{{.Fresh.Brief.Assessment}}</div>{{end}}{{if .Reports}}{{template "tree" .Reports}}{{end}}</li>{{end}}</ul>{{end}}
{{if .Roots}}{{template "tree" .Roots}}{{else}}<p>No goals. Start one with <code>ym goal "&lt;ask&gt;"</code>.</p>{{end}}
</body></html>`))

var itemPage = template.Must(template.New("item").Funcs(siteFuncs).Parse(tags + `<!doctype html><html><head><title>{{.Lead}} item</title>` + siteStyle + `</head><body>
<p class="meta"><a href="{{at "/"}}">all briefs</a></p>
<h1>{{.Lead}}{{template "copy" (copy (print "item " .ID) .Text)}}</h1>
<p class="meta">{{.ID}} · {{.Key}} · {{.Kind}} · {{.State}}</p>
<div class="card"><p class="sec">{{.Text}}</p></div>
</body></html>`))

var briefPage = template.Must(template.New("brief").Funcs(siteFuncs).Parse(tags + `<!doctype html><html><head><title>{{if eq .Kind "chief"}}chief {{.Title}}{{else}}{{.Kind}}{{end}} brief</title>` + siteStyle + `</head><body>
<p class="meta"><a href="{{at "/"}}">all briefs</a></p>
<h1>{{if eq .Kind "chief"}}Chief {{.Title}}{{else}}{{.Kind}}: {{.Title}}{{end}}</h1>
<p class="meta">{{.ID}}{{if .Repo}} · {{.Repo}}{{end}}{{if .Artifact}} · <a href="{{.Artifact}}">artifact</a>{{end}}</p>
<p>{{template "tags" .}}</p>
{{if .Remote}}<p class="meta">{{.Remote}}</p>{{end}}
{{if .Fresh.Stale}}{{if .Fresh.Brief}}<p class="meta">This brief predates {{.Fresh.BehindWhat}} ({{.Age .Fresh.Behind}}); its manager has been asked to refresh it.</p>{{end}}{{end}}
{{with .Shown}}<div class="card"><p class="meta">Brief v{{.Version}}, {{$.Age .CreatedAt}}{{template "copy" (copy "brief" $.Markdown)}}</p>{{range .Sections}}<h2>{{index . 0}}{{template "copy" (copy (index . 0) (index . 1))}}</h2><p class="sec">{{index . 1}}</p>{{end}}</div>
{{else}}<p>No brief recorded yet.</p>{{end}}
{{if .ContextText}}<h2>Context{{template "copy" (copy "context" .ContextText)}}</h2>
{{range .Context}}<div class="card"><p class="meta">Revision {{.Revision}} by {{$.By .}}, {{$.Age .CreatedAt}}, {{if .Adopted}}adopted{{else}}not adopted yet{{end}}{{template "copy" (copy (print "revision " .Revision) .Text)}}</p><div class="plan">{{.Text}}</div></div>
{{else}}<div class="card"><p class="meta">Revision 0, the ask it started with{{template "copy" (copy "revision 0" $.Ask)}}</p><div class="plan">{{$.Ask}}</div></div>{{end}}{{end}}
{{if .Items}}<h2>Open items</h2>{{template "items" .Items}}{{end}}
{{define "report"}}<div class="card"><a href="{{at "/b/"}}{{.ID}}"><b>{{if eq .Kind "chief"}}chief {{.Title}}{{else}}{{.Kind}}: {{.Title}}{{end}}</b></a>{{if .Fresh.Brief}}{{template "copy" (copy (print .Name " brief") .Markdown)}}{{end}}<br>{{template "tags" .}}
{{with .Fresh.Brief}}<h2>Assessment</h2><p class="sec">{{.Assessment}}</p><h2>Next</h2><p class="sec">{{.Next}}</p><h2>Waiting on</h2><p class="sec">{{.WaitingOn}}</p>{{end}}
{{range .Reports}}{{template "report" .}}{{end}}</div>{{end}}
{{if .Reports}}<h2>Reports</h2>{{range .Reports}}{{template "report" .}}{{end}}{{end}}
{{if .Versions}}<h2>Versions</h2><p class="meta">{{range .Versions}}<a href="?v={{.Version}}">v{{.Version}}</a> {{end}}</p>{{end}}
</body></html>`))
