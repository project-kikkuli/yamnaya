package core

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"
)

const week = 7 * 24 * time.Hour

// RelayUsage is what a host reports of its own usage on the relay round trip, for a host that cannot read its board.
type RelayUsage struct {
	// Machine is the reporting machine's identity, whatever it is called; Names are what else it may have reported under.
	Machine string    `json:"machine,omitempty"`
	Names   []string  `json:"names,omitempty"`
	Ready   bool      `json:"ready"`
	At      time.Time `json:"at"`
	Limits  []Limit   `json:"limits"`
	Today   Spend     `json:"today"`
	Week    Spend     `json:"week"`
}

// ownUsage is this host's report, from its meter alone so that no other host's rows travel with it.
func (c *Core) ownUsage() *RelayUsage {
	if c.Meter == nil {
		return nil
	}
	now := c.now()
	f := c.Meter.Fleet(now)
	if !f.Ready && len(f.Limits) == 0 {
		return nil
	}
	machine, _ := c.Store.MachineID()
	return &RelayUsage{Machine: machine, Names: []string{osHostName()}, Ready: f.Ready, At: now, Limits: f.Limits, Today: f.Today, Week: f.Week}
}

// MachineID is this state directory's identity, made the first time something asks; a host's names can all change, this does not.
func (s *Store) MachineID() (string, error) {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO ym_machine(id,machine) VALUES(1,?)`, uuid.NewString()); err != nil {
		return "", err
	}
	var id string
	err := s.db.QueryRow(`SELECT machine FROM ym_machine WHERE id=1`).Scan(&id)
	return id, err
}

// keepRelayedUsage stores a machine's one report under the name it reported by. Its report under any earlier name goes, and so does a
// report under one of its Names from before reports named their machine. An older peer names no machine, so a rename leaves its old
// row behind until a week of silence, when nothing in it still counts.
func (s *Store) keepRelayedUsage(host string, u RelayUsage, now time.Time) error {
	raw, err := json.Marshal(u)
	if err != nil {
		return err
	}
	old, err := s.relayedUsageBefore(host, u, now)
	if err != nil {
		return err
	}
	for _, name := range old {
		if _, err := s.db.Exec(`DELETE FROM ym_relayed_usage WHERE host=?`, name); err != nil {
			return err
		}
		if u.Machine != "" {
			if err := s.moveNickname(name, host); err != nil {
				return err
			}
		}
	}
	_, err = s.db.Exec(`INSERT INTO ym_relayed_usage(host,body) VALUES(?,?) ON CONFLICT(host) DO UPDATE SET body=excluded.body`, host, string(raw))
	return err
}

// moveNickname hands a renamed machine's nickname to its new name, unless the old name is a host ym reaches or the new one has its own.
func (s *Store) moveNickname(from, to string) error {
	_, err := s.db.Exec(`UPDATE OR IGNORE ym_host_nicknames SET host=? WHERE host=? AND host NOT IN (SELECT name FROM ym_hosts)`, to, from)
	return err
}

// relayedUsageBefore are the rows u replaces, and those of machines that name no identity and have gone a week without reporting.
func (s *Store) relayedUsageBefore(host string, u RelayUsage, now time.Time) ([]string, error) {
	rows, err := s.db.Query(`SELECT host, body FROM ym_relayed_usage WHERE host<>?`, host)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, body string
		if err := rows.Scan(&name, &body); err != nil {
			return nil, err
		}
		var kept RelayUsage
		if json.Unmarshal([]byte(body), &kept) != nil {
			continue
		}
		same := kept.Machine != "" && kept.Machine == u.Machine
		unnamed := kept.Machine == "" && (u.Machine != "" && slices.Contains(u.Names, name) || now.Sub(kept.At) >= week)
		if same || unnamed {
			out = append(out, name)
		}
	}
	return out, rows.Err()
}

func (s *Store) relayedUsageHosts() ([]string, error) {
	rows, err := s.db.Query(`SELECT host FROM ym_relayed_usage ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// usageBoards are the boards MergeFleet reads: those this board reads and the reports other hosts relayed here. A host this board
// also reads shows that read, unless it stopped answering before its latest report arrived.
func (c *Core) usageBoards(read []RemoteBoard) []RemoteBoard {
	boards := slices.Clone(read)
	if !c.ShowRemotes {
		return boards
	}
	rows, err := c.Store.db.Query(`SELECT host, body FROM ym_relayed_usage ORDER BY host`)
	if err != nil {
		return boards
	}
	defer rows.Close()
	self := c.SelfName()
	for rows.Next() {
		var host, body string
		var u RelayUsage
		if rows.Scan(&host, &body) != nil || json.Unmarshal([]byte(body), &u) != nil || host == self {
			continue
		}
		name := host
		if i := slices.IndexFunc(boards, func(r RemoteBoard) bool {
			return r.Host == host || r.Self == host || r.Machine != "" && r.Machine == u.Machine
		}); i >= 0 {
			if boards[i].answersAsOf(u.At) {
				continue
			}
			name = boards[i].Host
			boards[i].Usage = nil
		}
		boards = append(boards, RemoteBoard{Host: name, Self: host, At: u.At, UsageOnly: true,
			Usage: &Fleet{Ready: u.Ready, At: u.At, Limits: u.Limits, Today: u.Today, Week: u.Week}})
	}
	return boards
}

func (r RemoteBoard) answersAsOf(at time.Time) bool {
	return r.Usage != nil && (!r.Stale && r.Err == nil || !r.At.Before(at))
}

// mergedFleet is this host's usage with every other host's this board shows, read or relayed, each as it still stands at now. A
// relayed host with no live window left has no row, as a local login whose window reset has none; Quiet says when it last reported.
func (c *Core) mergedFleet(now time.Time, read []RemoteBoard) Fleet {
	boards := c.usageBoards(read)
	var quiet []RemoteBoard
	for i, r := range boards {
		if r.Usage == nil {
			continue
		}
		live := r.Usage.standingAt(now)
		boards[i].Usage = &live
		if r.UsageOnly && len(live.Limits) == 0 {
			quiet = append(quiet, RemoteBoard{Host: r.Host, At: r.At})
		}
	}
	merged := MergeFleet(c.Meter.Fleet(now), boards)
	merged.Quiet = quiet
	return merged
}

// standingAt is a host's report as it still holds at now: windows that have reset are gone, and its today and 7d count only while they
// still cover now, judged on the calendar of the host that made them.
func (f Fleet) standingAt(now time.Time) Fleet {
	f.Limits = slices.DeleteFunc(slices.Clone(f.Limits), func(l Limit) bool { return !l.ResetsAt.After(now) })
	if f.At.IsZero() {
		return f
	}
	day := func(t time.Time) time.Time {
		y, m, d := t.In(f.At.Location()).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	if !day(now).Equal(day(f.At)) {
		f.Today = Spend{}
	}
	if !day(now).Before(day(f.At).Add(week)) {
		f.Week = Spend{}
	}
	return f
}
