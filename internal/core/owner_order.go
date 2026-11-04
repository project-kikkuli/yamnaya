package core

import (
	"slices"
)

func (n Need) orderKey() string {
	if n.Item != nil {
		return "item:" + n.Item.Key()
	}
	return n.MarkKey()
}

func (s *Store) ownerOrder() ([]string, error) {
	return remembered(s, "owner order", cloneSlice[string], func() ([]string, error) {
		rows, err := s.db.Query(`SELECT key FROM ym_owner_ranks WHERE pos IS NOT NULL ORDER BY pos`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return nil, err
			}
			out = append(out, key)
		}
		return out, rows.Err()
	})
}

func (s *Store) rankHeld(key string) (bool, error) {
	var held bool
	err := s.db.QueryRow(`SELECT count(*) > 0 FROM ym_owner_ranks WHERE key = ?`, key).Scan(&held)
	return held, err
}

func (s *Store) holdRank(key string, clearPlace bool) error {
	defer s.invalidate()
	if clearPlace {
		_, err := s.db.Exec(`INSERT INTO ym_owner_ranks (key) VALUES (?1) ON CONFLICT(key) DO UPDATE SET pos = NULL`, key)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO ym_owner_ranks (key) VALUES (?1) ON CONFLICT(key) DO NOTHING`, key)
	return err
}

// stepRow moves n one place in the order the owner sees and holds its rank. Every row the owner has stepped or stepped past keeps
// that order among those rows, in the places they would hold by score, so the rest still rank by score around them.
func (s *Store) stepRow(b Board, n Need, dir string) error {
	key := n.orderKey()
	if err := s.holdRank(key, false); err != nil {
		return err
	}
	shown := b.Needs
	if !slices.ContainsFunc(shown, func(r Need) bool { return r.orderKey() == key }) {
		shown = b.Later
	}
	seen := make([]string, len(shown))
	at := -1
	for i, r := range shown {
		seen[i] = r.orderKey()
		if seen[i] == key {
			at = i
		}
	}
	other := at - 1
	if dir == "down" {
		other = at + 1
	}
	if at < 0 || other < 0 || other >= len(seen) {
		return nil
	}
	order, err := s.ownerOrder()
	if err != nil {
		return err
	}
	placed := map[string]bool{key: true, seen[other]: true}
	var next []string
	for _, k := range order {
		if slices.Contains(seen, k) {
			placed[k] = true
		} else {
			next = append(next, k)
		}
	}
	seen[at], seen[other] = seen[other], seen[at]
	for _, k := range seen {
		if placed[k] {
			next = append(next, k)
		}
	}
	return s.setOwnerOrder(next)
}

func (s *Store) setOwnerOrder(keys []string) error {
	defer s.invalidate()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE ym_owner_ranks SET pos = NULL`); err != nil {
		return err
	}
	for i, k := range keys {
		if _, err := tx.Exec(`INSERT INTO ym_owner_ranks (key, pos) VALUES (?1, ?2) ON CONFLICT(key) DO UPDATE SET pos = ?2`, k, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func applyOwnerOrder(ns []Need, order []string) {
	place := make(map[string]int, len(order))
	for i, k := range order {
		place[k] = i
	}
	var slots []int
	var held []Need
	for i, n := range ns {
		if _, ok := place[n.orderKey()]; ok {
			slots = append(slots, i)
			held = append(held, n)
		}
	}
	slices.SortStableFunc(held, func(a, c Need) int { return place[a.orderKey()] - place[c.orderKey()] })
	for i, at := range slots {
		ns[at] = held[i]
	}
}
