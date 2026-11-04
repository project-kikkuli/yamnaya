package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const RelayWork = "work"

var ErrWorkPending = errors.New("assignment registration awaits its originating manager's host")

const remoteWorkSchema = `
CREATE TABLE IF NOT EXISTS ym_remote_work (
 id TEXT PRIMARY KEY,origin TEXT NOT NULL,goal TEXT NOT NULL,source TEXT NOT NULL,
 session TEXT NOT NULL,bound_session TEXT NOT NULL DEFAULT '',generation INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL,body TEXT NOT NULL,
 UNIQUE(origin,goal,source)
);
CREATE TABLE IF NOT EXISTS ym_remote_work_aliases (session TEXT PRIMARY KEY,assignment TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS ym_remote_go (item INTEGER PRIMARY KEY,origin TEXT NOT NULL);
`

type WorkMessage struct {
	Events     []GoalEvent
	Successors map[string]string `json:",omitempty"`
	Op         string
	Work       RemoteAssignment
	Summary    string
	Item       int64
	Answer     string
	Event      GoalEvent
}

func (s *Store) remoteWork(id string) (RemoteAssignment, error) {
	var w RemoteAssignment
	var body string
	err := s.db.QueryRow(`SELECT body,session,generation,state FROM ym_remote_work WHERE id=?`, id).Scan(&body, &w.Worker, &w.Assignment.Generation, &w.State)
	if err != nil {
		return w, err
	}
	session, generation, state := w.Worker, w.Assignment.Generation, w.State
	if err = json.Unmarshal([]byte(body), &w); err != nil {
		return w, err
	}
	w.Worker, w.Assignment.Generation, w.State = session, generation, state
	return w, nil
}

