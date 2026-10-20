package work

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Store struct{ db *sql.DB }

func New(db *sql.DB) (*Store, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS ym_work (id TEXT PRIMARY KEY, origin TEXT NOT NULL UNIQUE, repo TEXT NOT NULL, state BLOB NOT NULL);
	CREATE TABLE IF NOT EXISTS ym_work_events (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, work TEXT NOT NULL, event BLOB NOT NULL);
	CREATE INDEX IF NOT EXISTS ym_work_events_scope ON ym_work_events(work,seq);
	CREATE TABLE IF NOT EXISTS ym_work_outbox (id TEXT NOT NULL, destination TEXT NOT NULL, work TEXT NOT NULL, text TEXT NOT NULL, delivered INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(id,destination));
	CREATE TABLE IF NOT EXISTS ym_work_seen (work TEXT PRIMARY KEY, revision INTEGER NOT NULL);
	CREATE TABLE IF NOT EXISTS ym_work_peers (host TEXT PRIMARY KEY, state BLOB, error TEXT NOT NULL DEFAULT '', fetched TEXT NOT NULL DEFAULT '');
	CREATE TABLE IF NOT EXISTS ym_work_commands (id TEXT PRIMARY KEY, host TEXT NOT NULL, event BLOB NOT NULL, error TEXT NOT NULL DEFAULT '');
	CREATE TABLE IF NOT EXISTS ym_work_source_errors (work TEXT NOT NULL, url TEXT NOT NULL, error TEXT NOT NULL, PRIMARY KEY(work,url));`)
	if err == nil {
		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS ym_work_polls (work TEXT NOT NULL,url TEXT NOT NULL,at INTEGER NOT NULL,PRIMARY KEY(work,url)); CREATE TABLE IF NOT EXISTS ym_work_notifications (id TEXT NOT NULL,destination TEXT NOT NULL,PRIMARY KEY(id,destination)); CREATE INDEX IF NOT EXISTS ym_work_outbox_scope ON ym_work_outbox(work,delivered);`)
	}
	if err == nil {
		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS ym_work_runs(session TEXT PRIMARY KEY,error TEXT NOT NULL);`)
	}
	if err == nil {
		_, err = db.Exec(agentSchema)
	}
	return &Store{db: db}, err
}

func (s *Store) Ensure(origin, repo, name string) (Scope, error) {
	x := Scope{ID: uuid.NewString(), Origin: origin, Repo: repo, Name: name, Bindings: map[string]int64{}, Reviews: map[string]Review{}, Updated: time.Now().UTC()}
	raw, err := json.Marshal(x)
	if err != nil {
		return x, err
	}
	_, err = s.db.Exec(`INSERT INTO ym_work(id,origin,repo,state) VALUES(?,?,?,?) ON CONFLICT(origin) DO NOTHING`, x.ID, origin, repo, raw)
	if err != nil {
		return x, err
	}
	return s.Get(origin)
}

func (s *Store) Get(id string) (Scope, error) {
	var raw []byte
	err := s.db.QueryRow(`SELECT state FROM ym_work WHERE id=? OR origin=?`, id, id).Scan(&raw)
	var x Scope
	if err == nil {
		err = json.Unmarshal(raw, &x)
	}
	if err == nil {
		err = s.db.QueryRow(`SELECT count(*) FROM ym_agent_queue WHERE agent=?`, x.ID).Scan(&x.PendingInputs)
	}
	return x, err
}

func (s *Store) List() ([]Scope, error) {
	rows, err := s.db.Query(`SELECT state,COALESCE((SELECT revision FROM ym_work_seen s WHERE s.work=w.id),0),(SELECT count(*) FROM ym_work_outbox o WHERE o.work=w.id AND o.delivered=0),(SELECT count(*) FROM ym_agent_queue q WHERE q.agent=w.id) FROM ym_work w ORDER BY repo,origin`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Scope
	for rows.Next() {
		var raw []byte
		var x Scope
		var seen int64
		var pending, inputs int
		if err := rows.Scan(&raw, &seen, &pending, &inputs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &x); err != nil {
			return nil, err
		}
		x.Changes = max(0, x.Revision-seen)
		x.Pending = pending
		x.PendingInputs = inputs
		out = append(out, x)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	errors, err := s.db.Query(`SELECT work,url,error FROM ym_work_source_errors WHERE error!=''`)
	if err != nil {
		return nil, err
	}
	defer errors.Close()
	byID := map[string][]string{}
	for errors.Next() {
		var id, url, text string
		if err = errors.Scan(&id, &url, &text); err != nil {
			return nil, err
		}
		byID[id] = append(byID[id], url+": "+text)
	}
	if err = errors.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].CoverageErrors = byID[out[i].ID]
	}
	runs, err := s.db.Query(`SELECT session,error FROM ym_work_runs WHERE error!=''`)
	if err != nil {
		return nil, err
	}
	defer runs.Close()
	for runs.Next() {
		var session, text string
		if err = runs.Scan(&session, &text); err != nil {
			return nil, err
		}
		for i := range out {
			if out[i].Manager == session {
				out[i].CoverageErrors = append(out[i].CoverageErrors, "manager: "+text)
			}
		}
	}
	if err = runs.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) RunResult(session string, err error) error {
	text := ""
	if err != nil {
		text = err.Error()
	}
	_, e := s.db.Exec(`INSERT INTO ym_work_runs(session,error) VALUES(?,?) ON CONFLICT(session) DO UPDATE SET error=excluded.error`, session, text)
	return e
}

func (s *Store) Commit(e Event) (Scope, error) {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Scope{}, err
	}
	defer tx.Rollback()
	var raw []byte
	if err = tx.QueryRow(`SELECT state FROM ym_work WHERE id=? OR origin=?`, e.Work, e.Work).Scan(&raw); err != nil {
		return Scope{}, err
	}
	var x Scope
	if err = json.Unmarshal(raw, &x); err != nil {
		return x, err
	}
	var prior []byte
	err = tx.QueryRow(`SELECT event FROM ym_work_events WHERE id=?`, e.ID).Scan(&prior)
	if err == nil {
		var old Event
		if err = json.Unmarshal(prior, &old); err != nil {
			return x, err
		}
		if old.Work != x.ID {
			return x, errors.New("event identity belongs to another workstream")
		}
		left, right := old, e
		left.ID, right.ID = "", ""
		left.Work, right.Work = "", ""
		left.Expected, right.Expected = 0, 0
		left.At, right.At = time.Time{}, time.Time{}
		a, _ := json.Marshal(left)
		b, _ := json.Marshal(right)
		if string(a) != string(b) {
			return x, errors.New("event identity reused with different content")
		}
		return x, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return x, err
	}
	if e.Expected != x.Revision {
		compatible := e.Type == "message" || (e.Type == "context" && e.Context == x.Context.Revision)
		if e.Type == "feedback" || e.Type == "decide" {
			r, ok := x.Reviews[e.Target]
			compatible = ok && r.State == Ready && e.Review.Version == r.Version && e.Review.Preparation != 0 && e.Review.Preparation == r.Preparation && e.Context == x.Context.Revision
		}
		if !compatible {
			return x, ErrConflict
		}
	}
	if err = validateDelegation(tx, x, e); err != nil {
		return x, err
	}
	e.Work = x.ID
	before := x.Revision
	if err = Apply(&x, e); err != nil {
		return x, err
	}
	if x.Revision == before {
		return x, nil
	}
	raw, err = json.Marshal(x)
	if err != nil {
		return x, err
	}
	if _, err = tx.Exec(`UPDATE ym_work SET state=? WHERE id=?`, raw, x.ID); err != nil {
		return x, err
	}
	raw, err = json.Marshal(e)
	if err != nil {
		return x, err
	}
	if _, err = tx.Exec(`INSERT INTO ym_work_events(id,work,event) VALUES(?,?,?)`, e.ID, x.ID, raw); err != nil {
		return x, err
	}
	destinations := []string{}
	switch e.Type {
	case "context", "authority":
		for session := range x.Bindings {
			destinations = append(destinations, session)
		}
	case "bind":
		if x.Context.Revision > 0 {
			destinations = append(destinations, e.Target)
		}
	case "message":
		destinations = append(destinations, e.Target)
	case "owner", "parent", "result", "verify", "reopen", "observe", "feedback", "decide", "wake", "watch", "close", "resume", "relate", "unrelate":
		destinations = append(destinations, "@manager")
	case "purpose":
		if e.Actor == "owner" {
			for session := range x.Bindings {
				destinations = append(destinations, session)
			}
		}
	case "manager":
		destinations = append(destinations, x.Manager)
	}
	if x.Owner != "" {
		destinations = nil
	}
	text := fmt.Sprintf("ym work %s: %s\n%s\nRead current context and brief with ym work show %s. Publish assessment with ym work brief; acknowledge incorporated context with ym work adopt.", x.ID, e.Type, e.Text, x.ID)
	if e.Type == "context" || e.Type == "manager" || e.Type == "bind" {
		text = fmt.Sprintf("ym work %s context r%d:\n%s\nRead ym work show %s. Record adoption with ym work adopt %s --revision %d after incorporating it; receipt alone is not adoption.", x.ID, x.Context.Revision, x.Context.Text, x.ID, x.ID, x.Context.Revision)
	}
	if e.Type == "observe" {
		text = fmt.Sprintf("ym work %s source %s changed to %s. Prepare its review against current source/context; publish an updated assessment.", x.ID, e.Review.ID, e.Review.Version)
	}
	if e.Type == "feedback" || e.Type == "decide" {
		text = fmt.Sprintf("ym work %s review %s: owner %s\n%s\nYou own follow-through. Keep the obligation and project brief current.", x.ID, e.Target, e.Type, e.Text)
	}
	if e.Type == "wake" {
		text = fmt.Sprintf("ym work %s trigger: %s\nRead current context, reassess, and publish a brief or prepare an owner judgment within the delegated authority.", x.ID, x.Trigger)
	}
	for _, dest := range destinations {
		if (dest == "@manager" || dest == "") && x.Owner != "" {
			continue
		}
		var persistent int
		if err = tx.QueryRow(`SELECT count(*) FROM ym_agents WHERE id=?`, dest).Scan(&persistent); err != nil {
			return x, err
		}
		if persistent > 0 {
			continue
		}
		if dest == "" {
			dest = "@manager"
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO ym_work_outbox(id,destination,work,text) VALUES(?,?,?,?)`, e.ID, dest, x.ID, text); err != nil {
			return x, err
		}
	}
	if err = queueAgentEvent(tx, x, e); err != nil {
		return x, err
	}
	return x, tx.Commit()
}

