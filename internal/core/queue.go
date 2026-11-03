package core

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// What an item says it touches, as ym item add --impact takes it.
const (
	ImpactProd     = "prod"
	ImpactClinical = "clinical"
	ImpactAgents   = "agents"
	ImpactRoutine  = "routine"
)

// MaxDoLine is the most a do-line may hold, so it fits a row of QueueWidth columns.
const MaxDoLine = 28

// QueueWidth is the columns of a queue row, the width of a phone.
const QueueWidth = 40

// checkQueueFields refuses what ym item add and prepare cannot rank or draw.
func checkQueueFields(do, impact string, minutes int) error {
	if n := len([]rune(do)); n > MaxDoLine {
		return fmt.Errorf("--do holds %d characters; at most %d, an imperative that fits the row", n, MaxDoLine)
	}
	switch impact {
	case "", ImpactProd, ImpactClinical, ImpactAgents, ImpactRoutine:
	default:
		return fmt.Errorf("--impact %q: want prod, clinical, agents or routine", impact)
	}
	if minutes < 0 {
		return fmt.Errorf("--minutes %d: want a positive estimate, or none", minutes)
	}
	return nil
}

// queueItems adds the columns that rank items and wake them from a snooze, and the same marks for rows that are not items.
func queueItems(db *sql.DB) error {
	for _, col := range []struct{ name, def string }{
		{"do_line", `TEXT NOT NULL DEFAULT ''`},
		{"impact", `TEXT NOT NULL DEFAULT ''`},
		{"minutes", `INTEGER NOT NULL DEFAULT 0`},
		{"pin", `INTEGER NOT NULL DEFAULT 0`},
		{"pin_owner", `INTEGER NOT NULL DEFAULT 0`},
		{"snooze_until", `TEXT`},
	} {
		var has int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('ym_items') WHERE name = ?`, col.name).Scan(&has); err != nil {
			return err
		}
		if has > 0 {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE ym_items ADD COLUMN ` + col.name + ` ` + col.def); err != nil {
			return err
		}
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS ym_queue_marks (
		key TEXT PRIMARY KEY, pin INTEGER NOT NULL DEFAULT 0, pin_owner INTEGER NOT NULL DEFAULT 0, snooze_until TEXT)`)
	return err
}

// QueueMark is what the owner or a chief set on a row: Pin above zero ranks it over the score, below zero under it; Owner says the owner set Pin.
type QueueMark struct {
	Pin   int
	Owner bool
	Until time.Time
}

func (s *Store) queueMarks() (map[string]QueueMark, error) {
	return remembered(s, "queue marks", cloneMap[string, QueueMark], func() (map[string]QueueMark, error) {
		rows, err := s.db.Query(`SELECT key, pin, pin_owner, snooze_until FROM ym_queue_marks`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]QueueMark{}
		for rows.Next() {
			var key string
			var m QueueMark
			var until sql.NullString
			if err := rows.Scan(&key, &m.Pin, &m.Owner, &until); err != nil {
				return nil, err
			}
			m.Until = parseTime(until)
			out[key] = m
		}
		return out, rows.Err()
	})
}

// SnoozeUntil is when "1h", "tonight" (t) or "tomorrow" (m) ends, counted from now on the local clock; any other Go duration also works.
func SnoozeUntil(now time.Time, when string) (time.Time, error) {
	local := now.Local()
	at := func(day, hour int) time.Time {
		return time.Date(local.Year(), local.Month(), local.Day()+day, hour, 0, 0, 0, local.Location())
	}
	switch strings.ToLower(strings.TrimSpace(when)) {
	case "t", "tonight":
		if local.Hour() >= 20 {
			return at(1, 20), nil
		}
		return at(0, 20), nil
	case "m", "tomorrow":
		return at(1, 9), nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(when))
	if err != nil || d <= 0 {
		return time.Time{}, fmt.Errorf("later %q: want 1h, tonight or tomorrow", when)
	}
	return now.Add(d), nil
}

// SnoozeItem snoozes the item ref names until a time, or until reopened when until is zero; an item already snoozed takes the new time.
func (s *Store) SnoozeItem(ref string, until time.Time) (Item, error) {
	defer s.invalidate()
	it, err := s.Item(ref)
	if err != nil {
		return it, err
	}
	if it.State != ItemSnoozed {
		if it, err = s.Transition(ref, ItemSnoozed, ""); err != nil {
			return it, err
		}
	}
	var at any
	if !until.IsZero() {
		at = until.UTC().Format(time.RFC3339Nano)
	}
	if _, err := s.db.Exec(`UPDATE ym_items SET snooze_until = ? WHERE seq = ?`, at, it.Seq); err != nil {
		return it, err
	}
	return s.Item(it.Key())
}

// wakeSnoozed reopens every item whose snooze ended.
func (s *Store) wakeSnoozed(now time.Time) error {
	rows, err := s.db.Query(`SELECT seq, snooze_until FROM ym_items WHERE state = ? AND snooze_until IS NOT NULL`, ItemSnoozed)
	if err != nil {
		return err
	}
	var due []int64
	for rows.Next() {
		var seq int64
		var until sql.NullString
		if err := rows.Scan(&seq, &until); err != nil {
			rows.Close()
			return err
		}
		if !parseTime(until).After(now) {
			due = append(due, seq)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, seq := range due {
		if _, err := s.Transition(fmt.Sprintf("#%d", seq), ItemOpen, "snooze ended"); err != nil {
			return err
		}
	}
	return nil
}

// SnoozeRow hides a row that is not an item until a time; a new head or review update does not bring it back early.
func (s *Store) SnoozeRow(key string, until time.Time) error {
	defer s.invalidate()
	_, err := s.db.Exec(`INSERT INTO ym_queue_marks (key, snooze_until) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET snooze_until = excluded.snooze_until`, key, until.UTC().Format(time.RFC3339Nano))
	return err
}

// WakeRow returns a snoozed row that is not an item to the queue.
func (s *Store) WakeRow(key string) error {
	defer s.invalidate()
	_, err := s.db.Exec(`UPDATE ym_queue_marks SET snooze_until = NULL WHERE key = ?`, key)
	return err
}

// rankPin is the pin a rank of top, up or down gives: top goes over every pin of the same hand, up and down move one step from the
// row's pin if the same hand set it, else from none. A chief's rank leaves a row the owner ranked alone, reported by false.
func rankPin(cur QueueMark, dir string, owner bool, highest int) (int, bool, error) {
	if cur.Owner && !owner {
		return cur.Pin, false, nil
	}
	base := cur.Pin
	if cur.Owner != owner {
		base = 0
	}
	switch dir {
	case "top":
		return highest + 1, true, nil
	case "up":
		return base + 1, true, nil
	case "down":
		return base - 1, true, nil
	}
	return 0, false, fmt.Errorf("rank %q: want top, up or down", dir)
}

func (s *Store) highestPin(owner bool) (int, error) {
	var top int
	err := s.db.QueryRow(`SELECT max(0, COALESCE((SELECT max(pin) FROM ym_items WHERE pin_owner = ?1), 0),
		COALESCE((SELECT max(pin) FROM ym_queue_marks WHERE pin_owner = ?1), 0))`, owner).Scan(&top)
	return top, err
}

// RankItem pins the item ref names; false means the owner already ranked it and a chief cannot.
func (s *Store) RankItem(ref, dir string, owner bool) (Item, bool, error) {
	defer s.invalidate()
	it, err := s.Item(ref)
	if err != nil {
		return it, false, err
	}
	top, err := s.highestPin(owner)
	if err != nil {
		return it, false, err
	}
	pin, ok, err := rankPin(QueueMark{Pin: it.Pin, Owner: it.PinOwner}, dir, owner, top)
	if err != nil || !ok {
		return it, false, err
	}
	if _, err := s.db.Exec(`UPDATE ym_items SET pin = ?, pin_owner = ? WHERE seq = ?`, pin, owner && pin != 0, it.Seq); err != nil {
		return it, false, err
	}
	it, err = s.Item(it.Key())
	return it, true, err
}

// RankRow pins a row that is not an item, by its MarkKey; false means the owner already ranked it and a chief cannot.
func (s *Store) RankRow(key, dir string, owner bool) (bool, error) {
	defer s.invalidate()
	marks, err := s.queueMarks()
	if err != nil {
		return false, err
	}
	top, err := s.highestPin(owner)
	if err != nil {
		return false, err
	}
	pin, ok, err := rankPin(marks[key], dir, owner, top)
	if err != nil || !ok {
		return false, err
	}
	_, err = s.db.Exec(`INSERT INTO ym_queue_marks (key, pin, pin_owner) VALUES (?1, ?2, ?3)
		ON CONFLICT(key) DO UPDATE SET pin = ?2, pin_owner = ?3`, key, pin, owner && pin != 0)
	return err == nil, err
}

// SnoozeNeed sends the row to later until the time when names; an item on another host counts when there.
func (c *Core) SnoozeNeed(n Need, when string) error {
	if n.Item != nil {
		_, err := c.SnoozeFor(n.Item.Key(), when)
		return err
	}
	until, err := SnoozeUntil(c.now(), when)
	if err != nil {
		return err
	}
	return c.Store.SnoozeRow(n.MarkKey(), until)
}

// SnoozeFor snoozes an item until the time when names, or until reopened when when is empty.
func (c *Core) SnoozeFor(id, when string) (Item, error) {
	if when == "" {
		return c.Snooze(id)
	}
	if it, _, ok, err := c.remoteItem(id, "snooze", "--for", when); ok {
		it.State = ItemSnoozed
		return it, err
	}
	until, err := SnoozeUntil(c.now(), when)
	if err != nil {
		return Item{}, err
	}
	return c.Store.SnoozeItem(id, until)
}

// Rank pins an item here or on its host; false means the owner already ranked it and a chief cannot.
func (c *Core) Rank(id, dir string, owner bool) (Item, bool, error) {
	args := []string{"rank", dir}
	if !owner {
		args = append(args, "--chief")
	}
	if it, _, ok, err := c.remoteItem(id, args...); ok {
		return it, err == nil, err
	}
	return c.Store.RankItem(id, dir, owner)
}

// RankNeed is the owner's reorder of one row.
func (c *Core) RankNeed(n Need, dir string) error {
	if n.Item != nil {
		_, _, err := c.Rank(n.Item.Key(), dir, true)
		return err
	}
	_, err := c.Store.RankRow(n.MarkKey(), dir, true)
	return err
}

// PRMerger merges a pull request at the head the owner saw.
type PRMerger interface {
	Merge(url, head string) error
}

// Check is what a row's check mark does.
type Check int

const (
	// CheckNone opens the row: the row does not say a thing the mark could do.
	CheckNone Check = iota
	CheckGo
	CheckAccept
	CheckMerge
)

// Check is what the check mark does for this row. An item answers with its rec only when its do-line states that rec, so nothing hidden
// is answered; a pull request merges only when the owner's account wrote it.
func (n Need) Check() Check {
	switch {
	case n.PR != nil:
		if n.mergeable() {
			return CheckMerge
		}
	case n.Item != nil && n.Item.Action == "":
		it := n.Item
		switch {
		case it.Kind == KindGo:
			return CheckGo
		case (it.Kind == KindDecision || it.Kind == KindDefault) && it.DoLine != "" && it.Rec != "":
			return CheckAccept
		}
	}
	return CheckNone
}

// mergeable reports a merge row: the board decides which pull requests are ready, and only the owner's own ever merge.
func (n Need) mergeable() bool {
	return n.PR != nil && n.PR.Status != nil && n.PR.Status.OwnedPR()
}

// Accept is what the row's accept key does once the row is open and shows the rec: the check mark, or answering with a rec the
// closed row did not state.
func (n Need) Accept() Check {
	if c := n.Check(); c != CheckNone {
		return c
	}
	if it := n.Item; it != nil && it.Action == "" && (it.Kind == KindDecision || it.Kind == KindDefault) && it.Rec != "" {
		return CheckAccept
	}
	return CheckNone
}

// DoCheck carries out what the row's accept key says, through the same paths ym item go and ym item answer take.
func (c *Core) DoCheck(b Board, n Need) (string, error) {
	switch n.Accept() {
	case CheckGo:
		it, d, err := c.Go(b, n.Item.Key())
		return it.ID + " answered go ahead, " + d.String(), err
	case CheckAccept:
		it, d, err := c.Answer(b, n.Item.Key(), n.Item.Rec)
		return it.ID + " answered with its rec, " + d.String(), err
	case CheckMerge:
		m, ok := c.PRs.(PRMerger)
		if !ok {
			return "", fmt.Errorf("this ym cannot merge %s", n.PR.URL)
		}
		if err := m.Merge(n.PR.URL, n.PR.Status.Merge.OID); err != nil {
			return "", err
		}
		return "merged " + n.PR.URL, c.Store.DismissQueue(n.Key())
	}
	return "", fmt.Errorf("%s has nothing to accept; open it", n.ID())
}

// RowID names what the owner read on this row: the row, and the words and head an action on it would carry out. Any change to those
// gives a new id, so an action sent from an older page finds nothing to act on.
func (n Need) RowID() string {
	parts := []string{n.Key(), n.Verb(), n.DoLine()}
	if n.Item != nil {
		parts = append(parts, n.Item.Key(), n.Item.Kind, n.Item.Rec)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:12])
}

// MarkKey names the row across changes of its head or review update, so a snooze or pin holds through them. A pull request has one
// key whichever kind of row shows it.
func (n Need) MarkKey() string {
	switch {
	case n.PR != nil:
		return "pr:" + n.PR.URL
	case n.Review != nil:
		return "pr:" + n.Review.URL
	case n.Doc != nil:
		return "doc:" + n.Doc.Page
	}
	return n.Key()
}

func (n Need) mark() QueueMark {
	if n.Item != nil {
		return QueueMark{Pin: n.Item.Pin, Owner: n.Item.PinOwner, Until: n.Item.SnoozeUntil}
	}
	return n.Mark
}

// tier orders pins: the owner's over a chief's over the score, and the reverse below it.
func (n Need) tier() int {
	m := n.mark()
	t := 0
	switch {
	case m.Pin > 0:
		t = 1
	case m.Pin < 0:
		t = -1
	}
	if m.Owner {
		t *= 2
	}
	return t
}

// Urgent reports whether the row touches production or clinical work.
func (n Need) Urgent() bool {
	return n.Item != nil && (n.Item.Impact == ImpactProd || n.Item.Impact == ImpactClinical)
}

func (n Need) blocked() int {
	switch {
	case n.Item != nil:
		if len(n.Asks) > 1 {
			total := 0
			for _, a := range n.Asks {
				total += a.Blocks
			}
			return total
		}
		return n.Item.Blocks
	case n.Goal != nil, n.Steer != nil, n.Relay != nil, n.Session != nil:
		return 1
	}
	return 0
}

// Since is when the row began waiting, zero when nothing says.
func (n Need) Since() time.Time {
	switch {
	case n.Item != nil:
		return n.Item.CreatedAt
	case n.Goal != nil:
		return n.Goal.LastMovedAt
	case n.Steer != nil:
		return n.Steer.RaisedAt
	case n.Relay != nil:
		return n.Relay.RefusedSince
	case n.PR != nil && n.PR.Status != nil:
		return n.PR.Status.Merge.Opened
	case n.Review != nil:
		if !n.Review.Opened.IsZero() {
			return n.Review.Opened
		}
		at, _ := time.Parse(time.RFC3339, n.Review.Updated)
		return at
	case n.Doc != nil:
		at, _ := time.Parse(time.RFC3339, n.Doc.At)
		return at
	}
	return time.Time{}
}

// reviewMinutes estimates reading a diff: ten added lines a minute, a hundred deleted, two minutes at least. Zero is an unknown size.
func reviewMinutes(additions, deletions int) int {
	if additions+deletions == 0 {
		return 0
	}
	return max(2, (additions+9)/10+(deletions+99)/100)
}

// Minutes is the row's time to do, zero when nothing honest can be said.
func (n Need) Minutes() int {
	switch {
	case n.Item != nil:
		if len(n.Asks) > 1 {
			total := 0
			for i := range n.Asks {
				m := Need{Item: &n.Asks[i]}.Minutes()
				if m == 0 {
					return 0
				}
				total += m
			}
			return total
		}
		if n.Item.Minutes > 0 {
			return n.Item.Minutes
		}
		switch n.Item.Kind {
		case KindGo:
			return 1
		case KindDecision, KindDefault:
			return 3
		}
	case n.PR != nil:
		if n.mergeable() {
			return 1
		}
		if st := n.PR.Status; st != nil {
			return reviewMinutes(st.Merge.Additions, st.Merge.Deletions)
		}
	case n.Review != nil:
		return reviewMinutes(n.Review.Additions, n.Review.Deletions)
	case n.Session != nil:
		return 1
	}
	return 0
}

// Score ranks a row: what it touches and blocks, how long it has waited, and whether it is a quick win.
func (n Need) Score(now time.Time) int {
	impact := 20 * n.blocked()
	if n.Urgent() {
		impact += 40
	}
	if n.mergeable() {
		impact += 15
	}
	if impact == 0 {
		impact = 5
	}
	score := impact
	if at := n.Since(); !at.IsZero() {
		score += min(24, max(0, int(now.Sub(at).Hours())))
	}
	if m := n.Minutes(); m > 0 && m <= 2 {
		score += 10
	}
	return score
}

// rank orders rows: pins by hand and height, then score, then the shortest, unknown sizes last among equals, then the oldest.
func rank(ns []Need, now time.Time) {
	shortest := func(n Need) int {
		if m := n.Minutes(); m > 0 {
			return m
		}
		return 1 << 30
	}
	sort.SliceStable(ns, func(i, j int) bool {
		a, c := ns[i], ns[j]
		if ta, tc := a.tier(), c.tier(); ta != tc {
			return ta > tc
		}
		if pa, pc := a.mark().Pin, c.mark().Pin; pa != pc {
			return pa > pc
		}
		if sa, sc := a.Score(now), c.Score(now); sa != sc {
			return sa > sc
		}
		if sa, sc := shortest(a), shortest(c); sa != sc {
			return sa < sc
		}
		return a.Since().Before(c.Since())
	})
}

// itemGoalEnded reports whether the goal an item came from is done, killed or dropped.
func itemGoalEnded(o Org, it Item) bool {
	id := it.GoalOwner
	if id == "" {
		id = it.Dossier.Goal
	}
	g, ok := o.Goals[id]
	return ok && g.Ended()
}

// foldLater moves snoozed rows to later and ranks both lists.
func (c *Core) foldLater(b *Board) error {
	marks, err := c.Store.queueMarks()
	if err != nil {
		return err
	}
	var kept []Need
	for _, n := range b.Needs {
		if n.Item == nil {
			n.Mark = marks[n.MarkKey()]
			if n.Mark.Until.After(b.At) {
				b.Later = append(b.Later, n)
				continue
			}
		}
		kept = append(kept, n)
	}
	b.Needs = kept
	rank(b.Needs, b.At)
	rank(b.Later, b.At)
	return nil
}

// Verb is the row's three-letter action.
func (n Need) Verb() string {
	switch {
	case n.Item != nil:
		switch {
		case n.Item.Action == "chat":
			return "cht"
		case n.Item.Kind == KindGo:
			return "go"
		case n.Item.Kind == KindReview:
			return "rev"
		case n.Item.Kind == KindChore:
			return "do"
		}
		return "dec"
	case n.PR != nil:
		if n.mergeable() {
			return "mrg"
		}
		return "rev"
	case n.Review != nil:
		return "rev"
	case n.Doc != nil:
		return "doc"
	case n.Goal != nil:
		return "stk"
	case n.Steer != nil:
		return "str"
	case n.Relay != nil:
		return "rly"
	case n.Session != nil && n.Session.Prompt == nil && n.Session.Status == Onboarding:
		return "set"
	}
	return "prm"
}

func prLabel(pr PR, title string) string {
	return strings.TrimSpace(fmt.Sprintf("%s#%d %s", prRepoName(pr), pr.Number, oneLine(title)))
}

// DoLine is the row's words: what its filer wrote, else the question cut to fit.
func (n Need) DoLine() string {
	switch {
	case n.Item != nil:
		if n.Item.DoLine != "" {
			return n.Item.DoLine
		}
		return ansi.Truncate(oneLine(n.Item.Question), MaxDoLine, "…")
	case n.PR != nil:
		title := ""
		if n.PR.Status != nil {
			title = n.PR.Status.Title
		}
		return prLabel(*n.PR, title)
	case n.Review != nil:
		if found := PRsIn(n.Review.URL); len(found) == 1 {
			return prLabel(found[0], n.Review.Title)
		}
		return oneLine(n.Review.Title)
	case n.Doc != nil:
		return "reply on " + oneLine(n.Doc.Title)
	case n.Goal != nil:
		return "unstick " + n.Goal.ID
	case n.Steer != nil:
		return fmt.Sprintf("%s adopt r%d", n.Manager, n.Steer.Revision)
	case n.Relay != nil:
		return "relay refused by " + n.Relay.Dest
	case n.Session != nil && n.Session.Prompt != nil:
		return n.Session.Prompt.String()
	case n.Session != nil && n.Session.Status == Onboarding:
		return "first-run onboarding: " + n.Session.WaitingFor
	}
	return "permission prompt"
}

// Why is what opening the row adds: the question and rec behind an item's do-line, the facts of a pull request, a goal's ask.
func (n Need) Why() []string {
	switch {
	case n.Item != nil:
		out := []string{oneLine(n.Item.Question)}
		if more := len(n.Asks) - 1; more > 0 {
			out = append(out, fmt.Sprintf("+%d more asks of this session", more))
		}
		if n.Item.Rec != "" {
			out = append(out, "rec: "+oneLine(n.Item.Rec))
		}
		return out
	case n.PR != nil:
		st := n.PR.Status
		if st == nil {
			return []string{n.PR.URL}
		}
		line := oneLine(st.Title) + " · " + st.CI()
		if st.Merge.Additions+st.Merge.Deletions > 0 {
			line += fmt.Sprintf(" · +%d/-%d", st.Merge.Additions, st.Merge.Deletions)
		}
		return []string{line, n.PR.URL}
	case n.Review != nil:
		var bits []string
		if n.Review.Title != "" {
			bits = append(bits, oneLine(n.Review.Title)+" by "+n.Review.Author)
		}
		if n.Review.Additions+n.Review.Deletions > 0 {
			bits = append(bits, fmt.Sprintf("+%d/-%d", n.Review.Additions, n.Review.Deletions))
		}
		if len(bits) == 0 {
			return []string{n.Review.URL}
		}
		return []string{strings.Join(bits, " · "), n.Review.URL}
	case n.Doc != nil:
		var out []string
		if len(n.Docs) > 1 {
			out = append(out, fmt.Sprintf("%d open threads", len(n.Docs)))
		}
		for _, d := range n.Docs {
			out = append(out, oneLine(d.Asker+": "+d.Text), d.URL)
		}
		if len(out) == 0 {
			out = []string{oneLine(n.Doc.Asker + ": " + n.Doc.Text)}
		}
		return out
	case n.Goal != nil:
		return []string{oneLine(n.Goal.Ask)}
	case n.Steer != nil:
		return []string{fmt.Sprintf("not adopted after %d nudges: %s", n.Steer.Nudges, oneLine(n.Steer.Text))}
	case n.Relay != nil:
		return []string{oneLine(n.Relay.Error)}
	}
	return []string{n.DoLine()}
}

// ID names the row: an item's qualified id, a pull request's repo and number, a goal.
func (n Need) ID() string {
	switch {
	case n.Item != nil:
		return n.Item.Qualified()
	case n.PR != nil:
		return fmt.Sprintf("%s#%d", prRepoName(*n.PR), n.PR.Number)
	case n.Review != nil:
		if found := PRsIn(n.Review.URL); len(found) == 1 {
			return fmt.Sprintf("%s#%d", prRepoName(found[0]), found[0].Number)
		}
		return n.Review.URL
	case n.Doc != nil:
		return oneLine(n.Doc.Title)
	case n.Goal != nil:
		return n.Goal.ID
	case n.Steer != nil:
		return n.Manager
	case n.Relay != nil:
		return fmt.Sprintf("relay %d", n.Relay.Seq)
	case n.Session != nil:
		return Short(n.Session.ID)
	}
	return ""
}

func oldFor(now, at time.Time) string {
	if at.IsZero() {
		return ""
	}
	d := now.Sub(at)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm old", max(0, int(d.Minutes())))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh old", int(d.Hours()))
	}
	return fmt.Sprintf("%dd old", int(d.Hours()/24))
}

// QueueMinutes is a time to do in at most three columns; unknown is ?m, never a guess.
func QueueMinutes(m int) string {
	switch {
	case m <= 0:
		return "?m"
	case m < 100:
		return fmt.Sprintf("%dm", m)
	case m < 100*60:
		return fmt.Sprintf("%dh", (m+59)/60)
	}
	return fmt.Sprintf("%dd", (m+1439)/1440)
}

// QueueHeader is the line over the rows: how many, how long the known ones take, and what later holds.
func QueueHeader(rows []Need, later int) string {
	total := 0
	for _, n := range rows {
		total += n.Minutes()
	}
	left := fmt.Sprintf("NEXT %d · ~%s", len(rows), QueueMinutes(total))
	if total == 0 {
		left = fmt.Sprintf("NEXT %d", len(rows))
	}
	if later == 0 {
		return left
	}
	right := fmt.Sprintf("later %d ▾", later)
	return left + strings.Repeat(" ", max(1, QueueWidth-ansi.StringWidth(left)-ansi.StringWidth(right))) + right
}

// RowLine draws one row within width columns: rank, verb, minutes, ! for prod or clinical, the do-line, then ✓ or ▸, with the mark in the last column.
// Side is the why, id and age to draw dim to the right of it, when width leaves room.
func RowLine(n Need, rank string, width int, now time.Time) (line, side string) {
	main := min(width, QueueWidth)
	mark := "▸"
	if n.Check() != CheckNone {
		mark = "✓"
	}
	head := fmt.Sprintf("%s %-3s %-3s ", rank, n.Verb(), QueueMinutes(n.Minutes()))
	do := n.DoLine()
	if n.Urgent() {
		do = "!" + do
	}
	room := max(1, main-ansi.StringWidth(head)-1)
	do = ansi.Truncate(do, room, "…")
	line = head + do + strings.Repeat(" ", max(0, room-ansi.StringWidth(do))) + mark
	if width >= 100 {
		tail := " · " + n.ID()
		if old := oldFor(now, n.Since()); old != "" {
			tail += " · " + old
		}
		room := width - ansi.StringWidth(line) - ansi.StringWidth(tail) - 3
		side = ansi.Truncate("  "+ansi.Truncate(n.Why()[0], max(1, room), "…")+tail, width-ansi.StringWidth(line), "…")
	}
	return line, side
}

// RowDetail is what opening a row shows within width columns: its why, wrapped, then id, goal and age.
func RowDetail(n Need, width int, now time.Time) []string {
	var out []string
	for _, w := range n.Why() {
		out = append(out, strings.Split(ansi.Wordwrap(w, max(10, width-2), " "), "\n")...)
	}
	facts := []string{n.ID()}
	if n.Item == nil && n.Manager != "" {
		facts = append(facts, n.Manager)
	}
	if n.Item != nil {
		if n.Item.Dossier.Goal != "" {
			facts = append(facts, n.Item.Dossier.Goal)
		}
		if n.Item.Blocks > 0 {
			facts = append(facts, fmt.Sprintf("blocks %d", n.Item.Blocks))
		}
	}
	if old := oldFor(now, n.Since()); old != "" {
		facts = append(facts, old)
	}
	if until := n.mark().Until; !until.IsZero() {
		facts = append(facts, "back "+until.Local().Format("Jan 2 15:04"))
	}
	return append(out, strings.Join(facts, " · "))
}

// LaterLabel stands in for the rank of a row in the later fold.
const LaterLabel = "·"

// RankLabels are the row numbers of n rows, padded to one width.
func RankLabels(n int) []string {
	width := len(fmt.Sprint(n))
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%*d", width, i+1)
	}
	return out
}
