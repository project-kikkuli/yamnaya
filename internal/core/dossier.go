package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Dossier is the evidence ym attaches to an item on its own: where it came from, the work's pull requests, what its agents did
// last, and what changed in the goal since the owner last looked.
type Dossier struct {
	Goal    string `json:"goal,omitempty"`
	GoalAsk string `json:"goal_ask,omitempty"`
	Repo    string `json:"repo,omitempty"`
	// From names who filed the item and, for an agent, the manager it works for.
	From     string         `json:"from,omitempty"`
	PRs      []PRNote       `json:"prs,omitempty"`
	Activity []ActivityNote `json:"activity,omitempty"`
	// Changes are what moved in the goal since Since, the owner's last look; a zero Since means the owner never looked.
	Changes []string  `json:"changes,omitempty"`
	Since   time.Time `json:"since,omitzero"`
	At      time.Time `json:"at,omitzero"`
}

type PRNote struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
	State string `json:"state,omitempty"`
	CI    string `json:"ci,omitempty"`
}

// Note is the pull request with its last fetched state and CI.
func (pr PR) Note() PRNote {
	n := PRNote{URL: pr.URL}
	if pr.Status != nil {
		n.Title, n.State, n.CI = pr.Status.Title, pr.Status.State, pr.Status.CI()
	}
	return n
}

func (p PRNote) String() string {
	out := p.URL
	if p.Title != "" {
		out = p.Title + "  " + p.URL
	}
	var st []string
	if p.State != "" {
		st = append(st, strings.ToLower(p.State))
	}
	if p.CI != "" {
		st = append(st, "ci "+p.CI)
	}
	if len(st) > 0 {
		out += "  (" + strings.Join(st, ", ") + ")"
	}
	return out
}

type ActivityNote struct {
	Who    string    `json:"who"`
	Status string    `json:"status"`
	What   string    `json:"what,omitempty"`
	At     time.Time `json:"at,omitzero"`
}

// Empty reports whether ym found nothing to attach.
func (d Dossier) Empty() bool {
	return d.Goal == "" && d.From == "" && len(d.PRs) == 0 && len(d.Activity) == 0 && len(d.Changes) == 0
}

const (
	dossierPRs      = 6
	dossierActivity = 5
	dossierChanges  = 8
)

// ErrNotReady rejects an ask that would reach the owner without what the owner needs to decide it.
var ErrNotReady = errors.New("not ready for the owner")

// Missing names what an owner request lacks: its recommendation, decision options, or review document.
func Missing(it Item) []string {
	var out []string
	if (it.Kind == KindDecision || it.Kind == KindDefault || it.Kind == KindReview) && strings.TrimSpace(it.Rec) == "" {
		out = append(out, "a recommendation")
	}
	if it.Kind == KindReview && (len(it.Evidence) == 0 || strings.TrimSpace(it.Evidence[0]) == "") {
		out = append(out, "a document link or path")
	}
	if it.Kind == KindDecision {
		n := 0
		for _, o := range it.Options {
			if strings.TrimSpace(o.Label) != "" && strings.TrimSpace(o.Effect) != "" {
				n++
			}
		}
		if n < 2 {
			out = append(out, "two options with their effects")
		}
	}
	return out
}

// Unprepared is what an open ask that climbed to the owner past a stuck or retired manager still lacks; an ask the owner's own
// session filed, or one filed before ym refused thin asks, has no one to prepare it and is never unprepared.
func Unprepared(it Item) []string {
	if it.State != ItemOpen || it.Recipient != Owner || it.Preparer == "" {
		return nil
	}
	return Missing(it)
}

var missingFlag = map[string]string{
	"a recommendation":               "--rec \"<what you recommend and why>\"",
	"two options with their effects": "at least two --option \"<label>=<what happens if picked>\"",
	"a document link or path":        "--evidence \"<document link or path>\"",
}

