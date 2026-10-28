package core

import (
	"fmt"
	"net"
	"slices"
	"strings"
)

const nicknameSchema = `
CREATE TABLE IF NOT EXISTS ym_host_nicknames (
	host TEXT PRIMARY KEY,
	nick TEXT NOT NULL UNIQUE
);
`

// Nicknames names each host as the board shows it: the nickname the owner set, else the shortest dash-separated tail of its
// first DNS label that no other host's label ends in and no host was renamed to.
func Nicknames(hosts []string, set map[string]string) map[string]string {
	out := shortHosts(hosts)
	taken := map[string]bool{}
	for _, h := range hosts {
		if n := set[h]; n != "" {
			out[h], taken[n] = n, true
		}
	}
	for _, h := range hosts {
		if set[h] == "" && taken[out[h]] {
			out[h] = h
		}
	}
	return out
}

func shortHosts(hosts []string) map[string]string {
	label := func(h string) string {
		if _, after, ok := strings.Cut(h, "@"); ok {
			h = after
		}
		if net.ParseIP(h) != nil {
			return h
		}
		first, _, _ := strings.Cut(h, ".")
		return first
	}
	out := map[string]string{}
	for _, h := range hosts {
		parts := strings.Split(label(h), "-")
		out[h] = h
		for k := 1; k <= len(parts); k++ {
			tail := strings.Join(parts[len(parts)-k:], "-")
			if !slices.ContainsFunc(hosts, func(o string) bool {
				return o != h && (label(o) == tail || strings.HasSuffix(label(o), "-"+tail))
			}) {
				out[h] = tail
				break
			}
		}
	}
	return out
}

// SetNicknames are the nicknames the owner gave hosts, by host.
func (s *Store) SetNicknames() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT host, nick FROM ym_host_nicknames`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var h, n string
		if err := rows.Scan(&h, &n); err != nil {
			return nil, err
		}
		out[h] = n
	}
	return out, rows.Err()
}

// HostNicknames names every remembered host as the board shows it.
func (s *Store) HostNicknames() (map[string]string, error) {
	known, err := s.KnownHosts()
	if err != nil {
		return nil, err
	}
	set, err := s.SetNicknames()
	if err != nil {
		return nil, err
	}
	var hosts []string
	for _, h := range known {
		hosts = append(hosts, h.Name)
	}
	return Nicknames(hosts, set), nil
}

// ResolveHost is the remembered host ref names, by its full name or its nickname with or without its @.
func (s *Store) ResolveHost(ref string) (string, error) {
	nicks, err := s.HostNicknames()
	if err != nil {
		return "", err
	}
	if _, ok := nicks[ref]; ok {
		return ref, nil
	}
	for h, n := range nicks {
		if n == strings.TrimPrefix(ref, "@") {
			return h, nil
		}
	}
	return "", fmt.Errorf("ym does not remember a host named %s: %w", ref, ErrNotFound)
}

// RenameHost gives host the nickname nick, or its default nickname back when nick is empty; no two hosts share one.
func (s *Store) RenameHost(host, nick string) (string, error) {
	nick = strings.TrimPrefix(strings.TrimSpace(nick), "@")
	if strings.ContainsAny(nick, " \t/@") {
		return "", fmt.Errorf("a nickname has no spaces, / or @: %q", nick)
	}
	nicks, err := s.HostNicknames()
	if err != nil {
		return "", err
	}
	if _, ok := nicks[host]; !ok {
		return "", fmt.Errorf("ym does not remember %s: %w", host, ErrNotFound)
	}
	for h, n := range nicks {
		if h != host && (n == nick || h == nick) && nick != "" {
			return "", fmt.Errorf("@%s already names %s", nick, h)
		}
	}
	if nick == "" {
		if _, err := s.db.Exec(`DELETE FROM ym_host_nicknames WHERE host=?`, host); err != nil {
			return "", err
		}
		nicks, err = s.HostNicknames()
		return nicks[host], err
	}
	_, err = s.db.Exec(`INSERT INTO ym_host_nicknames(host,nick) VALUES(?,?) ON CONFLICT(host) DO UPDATE SET nick=excluded.nick`, host, nick)
	return nick, err
}