func (s *Store) putRemoteWork(w RemoteAssignment) error {
	_, err := s.db.Exec(`INSERT INTO ym_remote_work(id,origin,goal,source,session,generation,state,body) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, w.Assignment.ID, w.Origin, w.Goal, w.Assignment.Source, w.Worker, w.Assignment.Generation, w.State, jsonOf(w))
	return err
}

func (s *Store) remoteOwner(session string) (RemoteAssignment, error) {
	var id string
	err := s.db.QueryRow(`WITH RECURSIVE ancestry(session,depth,path) AS (
 SELECT ?,0,','||?||',' UNION ALL SELECT l.parent,a.depth+1,a.path||l.parent||',' FROM ym_links l JOIN ancestry a ON l.child=a.session WHERE instr(a.path,','||l.parent||',')=0)
 SELECT id FROM (SELECT w.id,a.depth,w.rowid AS ordinal FROM ancestry a JOIN ym_remote_work w ON w.session=a.session AND w.state!='pending'
 UNION ALL SELECT w.id,a.depth,w.rowid FROM ancestry a JOIN ym_remote_work_aliases x ON x.session=a.session JOIN ym_remote_work w ON w.id=x.assignment AND w.state!='pending')
 ORDER BY depth,ordinal DESC LIMIT 1`, session, session).Scan(&id)
	if err != nil {
		return RemoteAssignment{}, err
	}
	return s.remoteWork(id)
}

func (c *Core) remoteOwner(session string) (RemoteAssignment, error) {
	session = c.Resolve(session)
	w, err := c.Store.remoteOwner(session)
	if err == nil && w.Host != c.SelfName() {
		err = sql.ErrNoRows
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return w, err
	}
	rows, readErr := c.Store.db.Query(`SELECT id FROM ym_remote_work WHERE state!='pending' ORDER BY rowid DESC`)
	if readErr != nil {
		return w, readErr
	}
	var candidates []string
	for rows.Next() {
		var id string
		if readErr = rows.Scan(&id); readErr != nil {
			rows.Close()
			return w, readErr
		}
		candidates = append(candidates, id)
	}
	readErr = rows.Err()
	rows.Close()
	if readErr != nil {
		return w, readErr
	}
	for _, id := range candidates {
		candidate, readErr := c.Store.remoteWork(id)
		if readErr != nil {
			return w, readErr
		}
		if candidate.Host == c.SelfName() && c.Resolve(candidate.Worker) == session {
			_, readErr = c.Store.db.Exec(`INSERT INTO ym_remote_work_aliases(session,assignment) VALUES (?,?) ON CONFLICT(session) DO UPDATE SET assignment=excluded.assignment`, session, id)
			return candidate, readErr
		}
	}
	lin, err := c.Store.Lineage()
	if err != nil {
		return w, err
	}
	// Native harness edges are also present in the board before ym spawn records them.
	b, _ := c.Board()
	seen := map[string]bool{}
	for at := session; at != "" && !seen[at]; {
		seen[at] = true
		if w, err = c.Store.remoteOwner(at); err == nil && w.Host == c.SelfName() {
			_, err = c.Store.db.Exec(`INSERT INTO ym_remote_work_aliases(session,assignment) VALUES (?,?) ON CONFLICT(session) DO UPDATE SET assignment=excluded.assignment`, session, w.Assignment.ID)
			return w, err
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return w, err
		}
		next := lin.Parent[at]
		if next == "" {
			next = b.parents[at]
		}
		at = next
	}
	return RemoteAssignment{}, sql.ErrNoRows
}

func (c *Core) queueWork(host string, m WorkMessage) error { return c.Relay(host, RelayWork, m) }

func (c *Core) localKey(key string) string {
	if host, session, ok := SplitRemote(key); ok && host == c.SelfName() {
		return session
	}
	return key
}

func (c *Core) sendRemoteLaunch(host string, n NewLaunch, a Assignment) (string, error) {
	g, err := c.Store.Goal(a.Goal)
	if err != nil {
		return "", err
	}
	w := RemoteAssignment{Origin: c.SelfName(), Host: host, Requester: c.SelfName(), Goal: g.ID, Manager: g.ManagerID, Parent: RemoteKey(c.SelfName(), n.Parent), Worker: n.Session, Assignment: a, Launch: &n, State: "approved"}
	if err = c.SettleAssignment(a.ID, RemoteKey(host, n.Session), n.Parent); err != nil {
		return "", err
	}
	if err = c.registerLaunch(w); err != nil {
		return "", err
	}

	return "origin-owned launch intent registered", nil
}

func (c *Core) requestRemoteAssignment(parent, worker, source, reason string, n *NewLaunch) (Assignment, error) {
	root, err := c.remoteOwner(parent)
	if err != nil {
		return Assignment{}, err
	}
	if strings.TrimSpace(source) == "" || strings.TrimSpace(reason) == "" {
		return Assignment{}, errors.New("assignment needs --source and --reason")
	}
	var id string
	err = c.Store.db.QueryRow(`SELECT id FROM ym_remote_work WHERE origin=? AND goal=? AND source=?`, root.Origin, root.Goal, source).Scan(&id)
	if err == nil {
		w, err := c.Store.remoteWork(id)
		if err != nil {
			return Assignment{}, err
		}
		if w.State == "pending" {
			if err = c.queueWork(root.Origin, WorkMessage{Op: "assign", Work: w}); err != nil {
				return w.Assignment, err
			}
			return w.Assignment, fmt.Errorf("%w: %s; replay the same source after approval before native launch", ErrWorkPending, id)
		}
		return w.Assignment, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Assignment{}, err
	}
	a := Assignment{ID: uuid.NewString(), Goal: RemoteKey(root.Origin, root.Goal), Session: worker, Source: source, Reason: reason, State: "pending"}
	host := c.SelfName()
	if n != nil {
		if target, remote := c.RepoHost(n.Repo); remote {
			host = target
		}
	}
	w := RemoteAssignment{Origin: root.Origin, Host: host, Requester: c.SelfName(), Goal: root.Goal, Manager: root.Manager, Parent: RemoteKey(c.SelfName(), parent), ParentAssignment: root.Assignment.ID, Worker: worker, Assignment: a, Launch: n, State: "pending"}
	if err = c.Store.putRemoteWork(w); err != nil {
		return a, err
	}
	if err = c.Store.db.QueryRow(`SELECT id FROM ym_remote_work WHERE origin=? AND goal=? AND source=?`, w.Origin, w.Goal, w.Assignment.Source).Scan(&id); err != nil {
		return a, err
	}
	w, err = c.Store.remoteWork(id)
	if err != nil {
		return a, err
	}
	a = w.Assignment
	if err = c.queueWork(root.Origin, WorkMessage{Op: "assign", Work: w}); err != nil {
		return a, err
	}
	return a, fmt.Errorf("%w: %s; replay the same source after approval before native launch", ErrWorkPending, a.ID)
}

func (c *Core) startForeign(w RemoteAssignment) error {
	current, err := c.Store.remoteWork(w.Assignment.ID)
	if err != nil {
		return err
	}
	switch current.State {
	case "launched", "started", "reported", "accepted", "failed", "revoked":
		return nil
	}
	if current.State == "pending" {
		return ErrWorkPending
	}
	if current.Launch == nil {
		return nil
	}
	return c.queueWork(current.Origin, WorkMessage{Op: "launch-request", Work: current})
}

func (c *Core) launchForeign(w RemoteAssignment) error {
	current, err := c.Store.remoteWork(w.Assignment.ID)
	if err != nil {
		return err
	}
	switch current.State {
	case "started", "launched", "reported", "accepted", "failed", "revoked":
		return nil
	}
	if current.Host != c.SelfName() || current.Launch == nil {
		return errors.New("live dispatch is not addressed to this host")
	}

	if _, err = c.Store.db.Exec(`UPDATE ym_remote_work SET state='starting' WHERE id=? AND state='approved'`, w.Assignment.ID); err != nil {
		return err
	}
	n := *current.Launch
	n.Assignment, n.Source, n.Session, n.Parent = current.Assignment.ID, current.Assignment.ID, current.Worker, c.localKey(current.Parent)
	n.Prompt += fmt.Sprintf("\n\nAssignment %s generation %d belongs to %s/%s. Submit its terminal result with ym goal report %s --summary \"<result and evidence>\"; ym routes it to the originating manager for acceptance.", current.Assignment.ID, current.Assignment.Generation, current.Origin, current.Goal, current.Assignment.ID)
	if c.Launches == nil {
		return errors.New("destination launcher is unavailable")
	}
	st, err := c.Launches.Start(n)
	if err != nil {
		if _, saveErr := c.Store.db.Exec(`UPDATE ym_remote_work SET state='failed' WHERE id=?`, w.Assignment.ID); saveErr != nil {
			return saveErr
		}
		if bindErr := c.settleForeign(current.Assignment.ID, current.Worker); bindErr != nil {
			return bindErr
		}
		return c.queueWork(current.Origin, WorkMessage{Op: "report", Work: current, Summary: "launch failed: " + err.Error()})
	}
	if st.Session != "" {
		if err = c.SettleAssignment(current.Assignment.ID, st.Session, c.localKey(current.Parent)); err != nil {
			return err
		}
	}
	_, err = c.Store.db.Exec(`UPDATE ym_remote_work SET state=CASE WHEN state='starting' THEN 'launched' ELSE state END WHERE id=?`, w.Assignment.ID)
	return err
}

func (c *Core) settleForeign(id, worker string) error {
	self := c.SelfName()
	worker = c.Resolve(worker)
	w, err := c.Store.remoteWork(id)
	if err != nil {
		return err
	}
	if w.State == "pending" {
		return ErrWorkPending
	}
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var bound string
	if err = tx.QueryRow(`SELECT bound_session FROM ym_remote_work WHERE id=?`, id).Scan(&bound); err != nil {
		return err
	}
	successors, learned, err := c.remoteWorkSuccessorsIn(tx, self, w.Origin, worker)
	if err != nil {
		return err
	}
	if bound == worker && !learned {
		return nil
	}
	if err = saveWorkSuccessorsIn(tx, self, successors); err != nil {
		return err
	}
	if bound != worker {
		if _, err = tx.Exec(`UPDATE ym_remote_work SET bound_session=?,session=?,state=CASE WHEN state IN ('approved','starting','launched') THEN 'started' ELSE state END WHERE id=?`, worker, worker, id); err != nil {
			return err
		}
		var newer int
		if err = tx.QueryRow(`SELECT count(*) FROM ym_remote_work WHERE session=? AND rowid>(SELECT rowid FROM ym_remote_work WHERE id=?)`, worker, id).Scan(&newer); err != nil {
			return err
		}
		if newer == 0 {
			if _, err = tx.Exec(`INSERT INTO ym_links(child,parent) VALUES (?,?) ON CONFLICT(child) DO UPDATE SET parent=excluded.parent`, worker, c.localKey(w.Parent)); err != nil {
				return err
			}
		}
	}
	w.Worker = worker
	body := jsonOf(WorkMessage{Op: "bound", Work: w, Successors: successors})
	if _, err = tx.Exec(`INSERT INTO ym_relay(dest,kind,body,created) VALUES (?,?,?,?)`, w.Origin, RelayWork, body, c.now().UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Core) reportForeign(session, id, summary string) (int64, error) {
	w, err := c.Store.remoteWork(id)
	if err != nil {
		return 0, err
	}
	if c.Resolve(session) != c.Resolve(w.Worker) {
		return 0, errors.New("only this assignment's worker may submit its remote terminal report")
	}
	b, err := c.Board()
	if err != nil {
		return 0, err
	}
	if err = c.noticeForeign(b); err != nil {
		return 0, err
	}
	if err = c.settleForeign(id, c.Resolve(session)); err != nil {
		return 0, err
	}
	w.Worker = c.Resolve(session)
	events, err := c.Store.GoalEvents(RemoteKey(w.Origin, w.Goal))
	if err != nil {
		return 0, err
	}
	tx, err := c.Store.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO ym_relay(dest,kind,body,created) VALUES (?,?,?,?)`, w.Origin, RelayWork, jsonOf(WorkMessage{Op: "report", Work: w, Summary: summary, Events: events}), c.now().UnixNano()); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`UPDATE ym_remote_work SET state='reported' WHERE id=? AND state!='accepted'`, id); err != nil {
		return 0, err
	}
	return 0, tx.Commit()
}

