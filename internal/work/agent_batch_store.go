package work

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const agentBatchSchema = `CREATE TABLE IF NOT EXISTS ym_agent_batches(id TEXT PRIMARY KEY,actor TEXT NOT NULL,text TEXT NOT NULL,created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS ym_agent_recipients(batch TEXT NOT NULL,agent TEXT NOT NULL,name TEXT NOT NULL,workspace TEXT NOT NULL,event TEXT NOT NULL UNIQUE,revision INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',delivered TEXT NOT NULL DEFAULT '',incorporated TEXT NOT NULL DEFAULT '',PRIMARY KEY(batch,agent));
CREATE INDEX IF NOT EXISTS ym_agent_recipient_delivery ON ym_agent_recipients(agent,incorporated,revision);`

func (s *Store) SendAgentBatch(id, actor, text string, recipients []Agent) (AgentBatch, error) {
	if id == "" || actor == "" || strings.TrimSpace(text) == "" || len(recipients) == 0 {
		return AgentBatch{}, errors.New("message needs an identity, actor, text and recipients")
	}
	unique := map[string]Agent{}
	for _, a := range recipients {
		if a.ID == "" || a.Workspace == "" {
			return AgentBatch{}, errors.New("message recipients need persistent agent and workspace identities")
		}
		unique[a.ID] = a
	}
	ids := []string{}
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	tx, err := s.db.Begin()
	if err != nil {
		return AgentBatch{}, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT OR IGNORE INTO ym_agent_batches(id,actor,text,created) VALUES(?,?,?,?)`, id, actor, text, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return AgentBatch{}, err
	}
	var priorActor, priorText string
	if err = tx.QueryRow(`SELECT actor,text FROM ym_agent_batches WHERE id=?`, id).Scan(&priorActor, &priorText); err != nil {
		return AgentBatch{}, err
	}
	if priorActor != actor || priorText != text {
		return AgentBatch{}, errors.New("message identity already belongs to different content")
	}
	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM ym_agent_recipients WHERE batch=?`, id).Scan(&count); err != nil {
		return AgentBatch{}, err
	}
	if count > 0 && count != len(ids) {
		return AgentBatch{}, errors.New("message identity already belongs to different recipients")
	}
	for _, agent := range ids {
		a := unique[agent]
		if count > 0 {
			var workspace string
			if err = tx.QueryRow(`SELECT workspace FROM ym_agent_recipients WHERE batch=? AND agent=?`, id, agent).Scan(&workspace); err != nil || workspace != a.Workspace {
				return AgentBatch{}, errors.New("message identity already belongs to different recipients")
			}
			continue
		}
		raw, _ := json.Marshal([]string{id, a.ID})
		event := "agent-message/" + uuid.NewSHA1(uuid.NameSpaceOID, raw).String()
		if _, err = tx.Exec(`INSERT INTO ym_agent_recipients(batch,agent,name,workspace,event) VALUES(?,?,?,?,?)`, id, a.ID, a.Name, a.Workspace, event); err != nil {
			return AgentBatch{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return AgentBatch{}, err
	}
	if err = s.DispatchAgentBatch(id, false); err != nil {
		return AgentBatch{}, err
	}
	return s.AgentBatch(id)
}

func (s *Store) AgentBatch(id string) (AgentBatch, error) {
	var b AgentBatch
	var created string
	err := s.db.QueryRow(`SELECT id,actor,text,created FROM ym_agent_batches WHERE id=?`, id).Scan(&b.ID, &b.Actor, &b.Text, &created)
	if err != nil {
		return b, err
	}
	b.Created, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return b, err
	}
	rows, err := s.db.Query(`SELECT r.agent,r.name,r.workspace,r.event,r.revision,r.error,r.delivered,r.incorporated,COALESCE(q.error,'') FROM ym_agent_recipients r LEFT JOIN ym_agent_queue q ON q.agent=r.agent AND q.work=r.workspace AND q.revision>=r.revision WHERE r.batch=? ORDER BY r.name,r.agent`, id)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var r AgentRecipient
		var delivered, incorporated, failure string
		if err = rows.Scan(&r.Agent, &r.Name, &r.Workspace, &r.Event, &r.Revision, &r.Error, &delivered, &incorporated, &failure); err != nil {
			return b, err
		}
		if delivered != "" {
			r.Delivered, err = time.Parse(time.RFC3339Nano, delivered)
			if err != nil {
				return b, err
			}
		}
		if incorporated != "" {
			r.Incorporated, err = time.Parse(time.RFC3339Nano, incorporated)
			if err != nil {
				return b, err
			}
		}
		if r.Revision > 0 && r.Incorporated.IsZero() && failure != "" {
			r.Error = failure
		}
		r.State = r.Status()
		b.Recipients = append(b.Recipients, r)
	}
	return b, rows.Err()
}

