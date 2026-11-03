package core

import (
	"encoding/json"
	"maps"
	"slices"
	"time"
)

type ItemSnapshot struct {
	At      time.Time         `json:"at"`
	Items   []Item            `json:"items"`
	Parents map[string]string `json:"parents,omitempty"`
}

func (c *Core) itemSnapshot() (*ItemSnapshot, error) {
	items, err := c.Store.Items(ItemFilter{})
	if err != nil {
		return nil, err
	}
	path, err := c.PathOf(Board{})
	if err != nil {
		return nil, err
	}
	for i, it := range items {
		full, err := c.Store.Item(it.Key())
		if err != nil {
			return nil, err
		}
		full.Path, err = path(it)
		if err != nil {
			return nil, err
		}
		items[i] = full
	}
	o, err := c.LoadOrg()
	if err != nil {
		return nil, err
	}
	parents := map[string]string{}
	for id, m := range o.Managers {
		parents[id] = m.ParentID
	}
	return &ItemSnapshot{At: c.now(), Items: items, Parents: parents}, nil
}

func (c *Core) itemSnapshots() (map[string]ItemSnapshot, error) {
	snapshots, err := c.Store.itemSnapshots()
	if err != nil {
		return nil, err
	}
	local, err := c.itemSnapshot()
	if err != nil {
		return nil, err
	}
	snapshots[c.SelfName()] = *local
	return snapshots, nil
}

func (c *Core) keepItemSnapshots(snapshots map[string]ItemSnapshot) error {
	for host, snap := range snapshots {
		if host == c.SelfName() {
			continue
		}
		raw, err := json.Marshal(snap)
		if err != nil {
			return err
		}
		if _, err := c.Store.db.Exec(`INSERT INTO ym_host_items(host,at,items) VALUES(?,?,?)
			ON CONFLICT(host) DO UPDATE SET at=excluded.at,items=excluded.items WHERE excluded.at >= ym_host_items.at`, host, snap.At.UnixNano(), raw); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) itemSnapshots() (map[string]ItemSnapshot, error) {
	rows, err := s.db.Query(`SELECT host,items FROM ym_host_items ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ItemSnapshot{}
	for rows.Next() {
		var host string
		var raw []byte
		var snap ItemSnapshot
		if err := rows.Scan(&host, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &snap); err != nil {
			return nil, err
		}
		out[host] = snap
	}
	return out, rows.Err()
}

func (c *Core) itemBoards() ([]RemoteBoard, error) {
	snapshots, err := c.Store.itemSnapshots()
	if err != nil {
		return nil, err
	}
	var out []RemoteBoard
	for _, host := range slices.Sorted(maps.Keys(snapshots)) {
		snap := snapshots[host]
		b := RemoteBoard{Host: host, At: snap.At, ItemParents: snap.Parents}
		for _, it := range snap.Items {
			it.Host, it.Scope = host, RemoteKey(host, it.Scope)
			if it.State == ItemOpen {
				b.Items = append(b.Items, it)
			} else {
				b.Closed = append(b.Closed, it)
			}
		}
		out = append(out, b)
	}
	return out, nil
}
