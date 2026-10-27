package core

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BriefGrace is how long a manager may leave its brief behind what happened before it is nudged to refresh it.
const BriefGrace = 5 * time.Minute

const briefSchema = `
CREATE TABLE IF NOT EXISTS ym_briefs (
	manager TEXT NOT NULL,
	version INTEGER NOT NULL,
	assessment TEXT NOT NULL,
	changed TEXT NOT NULL,
	uncertainty TEXT NOT NULL,
	next TEXT NOT NULL,
	waiting TEXT NOT NULL,
	session TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	PRIMARY KEY (manager, version)
);
CREATE TABLE IF NOT EXISTS ym_brief_nudges (
	manager TEXT PRIMARY KEY,
	behind TEXT NOT NULL,
	at TEXT NOT NULL
);
`

// Brief is one version of where a goal or chief stands, in the fixed shape a cold reader scans.
type Brief struct {
	Manager     string
	Version     int
	Assessment  string
	Changed     string
	Uncertainty string
	Next        string
	WaitingOn   string
	Session     string
	CreatedAt   time.Time
}

// BriefFields are a brief's sections, in order, with the flag and heading each goes by.
var BriefFields = []struct{ Flag, Heading string }{
	{"assessment", "Assessment"}, {"changed", "What changed"}, {"uncertainty", "Uncertainty"}, {"next", "Next"}, {"waiting-on", "Waiting on"},
}

func (b Brief) fields() []string {
	return []string{b.Assessment, b.Changed, b.Uncertainty, b.Next, b.WaitingOn}
}

// Sections pairs each heading with its text.
func (b Brief) Sections() [][2]string {
	out := make([][2]string, len(BriefFields))
	for i, f := range b.fields() {
		out[i] = [2]string{BriefFields[i].Heading, f}
	}
	return out
}

// Markdown renders the brief as the page kept at the manager's brief path.
func (b Brief) Markdown(title string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\nBrief v%d, %s\n", title, b.Version, b.CreatedAt.Local().Format(time.DateTime))
	for _, s := range b.Sections() {
		fmt.Fprintf(&sb, "\n## %s\n\n%s\n", s[0], s[1])
	}
	return sb.String()
}

const briefCols = `manager, version, assessment, changed, uncertainty, next, waiting, session, created_at`

func scanBrief(row interface{ Scan(...any) error }) (Brief, error) {
	var b Brief
	var created sql.NullString
	err := row.Scan(&b.Manager, &b.Version, &b.Assessment, &b.Changed, &b.Uncertainty, &b.Next, &b.WaitingOn, &b.Session, &created)
	b.CreatedAt = parseTime(created)
	return b, err
}

func (s *Store) addBrief(b Brief) (Brief, error) {
	err := s.db.QueryRow(`INSERT INTO ym_briefs (manager, version, assessment, changed, uncertainty, next, waiting, session, created_at)
		VALUES (?, (SELECT coalesce(max(version), 0) + 1 FROM ym_briefs WHERE manager = ?), ?, ?, ?, ?, ?, ?, ?) RETURNING version`,
		b.Manager, b.Manager, b.Assessment, b.Changed, b.Uncertainty, b.Next, b.WaitingOn, b.Session, stamp(b.CreatedAt)).Scan(&b.Version)
	return b, err
}

