package core

import (
	"fmt"
	"maps"
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

// TextNicknames names, by nickname, every host this one knows: those ym remote reached, itself and the hosts its relays reach.
func (s *Store) TextNicknames() (map[string]string, error) {
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
	if r, err := s.RelaySelf(); err == nil {
		for _, h := range append([]string{r.Name, r.Courier}, r.Peers...) {
			if h != "" && !slices.Contains(hosts, h) {
				hosts = append(hosts, h)
			}
		}
	}
	return Nicknames(hosts, set), nil
}

// Nicked is text with each host it names written as @nickname, as in @box/goal5.
func (s *Store) Nicked(text string) string {
	nicks, err := s.TextNicknames()
	if err != nil {
		return text
	}
	return nicked(text, nicks)
}

func nicked(text string, nicks map[string]string) string {
	hosts := slices.SortedFunc(maps.Keys(nicks), func(a, b string) int { return len(b) - len(a) })
	part := func(r byte) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_' || r == '@'
	}
	for _, h := range hosts {
		n := nicks[h]
		if n == "" || n == h {
			continue
		}
		var b strings.Builder
		rest := text
		for {
			i := strings.Index(rest, h)
			if i < 0 {
				b.WriteString(rest)
				break
			}
			end := i + len(h)
			if (i > 0 && part(rest[i-1])) || (end < len(rest) && part(rest[end]) && !(rest[end] == '.' && (end+1 == len(rest) || !part(rest[end+1])))) {
				b.WriteString(rest[:end])
			} else {
				b.WriteString(rest[:i] + "@" + n)
			}
			rest = rest[end:]
		}
		text = b.String()
	}
	return text
}

// Unnicked is ref with a leading @nickname/, or one after #, written as the full host name ym routes by.
func (s *Store) Unnicked(ref string) string {
	at, slash := strings.Index(ref, "@"), strings.Index(ref, "/")
	if at < 0 || slash < at || ref[:at] != "" && ref[:at] != "#" {
		return ref
	}
	nicks, err := s.TextNicknames()
	if err != nil {
		return ref
	}
	for h, n := range nicks {
		if n == ref[at+1:slash] || h == ref[at+1:slash] {
			return ref[:at] + h + ref[slash:]
		}
	}
	return ref
}
