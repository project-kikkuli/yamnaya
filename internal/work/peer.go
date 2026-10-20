package work

import (
	"encoding/json"
	"time"
)

type Peer struct {
	Host    string
	Scopes  []Scope
	Error   string
	Fetched time.Time
}

func (s *Store) AddPeer(host string) error {
	_, err := s.db.Exec(`INSERT INTO ym_work_peers(host) VALUES(?) ON CONFLICT(host) DO NOTHING`, host)
	return err
}

func (s *Store) SavePeer(p Peer) error {
	raw, err := json.Marshal(p.Scopes)
	if err != nil {
		return err
	}
	if p.Error != "" {
		_, err = s.db.Exec(`UPDATE ym_work_peers SET error=? WHERE host=?`, p.Error, p.Host)
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, w := range p.Scopes {
		old, oldErr := scopeTx(tx, p.Host+"/"+w.ID)
		if oldErr == nil && old.Revision >= w.Revision {
			continue
		}
		if _, err = tx.Exec(`INSERT INTO ym_agent_queue(agent,work,revision,queued) SELECT agent,work,?,? FROM ym_attachments WHERE work=? ON CONFLICT(agent,work) DO UPDATE SET revision=excluded.revision,error=''`, w.Revision, time.Now().UnixNano(), p.Host+"/"+w.ID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE ym_work_peers SET state=?,error='',fetched=? WHERE host=?`, raw, p.Fetched.Format(time.RFC3339Nano), p.Host); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Peers() ([]Peer, error) {
	rows, err := s.db.Query(`SELECT host,state,error,fetched FROM ym_work_peers ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		var p Peer
		var raw []byte
		var at string
		if err := rows.Scan(&p.Host, &raw, &p.Error, &at); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &p.Scopes); err != nil {
				return nil, err
			}
		}
		p.Fetched, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) QueueRemote(host string, e Event) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO ym_work_commands(id,host,event) VALUES(?,?,?)`, e.ID, host, raw)
	return err
}

func (s *Store) RemoteCommands(host string) ([]Event, error) {
	rows, err := s.db.Query(`SELECT event FROM ym_work_commands WHERE host=? AND error='' ORDER BY rowid`, host)
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
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) RemoteResult(id string, err error) error {
	if err == nil {
		_, err = s.db.Exec(`DELETE FROM ym_work_commands WHERE id=?`, id)
		return err
	}
	_, e := s.db.Exec(`UPDATE ym_work_commands SET error=? WHERE id=?`, err.Error(), id)
	return e
}

func (s *Store) RemoteStatus(host string) (map[string]int, map[string][]string, error) {
	rows, err := s.db.Query(`SELECT event,error FROM ym_work_commands WHERE host=?`, host)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	pending := map[string]int{}
	failures := map[string][]string{}
	for rows.Next() {
		var raw []byte
		var message string
		var event Event
		if err = rows.Scan(&raw, &message); err != nil {
			return nil, nil, err
		}
		if err = json.Unmarshal(raw, &event); err != nil {
			return nil, nil, err
		}
		if message == "" {
			pending[event.Work]++
		} else {
			failures[event.Work] = append(failures[event.Work], message)
		}
	}
	return pending, failures, rows.Err()
}
