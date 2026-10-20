package work

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *Store) Intake() (Scope, error) {
	w, err := s.Ensure("intake", "", "Incoming requests")
	if err != nil {
		return w, err
	}
	if w.Kind != "intake" {
		return s.Commit(Event{Work: w.ID, Expected: w.Revision, Type: "kind", Actor: "intake", Text: "intake"})
	}
	return w, nil
}

func (s *Store) TransferSources(from, to string) error {
	if from == to {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	src, err := scopeTx(tx, from)
	if err != nil {
		return err
	}
	dst, err := scopeTx(tx, to)
	if err != nil {
		return err
	}
	if dst.Reviews == nil {
		dst.Reviews = map[string]Review{}
	}
	known := map[string]bool{}
	for _, v := range dst.Sources {
		known[v.URL] = true
	}
	for _, v := range src.Sources {
		if !known[v.URL] {
			dst.Sources = append(dst.Sources, v)
		}
	}
	for id, r := range src.Reviews {
		if _, exists := dst.Reviews[id]; !exists {
			if r.State == Preparing || r.State == "incoming" {
				r.State = dst.PreparationState()
			}
			if r.State == Ready && r.PreparedContext != dst.Context.Revision {
				r.State = dst.PreparationState()
			}
			dst.Reviews[id] = r
		}
	}
	if dst.Related == nil {
		dst.Related = map[string]string{}
	}
	dst.Related[src.ID] = "Original intake history and feedback"
	src.Sources = nil
	src.Reviews = map[string]Review{}
	src.State = "archived"
	for _, w := range []*Scope{&src, &dst} {
		w.Revision++
		w.Updated = time.Now().UTC()
		raw, _ := json.Marshal(w)
		if _, err = tx.Exec(`UPDATE ym_work SET state=? WHERE id=?`, raw, w.ID); err != nil {
			return err
		}
	}
	event := Event{ID: fmt.Sprintf("intake-transfer/%s/%s/%d", src.ID, dst.ID, src.Revision), Work: dst.ID, Expected: dst.Revision - 1, Type: "message", Actor: "intake", At: dst.Updated, Text: "Incoming obligations transferred with their evidence and previous feedback; original history: " + src.ID}
	raw, _ := json.Marshal(event)
	if _, err = tx.Exec(`INSERT INTO ym_work_events(id,work,event) VALUES(?,?,?)`, event.ID, dst.ID, raw); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO ym_work_source_errors(work,url,error) SELECT ?,url,error FROM ym_work_source_errors WHERE work=?`, dst.ID, src.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO ym_work_polls(work,url,at) SELECT ?,url,at FROM ym_work_polls WHERE work=?`, dst.ID, src.ID); err != nil {
		return err
	}
	if err = queueAgentEvent(tx, dst, event); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE ym_work_outbox SET delivered=1 WHERE work=?`, src.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MigrateIntake() error {
	all, err := s.List()
	if err != nil {
		return err
	}
	for _, w := range all {
		if strings.HasPrefix(w.Origin, "github/") && (len(w.Sources) > 0 || len(w.Reviews) > 0) {
			target, err := s.Intake()
			if err != nil {
				return err
			}
			if err = s.TransferSources(w.ID, target.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) RetireDiscoveredManagers() error {
	all, err := s.List()
	if err != nil {
		return err
	}
	for _, w := range all {
		if w.Manager == "" || w.Owner != "" {
			continue
		}
		var raw []byte
		err = s.db.QueryRow(`SELECT event FROM ym_work_events WHERE work=? AND json_extract(CAST(event AS TEXT),'$.Type')='manager' ORDER BY seq DESC LIMIT 1`, w.ID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var e Event
		if err = json.Unmarshal(raw, &e); err != nil {
			return err
		}
		if e.Actor == "discovery" {
			if _, err = s.Commit(Event{ID: "retire-discovered-manager/" + w.ID, Work: w.ID, Expected: w.Revision, Type: "manager", Actor: "migration"}); err != nil {
				return err
			}
		}
	}
	return nil
}
