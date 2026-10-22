package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// DocAsk is something a person asks of the owner in a document, waiting on the owner or on the manager whose goal it concerns.
type DocAsk struct {
	Key, Source, Kind, URL, Page, Title, Asker, Text, Evidence, Version string
	Refs                                                                []string
	// Recipient is Owner or the manager the ask was routed to; Routed is the version that routing last placed.
	Recipient, Routed string
	// Told is the version last handed to Recipient when that is a manager.
	Told   string
	SeenAt time.Time
}

// DocSweep is how a source's last sweep went.
type DocSweep struct {
	Source string
	At     time.Time
	Pages  int
	Error  string
}

const docSchema = `
CREATE TABLE IF NOT EXISTS ym_doc_asks (
	key TEXT PRIMARY KEY,
	source TEXT NOT NULL,
	kind TEXT NOT NULL,
	url TEXT NOT NULL,
	page TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	asker TEXT NOT NULL DEFAULT '',
	text TEXT NOT NULL DEFAULT '',
	evidence TEXT NOT NULL DEFAULT '',
	version TEXT NOT NULL,
	refs TEXT NOT NULL DEFAULT '[]',
	state TEXT NOT NULL,
	recipient TEXT NOT NULL DEFAULT '',
	routed TEXT NOT NULL DEFAULT '',
	told TEXT NOT NULL DEFAULT '',
	seen_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_doc_pages (
	source TEXT NOT NULL,
	page TEXT NOT NULL,
	edited TEXT NOT NULL,
	PRIMARY KEY (source, page)
);
CREATE TABLE IF NOT EXISTS ym_doc_sweeps (
	source TEXT PRIMARY KEY,
	at TEXT NOT NULL,
	pages INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT ''
);
`

// SyncDocPage makes the open asks on one of source's pages exactly asks, as of the page's edit: new ones open, changed ones take
// the new version, and ones the page no longer shows close, as a discussion resolved or answered in the document does.
func (s *Store) SyncDocPage(source, page, edited string, asks []DocAsk) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE ym_doc_asks SET state = 'gone' WHERE state = 'open' AND source = ? AND page = ?`, source, page); err != nil {
		return err
	}
	for _, a := range asks {
		refs, err := json.Marshal(append([]string{}, a.Refs...))
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO ym_doc_asks (key, source, kind, url, page, title, asker, text, evidence, version, refs, state, seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?)
			ON CONFLICT(key) DO UPDATE SET source = excluded.source, kind = excluded.kind, url = excluded.url, page = excluded.page,
			title = excluded.title, asker = excluded.asker, text = excluded.text, evidence = excluded.evidence, version = excluded.version,
			refs = excluded.refs, state = 'open', seen_at = CASE ym_doc_asks.state WHEN 'open' THEN ym_doc_asks.seen_at ELSE excluded.seen_at END`,
			a.Key, source, a.Kind, a.URL, page, a.Title, a.Asker, a.Text, a.Evidence, a.Version, string(refs), now()); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO ym_doc_pages (source, page, edited) VALUES (?, ?, ?)
		ON CONFLICT(source, page) DO UPDATE SET edited = excluded.edited`, source, page, edited); err != nil {
		return err
	}
	return tx.Commit()
}

// DocPagesRead maps each of source's pages to the edit its asks were last read at.
func (s *Store) DocPagesRead(source string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT page, edited FROM ym_doc_pages WHERE source = ?`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var page, edited string
		if err := rows.Scan(&page, &edited); err != nil {
			return nil, err
		}
		out[page] = edited
	}
	return out, rows.Err()
}

// FinishDocSweep records how source's sweep went: the pages it listed and why any of them could not be read, or why it could not
// list them at all, when pages is negative.
func (s *Store) FinishDocSweep(source string, pages int, failure error) error {
	msg := ""
	if failure != nil {
		msg = failure.Error()
	}
	_, err := s.db.Exec(`INSERT INTO ym_doc_sweeps (source, at, pages, error) VALUES (?, ?, ?, ?)
		ON CONFLICT(source) DO UPDATE SET at = excluded.at, pages = CASE WHEN excluded.pages >= 0 THEN excluded.pages ELSE ym_doc_sweeps.pages END,
		error = excluded.error`, source, now(), pages, msg)
	return err
}

func (s *Store) DocSweeps() ([]DocSweep, error) {
	rows, err := s.db.Query(`SELECT source, at, pages, error FROM ym_doc_sweeps ORDER BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DocSweep
	for rows.Next() {
		var d DocSweep
		var at sql.NullString
		if err := rows.Scan(&d.Source, &at, &d.Pages, &d.Error); err != nil {
			return nil, err
		}
		d.At = parseTime(at)
		out = append(out, d)
	}
	return out, rows.Err()
}

// OpenDocAsks lists every open ask, oldest first.
func (s *Store) OpenDocAsks() ([]DocAsk, error) {
	rows, err := s.db.Query(`SELECT key, source, kind, url, page, title, asker, text, evidence, version, refs, recipient, routed, told, seen_at
		FROM ym_doc_asks WHERE state = 'open' ORDER BY seen_at, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DocAsk
	for rows.Next() {
		var a DocAsk
		var refs string
		var seen sql.NullString
		if err := rows.Scan(&a.Key, &a.Source, &a.Kind, &a.URL, &a.Page, &a.Title, &a.Asker, &a.Text, &a.Evidence, &a.Version, &refs,
			&a.Recipient, &a.Routed, &a.Told, &seen); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(refs), &a.Refs); err != nil {
			return nil, err
		}
		a.SeenAt = parseTime(seen)
		out = append(out, a)
	}
	return out, rows.Err()
}

