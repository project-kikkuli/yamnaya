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
	// Board is this host's goal tree, shaped as ym board prints it, for a host that cannot read its board over ssh. An older peer sends none.
	Board json.RawMessage `json:"board,omitempty"`
}

// boardRows is the part of a host's board that a pull carries: its goal tree and how each manager stands.
type boardRows struct {
	Self            string               `json:"self"`
	Managers        []Manager            `json:"managers"`
	Goals           []Goal               `json:"goals"`
	ManagerSessions map[string]string    `json:"manager_sessions,omitempty"`
	Standing        map[string]string    `json:"standing,omitempty"`
	Fresh           map[string]Freshness `json:"fresh,omitempty"`
}

// ownBoardRows are this host's rows; how a manager stands here is only what its goal and record say, since no sessions are read for it.
func (c *Core) ownBoardRows(o Org) (json.RawMessage, error) {
	fresh, err := c.freshness(o, nil)
	if err != nil {
		return nil, err
	}
	rows := boardRows{Self: c.SelfName(), ManagerSessions: o.Sessions, Standing: map[string]string{}, Fresh: fresh}
	for _, id := range slices.Sorted(maps.Keys(o.Managers)) {
		m := o.Managers[id]
		rows.Managers = append(rows.Managers, m)
		g, hasGoal := o.Goals[m.GoalID]
		switch {
		case hasGoal && g.State == GoalDone:
			rows.Standing[id] = StandDone
		case hasGoal && g.State == GoalKilled:
			rows.Standing[id] = StandKilled
		case hasGoal && g.State == GoalVerifying:
			rows.Standing[id] = StandVerifying
		case m.Stuck() || hasGoal && g.State == GoalStuck:
			rows.Standing[id] = StandStuck
		default:
			rows.Standing[id] = StandMoving
		}
	}
	for _, id := range slices.Sorted(maps.Keys(o.Goals)) {
		rows.Goals = append(rows.Goals, o.Goals[id])
	}
	return json.Marshal(rows)
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
	board, err := c.ownBoardRows(o)
	if err != nil {
		return nil, err
	}
	return &ItemSnapshot{Board: board, Machine: machine, HostNames: []string{osHostName()}, At: c.now(), Items: items, Parents: parents, Names: names}, nil
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
	known, err := c.Store.KnownHosts()
	if err != nil {
		return err
	}
	for host, snap := range snapshots {
		if host == c.SelfName() {
			continue
		}
		if err := c.Store.noteHostNames(namesInSnapshot(host, snap)); err != nil {
			return err
		}
		if len(snap.Board) > 0 && !slices.ContainsFunc(known, func(h KnownHost) bool { return h.Name == host }) {
			if err := c.Store.keepRelayedBoard(host, snap.At, snap.Board); err != nil {
				return err
			}
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
		if _, err := c.Store.db.Exec(`DELETE FROM ym_host_boards WHERE host=? AND host NOT IN (SELECT name FROM ym_hosts)`, host); err != nil {
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

// withRelayedRows is b, the board of a host that only relays here, with the goal tree it last sent, shown as old once the host goes quiet.
func (s *Store) withRelayedRows(b RemoteBoard, now time.Time) RemoteBoard {
	_, raw, ok, err := s.KeptBoard(b.Host)
	if err != nil || !ok {
		return b
	}
	var rows boardRows
	if json.Unmarshal(raw, &rows) != nil {
		return b
	}
	b.Self, b.Managers, b.Goals, b.ManagerSessions, b.Standing, b.Fresh = rows.Self, rows.Managers, rows.Goals, rows.ManagerSessions, rows.Standing, rows.Fresh
	b.Stale = now.Sub(b.At) > SnapshotLate
	return b
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
