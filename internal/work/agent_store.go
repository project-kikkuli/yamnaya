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

const agentSchema = `CREATE TABLE IF NOT EXISTS ym_agents(id TEXT PRIMARY KEY,state BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS ym_attachments(agent TEXT NOT NULL,work TEXT NOT NULL,reason TEXT NOT NULL,PRIMARY KEY(agent,work));
CREATE TABLE IF NOT EXISTS ym_agent_rules(agent TEXT NOT NULL,repo TEXT NOT NULL,PRIMARY KEY(agent,repo));
CREATE TABLE IF NOT EXISTS ym_agent_queue(agent TEXT NOT NULL,work TEXT NOT NULL,revision INTEGER NOT NULL,error TEXT NOT NULL DEFAULT '',queued INTEGER NOT NULL,PRIMARY KEY(agent,work));
CREATE INDEX IF NOT EXISTS ym_agent_due ON ym_agent_queue(error,queued);
CREATE TABLE IF NOT EXISTS ym_agent_checkpoints(agent TEXT PRIMARY KEY,token TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS ym_agent_settings(key TEXT PRIMARY KEY,value INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS ym_agent_notifications(agent TEXT PRIMARY KEY,signature TEXT NOT NULL);`

func queueAgentEvent(tx *sql.Tx, w Scope, e Event) error {
	switch e.Type {
	case "seen", "receipt", "adopt", "bind", "trigger":
		return nil
	}
	rows, err := tx.Query(`SELECT id FROM ym_agents WHERE id=? UNION SELECT agent FROM ym_attachments WHERE work=? UNION SELECT json_extract(CAST(state AS TEXT),'$.Owner') FROM ym_work WHERE id=?`, w.Owner, w.ID, w.Parent)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id sql.NullString
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if id.Valid && id.String != "" {
			ids = append(ids, id.String)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if e.Type == "execution" && id == w.Owner {
			var session string
			if err = tx.QueryRow(`SELECT COALESCE(json_extract(CAST(state AS TEXT),'$.Session'),'') FROM ym_agents WHERE id=?`, id).Scan(&session); err != nil {
				return err
			}
			if session == e.Target {
				continue
			}
		}

		if id == e.Actor {
			continue
		}
		if _, err = tx.Exec(`INSERT INTO ym_agent_queue(agent,work,revision,queued) VALUES(?,?,?,?) ON CONFLICT(agent,work) DO UPDATE SET revision=excluded.revision,error='',queued=CASE WHEN error!='' THEN excluded.queued ELSE queued END`, id, w.ID, w.Revision, e.At.UnixNano()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateAgent(name, role, repo, remit, authority string, execution Execution, actors ...string) (Agent, error) {
	initiator := "owner"
	if len(actors) > 0 {
		initiator = actors[0]
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(remit) == "" || strings.TrimSpace(authority) == "" {
		return Agent{}, errors.New("agent needs a name, remit, and delegated authority")
	}
	if role != "chief" && role != "manager" {
		return Agent{}, errors.New("role must be chief or manager")
	}
	if execution.Directory == "" || (execution.Harness != "codex" && execution.Harness != "claude" && len(execution.Command) == 0) {
		return Agent{}, errors.New("agent needs a working directory and a native harness")
	}
	w, err := s.Ensure("agent/"+uuid.NewString(), repo, name)
	if err != nil {
		return Agent{}, err
	}
	a := Agent{ID: w.ID, Workspace: w.ID, Name: name, Role: role, Repo: repo, Execution: execution, Updated: time.Now().UTC()}
	raw, _ := json.Marshal(a)
	if _, err = s.db.Exec(`INSERT INTO ym_agents(id,state) VALUES(?,?)`, a.ID, raw); err != nil {
		return a, err
	}
	for _, e := range []Event{{Type: "kind", Text: map[string]string{"chief": "chief", "manager": "agent"}[role]}, {Type: "purpose", Text: remit}, {Type: "authority", Text: authority}, {Type: "owner", Target: a.ID}, {Type: "plan", Text: "Read the remit and attached work, establish commitments, and publish an initial brief"}} {
		e.Work, e.Expected, e.Actor = w.ID, w.Revision, initiator
		w, err = s.Commit(e)
		if err != nil {
			return a, err
		}
	}
	return a, nil
}

func (s *Store) Agents() ([]Agent, error) {
	rows, err := s.db.Query(`SELECT state,(SELECT count(*) FROM ym_agent_queue q WHERE q.agent=a.id),COALESCE((SELECT error FROM ym_agent_queue q WHERE q.agent=a.id AND error!='' LIMIT 1),'') FROM ym_agents a ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var raw []byte
		var a Agent
		var pending int
		var failure string
		if err = rows.Scan(&raw, &pending, &failure); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		a.Pending, a.Error = pending, failure
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Agent(id string) (Agent, error) {
	all, err := s.Agents()
	if err != nil {
		return Agent{}, err
	}
	var found []Agent
	for _, a := range all {
		if a.ID == id {
			return a, nil
		}
		if strings.EqualFold(a.Name, id) {
			found = append(found, a)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	return Agent{}, fmt.Errorf("agent %q is missing or ambiguous", id)
}

func (s *Store) SaveAgent(a Agent) error {
	if a.Session != "" {
		known := false
		for _, id := range a.Sessions {
			if id == a.Session {
				known = true
			}
		}
		if !known {
			a.Sessions = append(a.Sessions, a.Session)
		}
	}
	a.Pending = 0
	a.Error = ""
	a.Updated = time.Now().UTC()
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE ym_agents SET state=? WHERE id=?`, raw, a.ID)
	return err
}

func (s *Store) Attach(agent, id, reason string) error {
	a, err := s.Agent(agent)
	if err != nil {
		return err
	}
	w, err := s.AttachmentScope(id)
	if err != nil {
		return err
	}
	if (w.Kind == "chief" || w.Kind == "agent") && w.Owner != "" {
		links, err := s.Attachments()
		if err != nil {
			return err
		}
		all, err := s.List()
		if err != nil {
			return err
		}
		control := map[string]string{}
		for _, v := range all {
			if v.Kind == "chief" || v.Kind == "agent" {
				control[v.ID] = v.Owner
			}
		}
		edges := map[string][]string{}
		for _, v := range links {
			if owner := control[v.Work]; owner != "" {
				edges[v.Agent] = append(edges[v.Agent], owner)
			}
		}
		pending := []string{w.Owner}
		seen := map[string]bool{}
		for len(pending) > 0 {
			id := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if id == a.ID {
				return errors.New("management subscriptions would form a feedback cycle")
			}
			if !seen[id] {
				seen[id] = true
				pending = append(pending, edges[id]...)
			}
		}
	}
	if w.ID == a.Workspace {
		return errors.New("agent already owns its workspace")
	}
	if strings.TrimSpace(reason) == "" {
		return errors.New("attachment needs a responsibility")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT INTO ym_attachments(agent,work,reason) VALUES(?,?,?) ON CONFLICT(agent,work) DO UPDATE SET reason=excluded.reason WHERE reason!=excluded.reason`, a.ID, w.ID, reason)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n > 0 {
		_, err = tx.Exec(`INSERT INTO ym_agent_queue(agent,work,revision,queued) VALUES(?,?,?,?) ON CONFLICT(agent,work) DO UPDATE SET revision=excluded.revision,error=''`, a.ID, w.ID, w.Revision, time.Now().UnixNano())
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Detach(agent, id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM ym_attachments WHERE agent=? AND work=?`, agent, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM ym_agent_queue WHERE agent=? AND work=? AND NOT EXISTS(SELECT 1 FROM ym_work WHERE id=? AND json_extract(CAST(state AS TEXT),'$.Owner')=?)`, agent, id, id, agent); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Attachments() ([]Attachment, error) {
	rows, err := s.db.Query(`SELECT agent,work,reason FROM ym_attachments ORDER BY agent,work`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		var a Attachment
		if err = rows.Scan(&a.Agent, &a.Work, &a.Reason); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Rules() ([]Rule, error) {
	rows, err := s.db.Query(`SELECT agent,repo FROM ym_agent_rules ORDER BY agent,repo`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		if err = rows.Scan(&r.Agent, &r.Repo); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SetRule(agent, repo string, remove bool) error {
	a, err := s.Agent(agent)
	if err != nil {
		return err
	}
	if a.Role != "chief" {
		return errors.New("default attachment requires a chief")
	}
	if remove {
		_, err = s.db.Exec(`DELETE FROM ym_agent_rules WHERE agent=? AND repo=?`, a.ID, repo)
	} else {
		_, err = s.db.Exec(`INSERT OR IGNORE INTO ym_agent_rules(agent,repo) VALUES(?,?)`, a.ID, repo)
	}
	return err
}

func (s *Store) AttachDefaults(w Scope) error {
	if w.Kind == "chief" || w.Kind == "agent" {
		return nil
	}
	rules, err := s.Rules()
	if err != nil {
		return err
	}
	for _, r := range rules {
		if (r.Repo == "" || r.Repo == w.Repo) && r.Agent != w.Owner && r.Agent != w.ID {
			if err = s.Attach(r.Agent, w.ID, "Follow new sessions and carry relevant context under the chief remit"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) AgentSnapshot(id string) (AgentSnapshot, error) {
	a, err := s.Agent(id)
	x := AgentSnapshot{Agent: a, Pending: map[string]int64{}, Errors: map[string]string{}}
	if err != nil {
		return x, err
	}
	x.Workspace, err = s.Get(a.Workspace)
	if err != nil {
		return x, err
	}
	all, err := s.List()
	if err != nil {
		return x, err
	}
	peers, err := s.Peers()
	if err != nil {
		return x, err
	}
	for _, p := range peers {
		for _, w := range p.Scopes {
			w.Host = p.Host
			w.ID = p.Host + "/" + w.ID
			if p.Error != "" {
				w.CoverageErrors = append(w.CoverageErrors, p.Error)
			}
			all = append(all, w)
		}
	}
	attachments, err := s.Attachments()
	if err != nil {
		return x, err
	}
	related := map[string]bool{}
	for _, v := range attachments {
		if v.Agent == a.ID {
			x.Attachments = append(x.Attachments, v)
			related[v.Work] = true
		}
	}
	rules, err := s.Rules()
	if err != nil {
		return x, err
	}
	for _, r := range rules {
		if r.Agent == a.ID {
			x.Rules = append(x.Rules, r)
		}
	}
	rows, err := s.db.Query(`SELECT work,revision,error FROM ym_agent_queue WHERE agent=? ORDER BY queued,work LIMIT 24`, a.ID)
	if err != nil {
		return x, err
	}
	for rows.Next() {
		var work, text string
		var rev int64
		if err = rows.Scan(&work, &rev, &text); err != nil {
			rows.Close()
			return x, err
		}
		x.Pending[work] = rev
		if text != "" {
			x.Errors[work] = text
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return x, err
	}
	for _, w := range all {
		if w.ID != a.Workspace && (related[w.ID] || w.Owner == a.ID || x.Pending[w.ID] > 0) {
			x.Work = append(x.Work, w)
		}
	}
	x.Workspace.Activity, err = s.Recent(x.Workspace.ID)
	if err != nil {
		return x, err
	}
	for i := range x.Work {
		if _, pending := x.Pending[x.Work[i].ID]; pending && x.Work[i].Host == "" {
			x.Work[i].Activity, err = s.Recent(x.Work[i].ID)
			if err != nil {
				return x, err
			}
		}
	}
	x.Messages, err = s.AgentMessages(a.ID)
	return x, err
}

func (s *Store) DueAgents() ([]Agent, error) {
	rows, err := s.db.Query(`SELECT a.state FROM ym_agents a JOIN ym_agent_queue q ON a.id=q.agent WHERE q.error='' AND NOT COALESCE(json_extract(CAST(a.state AS TEXT),'$.Paused'),0) GROUP BY a.id ORDER BY min(q.queued),a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var raw []byte
		var a Agent
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Checkpoint(agent, token string, c Checkpoint) error {
	a, err := s.Agent(agent)
	if err != nil {
		return err
	}
	if strings.TrimSpace(c.Next) == "" && strings.TrimSpace(c.WaitFor) == "" {
		return errors.New("checkpoint needs an independent next action or concrete wait condition")
	}
	if len(c.Inputs) == 0 {
		return errors.New("checkpoint must identify the input revisions incorporated")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	w, err := scopeTx(tx, a.Workspace)
	if err != nil {
		return err
	}
	if c.Brief.Assessment == "" || c.Brief.Context != w.Context.Revision {
		return errors.New("checkpoint needs a brief against current context")
	}
	incorporated := []Scope{}
	for id, revision := range c.Inputs {
		current, err := scopeTx(tx, id)
		if err != nil {
			return err
		}
		if revision > current.Revision {
			return errors.New("checkpoint input revision is in the future")
		}
		for _, r := range current.Reviews {
			if r.State == Completed && r.Judgment != "" && r.FollowThrough == "" && c.WaitFor != "" && c.WakeAt.IsZero() {
				var children int
				if err = tx.QueryRow(`SELECT count(*) FROM ym_work WHERE json_extract(CAST(state AS TEXT),'$.Parent')=? AND json_extract(CAST(state AS TEXT),'$.State') NOT IN('completed','archived')`, current.ID).Scan(&children); err != nil {
					return err
				}
				if children == 0 {
					return errors.New("owner decision still needs follow-through evidence, an independent next action, a scheduled wake, or an accountable child")
				}
			}
		}
		var queued int64
		err = tx.QueryRow(`SELECT revision FROM ym_agent_queue WHERE agent=? AND work=?`, a.ID, current.ID).Scan(&queued)
		if err != nil {
			return fmt.Errorf("input %s is not pending: %w", id, err)
		}
		if revision < queued {
			return ErrConflict
		}
		incorporated = append(incorporated, current)
	}
	for id, version := range c.Brief.Evidence {
		r, ok := w.Reviews[id]
		if !ok || r.Version != version {
			return errors.New("checkpoint evidence changed")
		}
	}
	if w.Adoptions == nil {
		w.Adoptions = map[string]int64{}
	}
	w.Adoptions[a.ID] = w.Context.Revision
	if _, bound := w.Bindings[a.Session]; a.Session != "" && bound {
		w.Bindings[a.Session] = w.Context.Revision
	}
	for _, current := range incorporated {
		if current.ID == w.ID || current.Host != "" {
			continue
		}
		if current.Adoptions == nil {
			current.Adoptions = map[string]int64{}
		}
		current.Adoptions[a.ID] = current.Context.Revision
		raw, _ := json.Marshal(current)
		if _, err = tx.Exec(`UPDATE ym_work SET state=? WHERE id=?`, raw, current.ID); err != nil {
			return err
		}
	}
	c.Brief.Inputs = c.Inputs
	w.Brief = c.Brief
	w.Brief.Next = c.Next
	w.Next, w.WaitFor, w.WakeAt = c.Next, c.WaitFor, c.WakeAt
	if c.WaitFor == "" {
		w.WakeAt = time.Now().UTC()
	}
	w.Revision++
	w.Updated = time.Now().UTC()
	raw, _ := json.Marshal(w)
	if _, err = tx.Exec(`UPDATE ym_work SET state=? WHERE id=?`, raw, w.ID); err != nil {
		return err
	}
	event := Event{ID: uuid.NewString(), Work: w.ID, Expected: w.Revision - 1, Type: "brief", Actor: a.ID, Brief: w.Brief, At: w.Updated, Text: c.WaitFor}
	raw, _ = json.Marshal(event)
	if _, err = tx.Exec(`INSERT INTO ym_work_events(id,work,event) VALUES(?,?,?)`, event.ID, w.ID, raw); err != nil {
		return err
	}
	for id, rev := range c.Inputs {
		at := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err = tx.Exec(`UPDATE ym_agent_recipients SET incorporated=?,delivered=CASE WHEN delivered='' THEN ? ELSE delivered END WHERE agent=? AND workspace=? AND revision>0 AND revision<=? AND incorporated=''`, at, at, a.ID, id, rev); err != nil {
			return err
		}
		if _, err = tx.Exec(`DELETE FROM ym_agent_queue WHERE agent=? AND work=? AND revision<=?`, a.ID, id, rev); err != nil {
			return err
		}
	}
	if err = queueAgentEvent(tx, w, event); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO ym_agent_checkpoints(agent,token) VALUES(?,?) ON CONFLICT(agent) DO UPDATE SET token=excluded.token`, a.ID, token); err != nil {
		return err
	}
	return tx.Commit()
}

func scopeTx(tx *sql.Tx, id string) (Scope, error) {
	var raw []byte
	var w Scope
	err := tx.QueryRow(`SELECT state FROM ym_work WHERE id=? OR origin=?`, id, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) && strings.Contains(id, "/") {
		host, _, _ := strings.Cut(id, "/")
		err = tx.QueryRow(`SELECT state FROM ym_work_peers WHERE host=?`, host).Scan(&raw)
		if err != nil {
			return w, err
		}
		var all []Scope
		if err = json.Unmarshal(raw, &all); err != nil {
			return w, err
		}
		for _, candidate := range all {
			if host+"/"+candidate.ID == id {
				candidate.ID = id
				candidate.Host = host
				return candidate, nil
			}
		}
		return w, sql.ErrNoRows
	}
	if err == nil {
		err = json.Unmarshal(raw, &w)
	}
	return w, err
}

func (s *Store) AgentFailure(id string, inputs map[string]int64, cause error) error {
	text := cause.Error()
	for work, rev := range inputs {
		if _, err := s.db.Exec(`UPDATE ym_agent_queue SET error=? WHERE agent=? AND work=? AND revision<=?`, text, id, work, rev); err != nil {
			return err
		}
	}
	a, err := s.Agent(id)
	if err != nil {
		return err
	}
	w, err := s.Get(a.Workspace)
	if err != nil {
		return err
	}
	_, err = s.Commit(Event{Work: w.ID, Expected: w.Revision, Type: "message", Actor: id, Text: "Execution needs investigation: " + text})
	return err
}

func (s *Store) Assign(id, agent, actor string) (Scope, error) {
	a, err := s.Agent(agent)
	if err != nil {
		return Scope{}, err
	}
	w, err := s.Get(id)
	if err != nil {
		return w, err
	}
	return s.Commit(Event{Work: w.ID, Expected: w.Revision, Type: "owner", Actor: actor, Target: a.ID})
}

func (s *Store) HasCheckpoint(agent, token string) (bool, error) {
	var got string
	err := s.db.QueryRow(`SELECT token FROM ym_agent_checkpoints WHERE agent=?`, agent).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return got == token, err
}

func validateDelegation(tx *sql.Tx, w Scope, e Event) error {
	if (e.Type == "owner" || e.Type == "authority") && e.Actor != "owner" && w.Owner != "" {
		permitted := false
		if w.Parent != "" {
			parent, err := scopeTx(tx, w.Parent)
			if err != nil {
				return err
			}
			permitted = parent.Owner == e.Actor
		}
		if e.Type == "owner" && w.Owner == e.Actor {
			permitted = true
		}
		if !permitted {
			return errors.New("changing delegated ownership or authority requires its delegator or the owner")
		}
	}

	if e.Type == "owner" {
		if _, err := tx.Exec(`UPDATE ym_work_outbox SET delivered=1 WHERE work=? AND destination='@manager'`, w.ID); err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRow(`SELECT count(*) FROM ym_agents WHERE id=?`, e.Target).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return errors.New("owner must be a persistent agent")
		}
		if _, err := tx.Exec(`DELETE FROM ym_agent_queue WHERE work=? AND agent!=? AND agent NOT IN(SELECT agent FROM ym_attachments WHERE work=?)`, w.ID, e.Target, w.ID); err != nil {
			return err
		}
	}
	if e.Type == "parent" {
		parent, err := scopeTx(tx, e.Target)
		if err != nil {
			return err
		}
		if e.Actor != "owner" && parent.Owner != "" && parent.Owner != e.Actor {
			return errors.New("only the accountable agent or owner creates a child commitment")
		}

		seen := map[string]bool{w.ID: true}
		id := e.Target
		for id != "" {
			if seen[id] {
				return errors.New("delegation would create an ownership cycle")
			}
			seen[id] = true
			p, err := scopeTx(tx, id)
			if err != nil {
				return err
			}
			id = p.Parent
		}
	}
	if e.Type == "result" && w.Owner != "" && e.Actor != "owner" && e.Actor != w.Owner {
		return errors.New("only the accountable agent submits result evidence")
	}
	if e.Type == "verify" {
		if e.Actor != "owner" {
			if w.Parent == "" {
				return errors.New("root outcome is reserved for the owner to verify")
			}
			p, err := scopeTx(tx, w.Parent)
			if err != nil {
				return err
			}
			if p.Owner != e.Actor {
				return errors.New("only the delegator or owner verifies this outcome")
			}
		}
		var unfinished int
		if err := tx.QueryRow(`SELECT count(*) FROM ym_work WHERE json_extract(CAST(state AS TEXT),'$.Parent')=? AND json_extract(CAST(state AS TEXT),'$.State') NOT IN('completed','archived')`, w.ID).Scan(&unfinished); err != nil {
			return err
		}
		if unfinished > 0 {
			return errors.New("child outcomes still need verification")
		}
	}
	return nil
}

func (s *Store) AttachmentScope(id string) (Scope, error) {
	if w, err := s.Get(id); err == nil {
		return w, nil
	}
	peers, err := s.Peers()
	if err != nil {
		return Scope{}, err
	}
	for _, p := range peers {
		for _, w := range p.Scopes {
			if p.Host+"/"+w.ID == id {
				w.Host = p.Host
				w.ID = id
				return w, nil
			}
		}
	}
	return Scope{}, sql.ErrNoRows
}

func (s *Store) AgentCapacity() (int, error) {
	var capacity int
	err := s.db.QueryRow(`SELECT value FROM ym_agent_settings WHERE key='capacity'`).Scan(&capacity)
	if errors.Is(err, sql.ErrNoRows) {
		return 4, nil
	}
	return capacity, err
}
func (s *Store) SetAgentCapacity(n int) error {
	if n < 1 {
		return errors.New("capacity must be positive")
	}
	_, err := s.db.Exec(`INSERT INTO ym_agent_settings(key,value) VALUES('capacity',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, n)
	return err
}
func (s *Store) AgentNotified(id, signature string) bool {
	var got string
	s.db.QueryRow(`SELECT signature FROM ym_agent_notifications WHERE agent=?`, id).Scan(&got)
	return got == signature
}
func (s *Store) AgentNotification(id, signature string) error {
	_, err := s.db.Exec(`INSERT INTO ym_agent_notifications(agent,signature) VALUES(?,?) ON CONFLICT(agent) DO UPDATE SET signature=excluded.signature`, id, signature)
	return err
}
