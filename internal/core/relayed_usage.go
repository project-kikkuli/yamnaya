package core

import (
	"encoding/json"
	"slices"
	"time"
)

// RelayUsage is what a host reports of its own usage on the relay round trip, for a host that cannot read its board.
type RelayUsage struct {
	Ready  bool      `json:"ready"`
	At     time.Time `json:"at"`
	Limits []Limit   `json:"limits"`
	Today  Spend     `json:"today"`
	Week   Spend     `json:"week"`
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
	return &RelayUsage{Ready: f.Ready, At: now, Limits: f.Limits, Today: f.Today, Week: f.Week}
}

func (s *Store) keepRelayedUsage(host string, u RelayUsage) error {
	raw, err := json.Marshal(u)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO ym_relayed_usage(host,body) VALUES(?,?) ON CONFLICT(host) DO UPDATE SET body=excluded.body`, host, string(raw))
	return err
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
		if i := slices.IndexFunc(boards, func(r RemoteBoard) bool { return r.Host == host || r.Self == host }); i >= 0 {
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

// mergedFleet is this host's usage with every other host's this board shows, read or relayed.
func (c *Core) mergedFleet(now time.Time, read []RemoteBoard) Fleet {
	return MergeFleet(c.Meter.Fleet(now), c.usageBoards(read))
}
