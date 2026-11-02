package core

import (
	"database/sql"
	"encoding/json"
)

type prCacheFact struct {
	State      string
	Head       string
	CI         string
	Unresolved int
}

func cacheFact(l Landing) prCacheFact {
	ci := ""
	switch {
	case l.Failed > 0:
		ci = "red"
	case l.Pending > 0:
		ci = "pending"
	case l.Passed > 0:
		ci = "green"
	}
	return prCacheFact{State: l.State, Head: l.Head, CI: ci, Unresolved: l.Unresolved}
}

func prCacheIn(tx *sql.Tx, ids string) (map[string]prCacheFact, error) {
	rows, err := tx.Query(`SELECT p.url,p.state,coalesce(json_extract(p.merge_details,'$.OID'),''),p.passed,p.failed,p.pending,p.unresolved FROM ym_prs p JOIN json_each(?) j ON j.value=p.url`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]prCacheFact{}
	for rows.Next() {
		var url string
		var l Landing
		if err := rows.Scan(&url, &l.State, &l.Head, &l.Passed, &l.Failed, &l.Pending, &l.Unresolved); err != nil {
			return nil, err
		}
		out[url] = cacheFact(l)
	}
	return out, rows.Err()
}

func (s *Store) checkedPRCache(ids string) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	cache, err := prCacheIn(tx, ids)
	if err != nil {
		return "", err
	}
	return jsonOf(cache), tx.Commit()
}

func compatibleCache(before string, current map[string]prCacheFact, facts []Fact) bool {
	old := map[string]prCacheFact{}
	if json.Unmarshal([]byte(before), &old) != nil {
		return false
	}
	checked := map[string]prCacheFact{}
	for _, f := range facts {
		if f.Check == "merged" && f.Snapshot != "" {
			var v prCacheFact
			if json.Unmarshal([]byte(f.Snapshot), &v) != nil {
				return false
			}
			checked[f.Subject] = v
		}
	}
	for url, now := range current {
		if prior, ok := old[url]; ok && prior == now {
			continue
		}
		if fresh, ok := checked[url]; !ok || now.State != fresh.State || now.Head != fresh.Head || now.Unresolved != fresh.Unresolved || now.CI != "" && now.CI != fresh.CI {
			return false
		}
	}
	return true
}