// OpenDocPages lists the pages of source's open asks, so a sweep rechecks them however long ago they were edited.
func (s *Store) OpenDocPages(source string) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT page FROM ym_doc_asks WHERE state = 'open' AND source = ? AND page != ''`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) routeDocAsk(key, recipient, routed, told string) error {
	_, err := s.db.Exec(`UPDATE ym_doc_asks SET recipient = ?, routed = ?, told = ? WHERE key = ?`, recipient, routed, told, key)
	return err
}

var (
	notionRef = regexp.MustCompile(`[a-fA-F0-9]{8}-?[a-fA-F0-9]{4}-?[a-fA-F0-9]{4}-?[a-fA-F0-9]{4}-?[a-fA-F0-9]{12}`)
	pullRef   = regexp.MustCompile(`https://github\.com/[\w.-]+/[\w.-]+/pull/\d+`)
)

// goalRefs are the document ids and pull requests a goal's work touches: ids its ask, evidence, summary, artifacts and brief name,
// and every pull request its manager's and agents' sessions opened or changed.
func goalRefs(n Node) map[string]bool {
	out := map[string]bool{}
	text := n.Manager.ArtifactURL
	if g := n.Goal; g != nil {
		text += "\n" + g.Ask + "\n" + g.Evidence + "\n" + g.Summary + "\n" + g.ArtifactURL
		if b, err := os.ReadFile(g.BriefPath); err == nil && g.BriefPath != "" {
			text += "\n" + string(b)
		}
	}
	for _, id := range notionRef.FindAllString(text, -1) {
		out[strings.ToLower(strings.ReplaceAll(id, "-", ""))] = true
	}
	for _, u := range pullRef.FindAllString(text, -1) {
		out[u] = true
	}
	sessions := append([]Live{}, n.Agents...)
	if n.Live != nil {
		sessions = append(sessions, *n.Live)
	}
	for _, s := range sessions {
		for _, pr := range s.AllPRs() {
			out[pr.URL] = true
		}
	}
	return out
}

// goalOf is the manager whose goal's work the ask concerns: the oldest goal not yet done or dropped whose refs include the ask's page or any page
// or pull request the ask names.
func goalOf(b Board, a DocAsk) (Manager, bool) {
	var walk func([]Node) (Manager, bool)
	walk = func(ns []Node) (Manager, bool) {
		for _, n := range ns {
			if n.Goal != nil && b.Org.Active(n.Manager) {
				refs := goalRefs(n)
				if refs[a.Page] {
					return n.Manager, true
				}
				for _, r := range a.Refs {
					if refs[r] {
						return n.Manager, true
					}
				}
			}
			if m, ok := walk(n.Children); ok {
				return m, ok
			}
		}
		return Manager{}, false
	}
	return walk(b.Tree)
}

// RouteDocs places every open document ask: one about a goal's work goes to that goal's manager, or the first working one above
// it, and is handed to it once per version; the rest wait in the owner's queue.
func (c *Core) RouteDocs(b Board) error {
	asks, err := c.Store.OpenDocAsks()
	if err != nil {
		return err
	}
	var failures []error
	for _, a := range asks {
		to := Owner
		if m, ok := goalOf(b, a); ok {
			to = b.Org.Above(m.ID)
		}
		told := a.Told
		if to != Owner && (to != a.Recipient || told != a.Version) {
			if err := c.tell(b, b.Org.Managers[to].Session, docText(b.Org, to, a), false); err != nil {
				failures = append(failures, err)
				continue
			}
			told = a.Version
		}
		if to != a.Recipient || a.Routed != a.Version || told != a.Told {
			if err := c.Store.routeDocAsk(a.Key, to, a.Version, told); err != nil {
				return err
			}
		}
	}
	return errors.Join(failures...)
}

// docText is how a document ask reaches the manager it was routed to.
func docText(o Org, to string, a DocAsk) string {
	about := ""
	if g, ok := o.Goals[o.Managers[to].GoalID]; ok {
		about = " about goal " + g.ID
	}
	return fmt.Sprintf("ym: %s asks the owner in %s doc %q%s (%s, %s): %s Thread: %s"+
		" You own this: act on it in the document itself (prefix anything you post with your model name). It leaves your queue when"+
		" the discussion is resolved or answered there. Escalate with ym item add only if it is beyond your authority.",
		a.Asker, docSource(a.Source), a.Title, about, a.URL, a.Kind, oneLine(a.Text), oneLine(strings.ReplaceAll(a.Evidence, "\n", " | ")))
}

func docSource(s string) string {
	if s == "notion" {
		return "a Notion"
	}
	return "a " + s
}