// Briefs lists manager's brief versions, oldest first.
func (s *Store) Briefs(manager string) ([]Brief, error) {
	rows, err := s.db.Query(`SELECT `+briefCols+` FROM ym_briefs WHERE manager = ? ORDER BY version`, manager)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Brief
	for rows.Next() {
		b, err := scanBrief(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// LatestBriefs maps each manager that wrote a brief to its newest version.
func (s *Store) LatestBriefs() (map[string]Brief, error) {
	return remembered(s, "briefs", cloneMap[string, Brief], s.latestBriefs)
}

func (s *Store) latestBriefs() (map[string]Brief, error) {
	rows, err := s.db.Query(`SELECT ` + briefCols + ` FROM ym_briefs b WHERE version = (SELECT max(version) FROM ym_briefs WHERE manager = b.manager)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Brief{}
	for rows.Next() {
		b, err := scanBrief(rows)
		if err != nil {
			return nil, err
		}
		out[b.Manager] = b
	}
	return out, rows.Err()
}

func (s *Store) briefNudges() (map[string]time.Time, error) {
	rows, err := s.db.Query(`SELECT manager, behind FROM ym_brief_nudges`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var m string
		var behind sql.NullString
		if err := rows.Scan(&m, &behind); err != nil {
			return nil, err
		}
		out[m] = parseTime(behind)
	}
	return out, rows.Err()
}

func (s *Store) noteBriefNudge(manager string, behind time.Time) error {
	_, err := s.db.Exec(`INSERT INTO ym_brief_nudges (manager, behind, at) VALUES (?, ?, ?) ON CONFLICT(manager) DO UPDATE SET behind = excluded.behind, at = excluded.at`,
		manager, stamp(behind), now())
	return err
}

// WriteBrief records the calling manager's or chief's next brief version and rewrites its brief page from it.
func (c *Core) WriteBrief(session string, b Brief) (Brief, error) {
	for i, f := range b.fields() {
		if strings.TrimSpace(f) == "" {
			return b, fmt.Errorf("a brief needs every section; %s is empty (write none when there is nothing to say)", BriefFields[i].Flag)
		}
	}
	o, err := c.LoadOrg()
	if err != nil {
		return b, err
	}
	session = c.Resolve(session)
	m, ok := o.ManagerOf(session)
	if !ok {
		return b, fmt.Errorf("session %s is not a manager or chief; only they keep a brief", Short(session))
	}
	b.Manager, b.Session, b.CreatedAt = m.ID, session, c.now()
	for _, p := range []*string{&b.Assessment, &b.Changed, &b.Uncertainty, &b.Next, &b.WaitingOn} {
		*p = strings.TrimSpace(*p)
	}
	if b, err = c.Store.addBrief(b); err != nil {
		return b, err
	}
	title := m.Name
	if g, ok := o.Goals[m.GoalID]; ok {
		title = g.ID + ": " + oneLine(g.Ask)
	}
	if m.BriefPath != "" {
		if err := writeFile(m.BriefPath, b.Markdown(title)); err != nil {
			return b, err
		}
	}
	return b, nil
}

func writeFile(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// Freshness is how a goal's or chief's newest brief stands against what happened since it was written.
type Freshness struct {
	Brief *Brief
	// Behind is the newest event the brief predates, zero when it is fresh.
	Behind     time.Time
	BehindWhat string
}

func (f Freshness) Stale() bool { return f.Brief == nil || !f.Behind.IsZero() }

func (f Freshness) String() string {
	switch {
	case f.Brief == nil:
		return "no brief"
	case f.Stale():
		return fmt.Sprintf("brief v%d stale", f.Brief.Version)
	}
	return fmt.Sprintf("brief v%d", f.Brief.Version)
}

// freshness compares m's newest brief with the meaningful events since: the goal's events, the adoption of a steer, and for a
// chief, a report's newer brief.
func freshness(o Org, reports map[string][]string, m Manager, briefs map[string]Brief, steers map[string]Steer, events []stamped) Freshness {
	f := Freshness{}
	if b, ok := briefs[m.ID]; ok {
		f.Brief = &b
	}
	since := timeZero
	if f.Brief != nil {
		since = f.Brief.CreatedAt
	}
	note := func(at time.Time, what string) {
		if at.After(since) && at.After(f.Behind) {
			f.Behind, f.BehindWhat = at, what
		}
	}
	if st, ok := steers[m.ID]; ok {
		note(st.AdoptedAt, fmt.Sprintf("adopted r%d", st.Revision))
	}
	for _, e := range events {
		note(e.At, e.What)
	}
	if m.Role == RoleChief {
		for _, r := range o.subtree(reports, m.ID) {
			if b, ok := briefs[r.ID]; ok && r.ID != m.ID {
				note(b.CreatedAt, fmt.Sprintf("%s brief v%d", r.ID, b.Version))
			}
		}
	}
	if f.Brief == nil {
		f.Behind = timeZero
	}
	return f
}

// stamped is a meaningful goal event's time and a few words naming it.
type stamped struct {
	At   time.Time
	What string
}

// Freshness reports how every manager's and chief's brief stands, by manager id.
func (c *Core) Freshness(o Org) (map[string]Freshness, error) {
	briefs, err := c.Store.LatestBriefs()
	if err != nil {
		return nil, err
	}
	steers, err := c.Store.LatestSteers()
	if err != nil {
		return nil, err
	}
	events, err := c.meaningfulEvents()
	if err != nil {
		return nil, err
	}
	out := map[string]Freshness{}
	reports := o.reports()
	for _, m := range o.Ordered() {
		out[m.ID] = freshness(o, reports, m, briefs, steers, events[m.ID])
	}
	for _, r := range c.shownRemotes() {
		for id, f := range r.Fresh {
			if key := RemoteKey(r.Host, id); o.Managers[key].Host == r.Host {
				out[key] = f
			}
		}
	}
	return out, nil
}

// watchBriefs nudges a running manager or chief whose brief predates a meaningful event by more than BriefGrace, once per event.
func (c *Core) watchBriefs(b Board, o Org) error {
	fresh, err := c.Freshness(o)
	if err != nil {
		return err
	}
	nudged, err := c.Store.briefNudges()
	if err != nil {
		return err
	}
	now := c.now()
	var errs []error
	for _, m := range o.Ordered() {
		f := fresh[m.ID]
		if !o.Active(m) || !b.Alive(m) || !f.Stale() {
			continue
		}
		behind := f.Behind
		if f.Brief == nil {
			behind = m.LaunchedAt
		}
		if now.Sub(behind) < BriefGrace || !nudged[m.ID].Before(behind) {
			continue
		}
		if s, ok := b.Session(m.Session); ok && s.Status == Busy {
			continue
		}
		text := fmt.Sprintf("ym: your brief is behind (%s). Refresh it now with ym goal brief --assessment \"…\" --changed \"…\" --uncertainty \"…\" --next \"…\" --waiting-on \"…\".", f.reason())
		if err := c.tell(b, m.Session, text, false); err != nil {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, c.Store.noteBriefNudge(m.ID, behind), c.Store.noteBriefAsk(m.ID, now))
	}
	return errors.Join(errs...)
}

func (f Freshness) reason() string {
	if f.Brief == nil {
		return "you have written none yet"
	}
	return fmt.Sprintf("v%d predates %s at %s", f.Brief.Version, f.BehindWhat, f.Behind.Local().Format(time.Kitchen))
}

// BriefsAddr is where ym serve serves briefs: $YAMNAYA_BRIEFS_ADDR, else 127.0.0.1:7477 for the default state directory and a port
// of its own for any other, so a second ym's serve never takes the owner's page.
func BriefsAddr() string {
	if a := os.Getenv("YAMNAYA_BRIEFS_ADDR"); a != "" {
		return a
	}
	dir, err := DefaultStorePath()
	if err != nil {
		return "127.0.0.1:7477"
	}
	dir, _ = filepath.Abs(filepath.Dir(dir))
	if home, err := os.UserHomeDir(); err != nil || dir == filepath.Join(home, ".local", "state", "yamnaya") {
		return "127.0.0.1:7477"
	}
	sum := sha256.Sum256([]byte(dir))
	return fmt.Sprintf("127.0.0.1:%d", 7478+binary.BigEndian.Uint32(sum[:4])%1000)
}

// BriefURL is the stable local page of the goal's or chief's brief that id names.
func BriefURL(id string) string { return "http://" + BriefsAddr() + "/b/" + id }

// BriefURL is the page of the brief id names here. On a host another host carries relays for, it names this host too, so the
// same link opens on that host, whose ym serve shows this host's briefs at /b/<host>/<id>, and here.
func (c *Core) BriefURL(id string) string {
	if r, err := c.Store.RelaySelf(); err == nil && r.Name != "" {
		return BriefURL(RemoteKey(r.Name, id))
	}
	return BriefURL(id)
}