// ready refuses an ask bound for the owner that is missing anything the owner needs to decide it.
func ready(it Item) error {
	missing := Missing(it)
	if len(missing) == 0 {
		return nil
	}
	var flags []string
	for _, m := range missing {
		flags = append(flags, missingFlag[m])
	}
	return fmt.Errorf("%w: this ask goes to the owner, who takes only calls ready to decide; refile it with %s, and --evidence <PR, CI run, commit or file> and --tried \"<what you tried>\" where they apply. If the call is within your authority, make it yourself instead",
		ErrNotReady, strings.Join(flags, " and "))
}

// itemDossiers adds the columns for an item's evidence, what was tried, ym's dossier and who was told to prepare it.
func itemDossiers(db *sql.DB) error {
	for col, def := range map[string]string{
		"evidence": `TEXT NOT NULL DEFAULT '[]'`,
		"tried":    `TEXT NOT NULL DEFAULT ''`,
		"dossier":  `TEXT NOT NULL DEFAULT ''`,
		"preparer": `TEXT NOT NULL DEFAULT ''`,
	} {
		var has int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('ym_items') WHERE name = ?`, col).Scan(&has); err != nil {
			return err
		}
		if has == 0 {
			if _, err := db.Exec(`ALTER TABLE ym_items ADD COLUMN ` + col + ` ` + def); err != nil {
				return err
			}
		}
	}
	return nil
}

// SaveDossier replaces the dossier of item seq.
func (s *Store) SaveDossier(seq int64, d Dossier) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE ym_items SET dossier = ? WHERE seq = ?`, string(raw), seq)
	return err
}

// Looked records that the owner has read goal's news, so later asks report only what happens after now.
func (c *Core) Looked(goal string) error {
	if goal == "" {
		return nil
	}
	return c.Store.MarkSeen(goal, c.now())
}

// work is the agent sessions whose work an item stands on: its manager's tree, every tree below a chief, else the filer's mission.
// Managers' and chiefs' own sessions are left out; the dossier names them in From.
func (c *Core) work(b Board, o Org, session string) (Manager, bool, []Live) {
	parents := c.parentsFor(b, nil)
	m, managed := o.ManagerOf(session)
	if !managed {
		m, managed = o.Managed(session, parents)
	}
	var out []Live
	if !managed {
		if p, ok := b.Project(b.ProjectOf(session)); ok {
			out = append(out, p.Sessions...)
		} else if s, ok := b.Session(session); ok {
			out = append(out, s)
		}
		return m, false, out
	}
	in := map[string]bool{}
	for _, below := range o.Subtree(m.ID) {
		in[below.ID] = true
	}
	for _, s := range b.Sessions {
		if owner, ok := o.Managed(s.Key(), parents); ok && in[owner.ID] && o.Sessions[s.Key()] == "" {
			out = append(out, s)
		}
	}
	return m, true, out
}

func titleFor(s Live) string {
	if s.Title != "" {
		return clip(oneLine(s.Title), 60)
	}
	return Short(s.Key())
}