func (s *Store) HasEvent(id string) bool {
	var found int
	return s.db.QueryRow(`SELECT 1 FROM ym_work_events WHERE id=?`, id).Scan(&found) == nil
}

func (s *Store) History(id string, after int64) ([]Event, error) {
	x, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT event FROM ym_work_events WHERE work=? AND seq>? ORDER BY seq`, x.ID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var raw []byte
		var e Event
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Seen(id string, revision int64) error {
	_, err := s.db.Exec(`INSERT INTO ym_work_seen(work,revision) VALUES(?,?) ON CONFLICT(work) DO UPDATE SET revision=max(revision,excluded.revision)`, id, revision)
	return err
}

func (s *Store) LastSeen(id string) int64 {
	var revision int64
	s.db.QueryRow(`SELECT revision FROM ym_work_seen WHERE work=?`, id).Scan(&revision)
	return revision
}

func (s *Store) Recent(id string) ([]Event, error) {
	rows, err := s.db.Query(`SELECT event FROM ym_work_events WHERE work=? ORDER BY seq DESC LIMIT 20`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var raw []byte
		var e Event
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		e.Review.Evidence = ""
		out = append(out, e)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func (s *Store) Unseen(id string) (int64, error) {
	x, err := s.Get(id)
	if err != nil {
		return 0, err
	}
	var seen int64
	err = s.db.QueryRow(`SELECT revision FROM ym_work_seen WHERE work=?`, x.ID).Scan(&seen)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return max(0, x.Revision-seen), err
}

func (s *Store) Inbox(session string) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT o.text FROM ym_work_outbox o JOIN ym_work w ON w.id=o.work WHERE o.delivered=0 AND (o.destination=? OR (o.destination='@manager' AND json_extract(CAST(w.state AS TEXT),'$.Manager')=?)) ORDER BY o.rowid`, session, session)
	if err != nil {
		return nil, err
	}
	var out []string
	for rows.Next() {
		var text string
		if err = rows.Scan(&text); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, text)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`UPDATE ym_work_outbox SET delivered=1 WHERE delivered=0 AND (destination=? OR (destination='@manager' AND work IN(SELECT id FROM ym_work WHERE json_extract(CAST(state AS TEXT),'$.Manager')=?)))`, session, session); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func (s *Store) SourceError(id, url string, err error) error {
	var previous string
	s.db.QueryRow(`SELECT error FROM ym_work_source_errors WHERE work=? AND url=?`, id, url).Scan(&previous)
	text := ""
	if err != nil {
		text = err.Error()
	}
	_, e := s.db.Exec(`INSERT INTO ym_work_source_errors(work,url,error) VALUES(?,?,?) ON CONFLICT(work,url) DO UPDATE SET error=excluded.error`, id, url, text)
	if e != nil {
		return e
	}
	_, e = s.db.Exec(`INSERT INTO ym_work_polls(work,url,at) VALUES(?,?,?) ON CONFLICT(work,url) DO UPDATE SET at=excluded.at`, id, url, time.Now().Unix())
	if e == nil && previous != text {
		if w, getErr := s.Get(id); getErr == nil {
			_, e = s.Commit(Event{Work: w.ID, Expected: w.Revision, Type: "message", Actor: "source", Text: "Source coverage changed for " + url + ": " + text})
		}
	}
	return e
}