func (c *Core) WorkReceive(m WorkMessage) error {
	w := m.Work
	switch m.Op {
	case "register":
		if w.Host != c.SelfName() || w.Origin == "" || w.Assignment.ID == "" || w.Launch != nil {
			return errors.New("invalid assignment route")
		}
		if err := c.Store.putRemoteWork(w); err != nil {
			return err
		}
		return c.settleForeign(w.Assignment.ID, w.Worker)
	case "launch-request":
		return c.registerLaunch(w)
	case "revoke":
		if w.Origin == "" {
			return errors.New("invalid work revocation")
		}
		return c.revokeForeign(w)
	case "goal-closed":
		if w.Origin == "" || w.Goal == "" {
			return errors.New("invalid goal close")
		}
		_, err := c.dismissGoalItems(RemoteKey(w.Origin, w.Goal), m.Summary)
		return err
	case "launch":
		if identity, err := c.Store.RelaySelf(); err != nil {
			return err
		} else if identity.Name == "" && w.Host != "" {
			if err = c.Store.setRelaySelf(RelaySelf{Name: w.Host, Courier: w.Origin, Peers: identity.Peers, At: c.now()}); err != nil {
				return err
			}
		}
		if w.Origin == "" || w.Host != c.SelfName() || w.Assignment.ID == "" || w.Launch == nil {
			return errors.New("remote launch lacks origin, destination or assignment")
		}
		w.State = "approved"
		if err := c.Store.putRemoteWork(w); err != nil {
			return err
		}
		return c.startForeign(w)
	case "assign":
		parent, err := scanAssignment(c.Store.db.QueryRow(`SELECT `+assignmentCols+` FROM ym_assignments WHERE id=?`, w.ParentAssignment))
		if err != nil {
			return err
		}
		if parent.Goal != w.Goal || w.Origin != c.SelfName() {
			return errors.New("remote assignment does not belong to this origin")
		}
		host, _, ok := SplitRemote(parent.Session)
		if !ok || host != w.Requester {
			return errors.New("remote parent session has not bound on this host")
		}
		g, err := c.Store.Goal(parent.Goal)
		if err != nil {
			return err
		}
		if g.State == GoalKilled || g.State == GoalDropped {
			if err := c.queueWork(w.Requester, WorkMessage{Op: "revoke", Work: w}); err != nil {
				return err
			}
			if w.Requester != w.Host {
				return c.queueWork(w.Host, WorkMessage{Op: "revoke", Work: w})
			}
			return nil
		}
		manager, err := c.Store.Manager(g.ManagerID)
		if err != nil {
			return err
		}
		a, _, err := c.Store.assign(manager.Session, RemoteKey(w.Host, w.Worker), "remote:"+w.Requester+":"+w.Assignment.Source, w.Assignment.Reason, c.now(), w.Assignment.ID)
		if err != nil {
			return err
		}
		w.Assignment = a
		return c.queueWork(w.Requester, WorkMessage{Op: "approved", Work: w})
	case "approved":
		existing, err := c.Store.remoteWork(w.Assignment.ID)
		if err != nil {
			return err
		}
		if existing.Origin != w.Origin || existing.Goal != w.Goal {
			return errors.New("assignment approval changed its origin")
		}
		w.Launch, w.Parent, w.Worker = existing.Launch, existing.Parent, existing.Worker
		if _, err = c.Store.db.Exec(`UPDATE ym_remote_work SET generation=?,body=?,state=CASE WHEN state='pending' THEN 'approved' ELSE state END WHERE id=?`, w.Assignment.Generation, jsonOf(w), w.Assignment.ID); err != nil {
			return err
		}
		return c.startForeign(w)
	case "bound":
		defer c.Store.invalidate()
		self := c.SelfName()
		tx, err := c.Store.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		a, err := scanAssignment(tx.QueryRow(`SELECT `+assignmentCols+` FROM ym_assignments WHERE id=?`, w.Assignment.ID))
		if err != nil {
			return err
		}
		host, _, remote := SplitRemote(a.Session)
		if a.Goal != w.Goal || a.Generation != w.Assignment.Generation || !remote || host != w.Host || w.Origin != self {
			return errors.New("binding changed assignment goal")
		}
		if err = saveWorkSuccessorsIn(tx, w.Host, m.Successors); err != nil {
			return err
		}
		if a.Report == 0 {
			if err = settleAssignmentIn(tx, a.ID, RemoteKey(w.Host, w.Worker), ""); err != nil {
				return err
			}
		}
		return tx.Commit()
	case "report":
		a, err := scanAssignment(c.Store.db.QueryRow(`SELECT `+assignmentCols+` FROM ym_assignments WHERE id=?`, w.Assignment.ID))
		if err != nil {
			return err
		}
		if a.Goal != w.Goal || a.Generation != w.Assignment.Generation || a.Session != RemoteKey(w.Host, w.Worker) || w.Origin != c.SelfName() {
			return errors.New("remote report identity has not bound")
		}
		if a.Report != 0 {
			return nil
		}
		for _, e := range m.Events {
			if err = c.WorkReceive(WorkMessage{Op: "event", Work: w, Event: e}); err != nil {
				return err
			}
		}
		if _, err = c.ReportAssignment(a.Session, a.ID, m.Summary); err != nil {
			return err
		}
		return nil
	case "accepted":
		existing, err := c.Store.remoteWork(w.Assignment.ID)
		if err != nil {
			return err
		}
		if existing.Origin != w.Origin || existing.Assignment.Generation != w.Assignment.Generation || existing.Goal != w.Goal {
			return errors.New("acceptance changed assignment identity")
		}
		_, err = c.Store.db.Exec(`UPDATE ym_remote_work SET state='accepted' WHERE id=? AND state!='revoked'`, w.Assignment.ID)
		return err
	case "go":
		if w.Origin != c.SelfName() {
			return errors.New("authorization belongs to another origin")
		}
		a, err := scanAssignment(c.Store.db.QueryRow(`SELECT `+assignmentCols+` FROM ym_assignments WHERE id=?`, w.Assignment.ID))
		if err != nil {
			return err
		}
		host, _, bound := SplitRemote(a.Session)
		if a.Goal != w.Goal || !bound || host != w.Host {
			return errors.New("remote authorization changed its originating assignment")
		}
		tx, err := c.Store.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = workIn(tx, w.Goal, fmt.Sprintf("remote-go:%s:%d", w.Host, m.Item), "authorized remote go item", c.now()); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		c.Store.invalidate()
		return c.queueWork(w.Host, WorkMessage{Op: "go-approved", Work: w, Item: m.Item, Answer: m.Answer})
	case "go-approved":
		it, err := c.Store.Item(fmt.Sprintf("#%d", m.Item))
		if err != nil {
			return err
		}
		if it.GoalOwner != RemoteKey(w.Origin, w.Goal) {
			return errors.New("authorization receipt changed goal ownership")
		}
		if _, err = c.Store.db.Exec(`INSERT INTO ym_remote_go(item,origin) VALUES (?,?) ON CONFLICT DO NOTHING`, m.Item, w.Origin); err != nil {
			return err
		}
		if it.State == ItemAnswered && it.Answer == m.Answer {
			return nil
		}
		b, err := c.Board()
		if err != nil {
			return err
		}
		_, _, err = c.Answer(b, it.Key(), m.Answer)
		return err
	case "event":
		a, err := scanAssignment(c.Store.db.QueryRow(`SELECT `+assignmentCols+` FROM ym_assignments WHERE id=?`, w.Assignment.ID))
		if err != nil {
			return err
		}
		host, _, remote := SplitRemote(a.Session)
		if !remote || host != w.Host || a.Goal != w.Goal || a.Generation != w.Assignment.Generation || w.Origin != c.SelfName() || !prKind(m.Event.Kind) {
			return errors.New("remote PR event changed assignment identity")
		}
		e := m.Event
		e.Goal = a.Goal
		if !strings.HasPrefix(e.Key, RemoteKey(w.Origin, w.Goal)+":") {
			return errors.New("remote PR event changed goal key")
		}
		e.Key = strings.TrimPrefix(e.Key, w.Origin+"/")
		_, _, err = c.Store.AddGoalEvent(e)
		return err
	}
	return fmt.Errorf("unknown work operation %q", m.Op)
}