// changes lists the goal's news the owner has not read, the same events the feed counts unread, oldest first; since is the
// owner's last read, zero when the owner never read the goal.
func (c *Core) changes(goal string) (time.Time, []string, error) {
	events, err := c.Store.GoalEvents(goal)
	if err != nil {
		return time.Time{}, nil, err
	}
	var since time.Time
	var out []string
	for _, e := range events {
		if !e.Seen.IsZero() {
			if e.Seen.After(since) {
				since = e.Seen
			}
			continue
		}
		if !e.OwnerNews() {
			continue
		}
		line := oneLine(e.Payload)
		if e.Kind == EventUpdate {
			line = "manager: " + line
		}
		if line == "" {
			line = e.Kind
		}
		out = append(out, clip(line, 160))
	}
	if len(out) > dossierChanges {
		more := len(out) - dossierChanges + 1
		out = append(out[:dossierChanges-1], fmt.Sprintf("and %d more", more))
	}
	return since, out, nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// dossier gathers, without any model, the evidence an ask from it.Session carries: its goal and manager, the work's pull requests
// with their CI, its agents' latest activity, and the goal's changes since the owner last looked.
func (c *Core) dossier(b Board, o Org, it Item) (Dossier, error) {
	d := Dossier{At: c.now()}
	m, managed, sessions := c.work(b, o, c.Resolve(it.Session))
	switch {
	case managed && o.Sessions[c.Resolve(it.Session)] == m.ID:
		d.From = m.Label()
	case managed:
		d.From = "agent " + agentName(b, it.Session) + ", working for " + m.Label()
	default:
		d.From = "agent " + agentName(b, it.Session) + ", in a session you started"
	}
	if g, ok := o.Goals[m.GoalID]; managed && ok {
		d.Goal, d.GoalAsk, d.Repo = g.ID, g.Ask, g.Repo
	}
	seenPR := map[string]bool{}
	for _, s := range sessions {
		for _, pr := range s.AllPRs() {
			if seenPR[pr.URL] {
				continue
			}
			seenPR[pr.URL] = true
			d.PRs = append(d.PRs, pr.Note())
		}
	}
	sort.SliceStable(d.PRs, func(i, j int) bool { return openFirst(d.PRs[i]) && !openFirst(d.PRs[j]) })
	if len(d.PRs) > dossierPRs {
		d.PRs = d.PRs[:dossierPRs]
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].Since.After(sessions[j].Since) })
	for _, s := range sessions {
		if len(d.Activity) == dossierActivity {
			break
		}
		what := s.Doing.String()
		if s.Status != Busy && s.Result != "" {
			what = s.Result
		}
		d.Activity = append(d.Activity, ActivityNote{Who: titleFor(s), Status: s.Status.String(), What: clip(oneLine(what), 140), At: s.Since})
	}
	if d.Goal != "" {
		var err error
		if d.Since, d.Changes, err = c.changes(d.Goal); err != nil {
			return d, err
		}
	}
	return d, nil
}

func openFirst(p PRNote) bool { return p.State == "" || p.State == "OPEN" }

func agentName(b Board, session string) string {
	if s, ok := b.Session(session); ok {
		return titleFor(s)
	}
	return Short(session)
}

// Brief refreshes the dossier of every open ask waiting on the owner, so its pull requests, activity and changes stay current.
func (c *Core) Brief(b Board) error {
	items, err := c.Store.Items(ItemFilter{})
	if err != nil {
		return err
	}
	o := b.Org
	if o.Managers == nil {
		if o, err = c.LoadOrg(); err != nil {
			return err
		}
	}
	for _, it := range items {
		if it.State != ItemOpen || it.Recipient != Owner {
			continue
		}
		d, err := c.dossier(b, o, it)
		if err != nil {
			return err
		}
		d.At = it.Dossier.At
		if same(d, it.Dossier) {
			continue
		}
		d.At = c.now()
		if err := c.Store.SaveDossier(it.Seq, d); err != nil {
			return err
		}
	}
	return nil
}

func same(a, b Dossier) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// DossierLines are the dossier as labelled lines, for plain-text views.
func DossierLines(d Dossier) [][2]string {
	var out [][2]string
	add := func(k, v string) {
		if v != "" {
			out = append(out, [2]string{k, v})
		}
	}
	if d.Goal != "" {
		add("goal", d.Goal+" "+oneLine(d.GoalAsk))
	}
	add("repository", d.Repo)
	add("from", d.From)
	for _, pr := range d.PRs {
		add("pr", pr.String())
	}
	for _, a := range d.Activity {
		add("activity", a.String())
	}
	if d.Goal != "" {
		head := "since you last looked"
		if d.Since.IsZero() {
			head = "since the goal began"
		}
		if len(d.Changes) == 0 {
			add(head, "nothing moved")
		}
		for _, ch := range d.Changes {
			add(head, ch)
		}
	}
	return out
}