func (s *Store) Due(id, url string, at time.Time) bool {
	var last int64
	err := s.db.QueryRow(`SELECT at FROM ym_work_polls WHERE work=? AND url=?`, id, url).Scan(&last)
	return errors.Is(err, sql.ErrNoRows) || (err == nil && at.Unix()-last >= 120)
}

func (s *Store) Coverage(id string) ([]string, error) {
	rows, err := s.db.Query(`SELECT url,error FROM ym_work_source_errors WHERE work=? AND error!=''`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var url, text string
		if err := rows.Scan(&url, &text); err != nil {
			return nil, err
		}
		out = append(out, url+": "+text)
	}
	return out, rows.Err()
}

func (s *Store) Deliver(send func(string, string) error) error {
	rows, err := s.db.Query(`SELECT o.id,CASE WHEN o.destination='@manager' THEN json_extract(CAST(w.state AS TEXT),'$.Manager') ELSE o.destination END AS target,o.work,o.text FROM ym_work_outbox o JOIN ym_work w ON w.id=o.work WHERE o.delivered=0 AND target!='' AND NOT EXISTS(SELECT 1 FROM ym_work_notifications n WHERE n.id=o.id AND n.destination=target) ORDER BY o.rowid LIMIT 64`)
	if err != nil {
		return err
	}
	type pending struct{ id, destination, work, text string }
	var todo []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.destination, &p.work, &p.text); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	groups := map[string][]pending{}
	var order []string
	for _, p := range todo {
		if _, ok := groups[p.destination]; !ok {
			order = append(order, p.destination)
		}
		groups[p.destination] = append(groups[p.destination], p)
	}
	for _, destination := range order {
		var ids []string
		for _, p := range groups[destination] {
			ids = append(ids, p.id)
		}
		if err = send(destination, "Pending work events: "+strings.Join(ids, ", ")+". Receive the current updates through ym work inbox; event identities are stable across delivery attempts."); err != nil {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		for _, p := range groups[destination] {
			if _, err = tx.Exec(`INSERT OR IGNORE INTO ym_work_notifications(id,destination) VALUES(?,?)`, p.id, destination); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Find(query string) (Scope, error) {
	if x, err := s.Get(query); err == nil {
		return x, nil
	}
	all, err := s.List()
	if err != nil {
		return Scope{}, err
	}
	var found []Scope
	for _, x := range all {
		_, bound := x.Bindings[query]
		if bound || x.Name == query || strings.HasPrefix(x.ID, query) {
			found = append(found, x)
		}
	}
	if len(found) != 1 {
		return Scope{}, fmt.Errorf("work %q matches %d scopes; use its ID", query, len(found))
	}
	return found[0], nil
}
