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

// Missing names what an ask bound for the owner lacks to be decided: a recommendation for a decision or default, and for a decision two options with effects.
func Missing(it Item) []string {
	var out []string
	if (it.Kind == KindDecision || it.Kind == KindDefault) && strings.TrimSpace(it.Rec) == "" {
		out = append(out, "a recommendation")
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

var missingFlag = map[string]string{
	"a recommendation":               "--rec \"<what you recommend and why>\"",
	"two options with their effects": "at least two --option \"<label>=<what happens if picked>\"",
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

// itemDossiers adds the columns for an item's evidence, what was tried and ym's dossier, and the owner's last look at each goal.
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
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS ym_goal_seen (goal TEXT PRIMARY KEY, at TEXT NOT NULL, snapshot TEXT NOT NULL)`)
	return err
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

// goalSnapshot is what the owner saw of a goal: each pull request's state, each agent's status and result, each item's state.
type goalSnapshot struct {
	State  string            `json:"state"`
	PRs    map[string]string `json:"prs"`
	Agents map[string]string `json:"agents"`
	Items  map[string]string `json:"items"`
}

func (s *Store) seen(goal string) (time.Time, goalSnapshot, error) {
	var at, raw string
	snap := goalSnapshot{}
	err := s.db.QueryRow(`SELECT at, snapshot FROM ym_goal_seen WHERE goal = ?`, goal).Scan(&at, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, snap, nil
	}
	if err != nil {
		return time.Time{}, snap, err
	}
	t, _ := time.Parse(time.RFC3339Nano, at)
	return t, snap, json.Unmarshal([]byte(raw), &snap)
}

func (s *Store) markSeen(goal string, at time.Time, snap goalSnapshot) error {
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO ym_goal_seen (goal, at, snapshot) VALUES (?, ?, ?)
		ON CONFLICT(goal) DO UPDATE SET at = excluded.at, snapshot = excluded.snapshot`, goal, at.UTC().Format(time.RFC3339Nano), string(raw))
	return err
}

// Looked records that the owner has seen goal as it stands on b, so later asks report only what changed after now.
func (c *Core) Looked(b Board, goal string) error {
	if goal == "" {
		return nil
	}
	o := b.Org
	if o.Managers == nil {
		var err error
		if o, err = c.LoadOrg(); err != nil {
			return err
		}
	}
	m, ok := o.Managers[o.Goals[goal].ManagerID]
	if !ok {
		return nil
	}
	items, err := c.Store.Items(ItemFilter{})
	if err != nil {
		return err
	}
	return c.Store.markSeen(goal, c.now(), c.snapshot(b, o, m, items))
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

func (c *Core) snapshot(b Board, o Org, m Manager, items []Item) goalSnapshot {
	snap := goalSnapshot{State: o.Goals[m.GoalID].State, PRs: map[string]string{}, Agents: map[string]string{}, Items: map[string]string{}}
	_, _, sessions := c.work(b, o, m.Session)
	for _, s := range sessions {
		snap.Agents[s.Key()] = s.Status.String() + "\t" + titleFor(s) + "\t" + oneLine(s.Result)
		for _, pr := range s.AllPRs() {
			snap.PRs[pr.URL] = prLine(pr)
		}
	}
	keys := map[string]bool{}
	for _, s := range sessions {
		keys[s.Key()] = true
	}
	for _, it := range items {
		if keys[it.Session] || o.Sessions[it.Session] == m.ID {
			snap.Items[it.Key()] = it.State + "\t" + it.ID + "\t" + oneLine(it.Question) + "\t" + oneLine(it.Answer+it.Reason)
		}
	}
	return snap
}

func prLine(pr PR) string {
	if pr.Status == nil {
		return ""
	}
	return pr.Status.State + "\t" + pr.Status.CI()
}

func titleFor(s Live) string {
	if s.Title != "" {
		return clip(oneLine(s.Title), 60)
	}
	return Short(s.Key())
}

// changes lists what differs between the owner's last look and now, leaving out the item being asked.
func changes(was, now goalSnapshot, skip string) []string {
	var out []string
	if was.State != "" && was.State != now.State {
		out = append(out, "goal went "+was.State+" -> "+now.State)
	}
	for _, url := range sortedKeys(now.PRs) {
		st, ci, _ := strings.Cut(now.PRs[url], "\t")
		prev, had := was.PRs[url]
		pst, pci, _ := strings.Cut(prev, "\t")
		switch {
		case !had:
			out = append(out, "opened "+url+withCI(st, ci))
		case pst != st && st != "":
			out = append(out, strings.ToLower(st)+" "+url)
		case pci != ci && ci != "":
			out = append(out, "ci "+orNone(pci)+" -> "+ci+" on "+url)
		}
	}
	for _, key := range sortedKeys(now.Agents) {
		st, rest, _ := strings.Cut(now.Agents[key], "\t")
		title, result, _ := strings.Cut(rest, "\t")
		prev, had := was.Agents[key]
		_, prest, _ := strings.Cut(prev, "\t")
		_, presult, _ := strings.Cut(prest, "\t")
		switch {
		case !had:
			out = append(out, "agent "+title+" started ("+st+")")
		case result != "" && result != presult:
			out = append(out, "agent "+title+" finished: "+clip(result, 100))
		}
	}
	for _, key := range sortedKeys(was.Agents) {
		if _, ok := now.Agents[key]; !ok {
			_, rest, _ := strings.Cut(was.Agents[key], "\t")
			title, _, _ := strings.Cut(rest, "\t")
			out = append(out, "agent "+title+" ended")
		}
	}
	for _, key := range sortedKeys(now.Items) {
		if key == skip {
			continue
		}
		parts := strings.SplitN(now.Items[key], "\t", 4)
		for len(parts) < 4 {
			parts = append(parts, "")
		}
		prev, had := was.Items[key]
		pstate, _, _ := strings.Cut(prev, "\t")
		switch {
		case !had:
			out = append(out, parts[1]+" asked: "+clip(parts[2], 100))
			if parts[0] != ItemOpen {
				out = append(out, parts[1]+" "+parts[0]+": "+clip(parts[3], 100))
			}
		case pstate != parts[0]:
			out = append(out, parts[1]+" "+parts[0]+": "+clip(parts[3], 100))
		}
	}
	if len(out) > dossierChanges {
		more := len(out) - dossierChanges + 1
		out = append(out[:dossierChanges-1], fmt.Sprintf("and %d more", more))
	}
	return out
}

func withCI(state, ci string) string {
	var st []string
	if state != "" && state != "OPEN" {
		st = append(st, strings.ToLower(state))
	}
	if ci != "" {
		st = append(st, "ci "+ci)
	}
	if len(st) == 0 {
		return ""
	}
	return " (" + strings.Join(st, ", ") + ")"
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// dossier gathers, without any model, the evidence an ask from it.Session carries: its goal and manager, the work's pull requests
// with their CI, its agents' latest activity, and the goal's changes since the owner last looked.
func (c *Core) dossier(b Board, o Org, it Item, items []Item) (Dossier, error) {
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
		d.Goal, d.GoalAsk = g.ID, g.Ask
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
		since, was, err := c.Store.seen(d.Goal)
		if err != nil {
			return d, err
		}
		d.Since = since
		gm := o.Managers[o.Goals[d.Goal].ManagerID]
		d.Changes = changes(was, c.snapshot(b, o, gm, items), it.Key())
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
		d, err := c.dossier(b, o, it, items)
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

// Prepare adds p to the open ask ref names and refreshes its dossier.
func (c *Core) Prepare(b Board, ref string, p Prepared) (Item, error) {
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
	items, err := c.Store.Items(ItemFilter{})
	if err != nil {
		return it, err
	}
	o, err := c.LoadOrg()
	if err != nil {
		return it, err
	}
	d, err := c.dossier(b, o, it, items)
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
