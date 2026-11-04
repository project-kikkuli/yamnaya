package core

import (
	"encoding/json"
	"maps"
	"slices"
	"time"
)

type ItemSnapshot struct {
	// Machine is the snapshot's machine, whatever it is called; HostNames are what else it may have gone by. An older peer sends neither.
	Machine   string            `json:"machine,omitempty"`
	HostNames []string          `json:"host_names,omitempty"`
	At        time.Time         `json:"at"`
	Items     []Item            `json:"items"`
	Parents   map[string]string `json:"parents,omitempty"`
	// Names are this host's goals and chiefs by name, so a host that only hears from it through relays can name them.
	Names map[string]string `json:"names,omitempty"`
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
	parents, names := map[string]string{}, map[string]string{}
	for id, m := range o.Managers {
		parents[id] = m.ParentID
		if m.Shown() != id {
			names[id] = m.Shown()
		}
	}
	for id, g := range o.Goals {
		if g.Shown() != id {
			names[id] = g.Shown()
		}
	}
	machine, err := c.Store.MachineID()
	if err != nil {
		return nil, err
	}
	return &ItemSnapshot{Machine: machine, HostNames: []string{osHostName()}, At: c.now(), Items: items, Parents: parents, Names: names}, nil
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

// keepItemSnapshots takes what from passed on; about a host forgotten here, only that host's own word counts.
func (c *Core) keepItemSnapshots(from string, snapshots map[string]ItemSnapshot) error {
	forgotten, err := c.Store.forgottenHosts()
	if err != nil {
		return err
	}
	for host, snap := range snapshots {
		if host == c.SelfName() || forgotten.has(host, snap.Machine) {
			continue
		}
		if err := c.Store.noteHostNames(namesInSnapshot(host, snap)); err != nil {
			return err
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
	return c.dropRenamedSnapshots()
}

// dropRenamedSnapshots leaves each machine one snapshot, its newest, and none of this machine's own, so hosts stop passing old names
// around. A snapshot from before snapshots named their machine goes when its name is one a machine says it went by, or this one's.
func (c *Core) dropRenamedSnapshots() error {
	snapshots, err := c.Store.itemSnapshots()
	if err != nil {
		return err
	}
	own, err := c.Store.MachineID()
	if err != nil {
		return err
	}
	newest, went := map[string]string{}, map[string]string{}
	for _, host := range slices.Sorted(maps.Keys(snapshots)) {
		snap := snapshots[host]
		if snap.Machine == "" {
			continue
		}
		if cur, ok := newest[snap.Machine]; !ok || snapshots[cur].At.Before(snap.At) {
			newest[snap.Machine] = host
		}
	}
	for _, snap := range snapshots {
		for _, name := range snap.HostNames {
			if snap.Machine != "" {
				went[name] = newest[snap.Machine]
			}
		}
	}
	for host, snap := range snapshots {
		to, gone := "", false
		switch {
		case snap.Machine == own:
			gone = true
		case snap.Machine != "":
			to, gone = newest[snap.Machine], newest[snap.Machine] != host
		case host == osHostName():
			gone = true
		default:
			to, gone = went[host]
		}
		if !gone {
			continue
		}
		if _, err := c.Store.db.Exec(`DELETE FROM ym_host_items WHERE host=?`, host); err != nil {
			return err
		}
		if to != "" {
			if err := c.Store.moveNickname(host, to); err != nil {
				return err
			}
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