func (s *Store) AgentBatches() ([]AgentBatch, error) {
	rows, err := s.db.Query(`SELECT id FROM ym_agent_batches ORDER BY created DESC,id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var out []AgentBatch
	for _, id := range ids {
		b, err := s.AgentBatch(id)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (s *Store) DispatchAgentBatch(id string, retry bool) error {
	b, err := s.AgentBatch(id)
	if err != nil {
		return err
	}
	for _, r := range b.Recipients {
		if retry && r.Status() != "failed" || !retry && (r.Error != "" || r.Revision > 0) {
			continue
		}
		a, cause := s.Agent(r.Agent)
		if cause == nil && (a.ID != r.Agent || a.Workspace != r.Workspace) {
			cause = errors.New("recipient identity or workspace changed")
		}
		var w Scope
		if cause == nil {
			w, cause = s.Get(r.Workspace)
		}
		if cause == nil && w.Owner != r.Agent {
			cause = errors.New("recipient no longer owns the selected workspace")
		}
		if cause == nil && r.Revision == 0 {
			_, cause = s.Commit(Event{ID: r.Event, Work: w.ID, Expected: w.Revision, Type: "message", Actor: b.Actor, Text: b.Text, At: b.Created})
		} else if cause == nil {
			_, cause = s.db.Exec(`INSERT INTO ym_agent_queue(agent,work,revision,queued) VALUES(?,?,?,?) ON CONFLICT(agent,work) DO UPDATE SET revision=max(revision,excluded.revision),error='',queued=excluded.queued`, r.Agent, r.Workspace, r.Revision, time.Now().UnixNano())
			if cause == nil {
				_, cause = s.db.Exec(`DELETE FROM ym_agent_notifications WHERE agent=?`, r.Agent)
			}
		}
		failure := ""
		if cause != nil {
			failure = cause.Error()
		}
		if _, err = s.db.Exec(`UPDATE ym_agent_recipients SET error=? WHERE batch=? AND agent=?`, failure, id, r.Agent); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DispatchAgentBatches() error {
	rows, err := s.db.Query(`SELECT DISTINCT batch FROM ym_agent_recipients WHERE revision=0 AND error=''`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = s.DispatchAgentBatch(id, false); err != nil {
			return fmt.Errorf("message %s: %w", id, err)
		}
	}
	return nil
}

func (s *Store) AgentMessages(agent string) ([]AgentMessage, error) {
	rows, err := s.db.Query(`SELECT b.id,r.event,b.actor,b.text,r.revision FROM ym_agent_recipients r JOIN ym_agent_batches b ON b.id=r.batch WHERE r.agent=? AND r.revision>0 AND r.incorporated='' ORDER BY b.created,b.id`, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentMessage
	for rows.Next() {
		var m AgentMessage
		if err = rows.Scan(&m.Batch, &m.Event, &m.Actor, &m.Text, &m.Revision); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) AgentMessagesDelivered(messages []AgentMessage) error {
	for _, m := range messages {
		if _, err := s.db.Exec(`UPDATE ym_agent_recipients SET delivered=? WHERE event=? AND delivered=''`, time.Now().UTC().Format(time.RFC3339Nano), m.Event); err != nil {
			return err
		}
	}
	return nil
}