func (a ActivityNote) String() string {
	out := a.Who + " " + a.Status
	if a.What != "" {
		out += ": " + a.What
	}
	return out
}

// Prepared is what a manager adds to an ask so the owner can decide it; empty fields keep what the ask has.
type Prepared struct {
	Rec, Tried, Assumption string
	Options                []Option
	Evidence               []string
}

func (p Prepared) flags() []string {
	var out []string
	for _, f := range [][2]string{{"--rec", p.Rec}, {"--tried", p.Tried}, {"--assumption", p.Assumption}} {
		if f[1] != "" {
			out = append(out, f[0], f[1])
		}
	}
	for _, o := range p.Options {
		out = append(out, "--option", o.Label+"="+o.Effect)
	}
	for _, e := range p.Evidence {
		out = append(out, "--evidence", e)
	}
	return out
}

// Prepare adds p to the open ask ref names and refreshes its dossier.
func (c *Core) Prepare(b Board, ref string, p Prepared) (Item, error) {
	if it, _, remote, err := c.remoteItem(ref, append([]string{"prepare"}, p.flags()...)...); remote {
		return it, err
	}
	it, err := c.Store.Item(ref)
	if err != nil {
		return it, err
	}
	if it.State != ItemOpen {
		return it, fmt.Errorf("item %s is %s; only an open ask can be prepared", it.ID, it.State)
	}
	if p.Rec != "" {
		it.Rec = p.Rec
	}
	if p.Tried != "" {
		it.Tried = p.Tried
	}
	if p.Assumption != "" {
		it.Assumption = p.Assumption
	}
	if len(p.Options) > 0 {
		it.Options = p.Options
	}
	it.Evidence = append(it.Evidence, p.Evidence...)
	if it.Recipient == Owner {
		if err := ready(it); err != nil {
			return it, err
		}
	}
	if err := c.Store.prepare(it); err != nil {
		return it, err
	}
	o, err := c.LoadOrg()
	if err != nil {
		return it, err
	}
	d, err := c.dossier(b, o, it)
	if err != nil {
		return it, err
	}
	if err := c.Store.SaveDossier(it.Seq, d); err != nil {
		return it, err
	}
	return c.Store.Item(it.Key())
}

func (s *Store) prepare(it Item) error {
	opts, err := json.Marshal(it.Options)
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(it.Evidence)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE ym_items SET rec = ?, options = ?, tried = ?, assumption = ?, evidence = ?, preparer = '' WHERE seq = ?`,
		it.Rec, string(opts), it.Tried, it.Assumption, string(evidence), it.Seq); err != nil {
		return err
	}
	if err := addEvent(tx, it.Key(), ItemOpen, ItemOpen, "prepared"); err != nil {
		return err
	}
	return tx.Commit()
}

// SetPreparer records the manager or chief told to prepare an ask that reached the owner unprepared.
func (s *Store) SetPreparer(seq int64, who string) error {
	_, err := s.db.Exec(`UPDATE ym_items SET preparer = ? WHERE seq = ?`, who, seq)
	return err
}

// prepareText tells the manager or chief an ask climbed past it, while it was down, to the owner without what the owner needs.
func prepareText(it Item, missing []string) string {
	return fmt.Sprintf("ym: %s %s (%q) reached the owner while you were down, without %s. Prepare it now so the owner can decide: "+
		"ym item prepare '%s' --rec \"<what you recommend and why>\" --option \"<label>=<effect>\" --option \"<label>=<effect>\" --evidence <PR, CI run, commit or file> --tried \"<what was tried>\". "+
		"If you can settle it yourself, tell the owner your call in --rec.",
		it.Key(), it.ID, oneLine(it.Question), strings.Join(missing, " and "), it.Key())
}
