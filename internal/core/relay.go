package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Relay kinds: what one host asks another to do for the goal tree.
const (
	// RelayTell hands a message to a manager or chief on the destination, or to the first one above it that can answer.
	RelayTell = "tell"
	// RelayAsk routes an item that lives on its origin to a manager or chief on the destination.
	RelayAsk = "ask"
	// RelayRoute hands an item on the destination to a new recipient after the one it was routed to could not answer.
	RelayRoute = "route"
	// RelayYM runs a ym command on the destination, such as an answer to an item that lives there.
	RelayYM = "ym"
	// RelayEvents hands a chief on the destination the state changes of goals below it on the origin, and every chief above it too.
	RelayEvents = "events"
	// RelayNotify raises the owner's desktop notification on the destination, the host the owner sits at.
	RelayNotify = "notify"
)

// Relay is one message for another host, kept until a host carrying it there confirms it, so a host that is unreachable loses
// nothing. Origin and OriginSeq name it on every host it passes through, so it takes effect once.
type Relay struct {
	Seq       int64     `json:"seq"`
	Origin    string    `json:"origin"`
	OriginSeq int64     `json:"origin_seq"`
	Dest      string    `json:"dest"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body"`
	Created   time.Time `json:"created"`
}

type relayTell struct {
	Manager string `json:"manager"`
	Text    string `json:"text"`
}

type relayAsk struct {
	// Item is the host the item lives on; Seq numbers it there.
	Item      string `json:"item"`
	Seq       int64  `json:"seq"`
	Recipient string `json:"recipient"`
	Text      string `json:"text"`
	// Hops counts the hosts the ask passed through, so chiefs that report to each other across hosts hand it to the owner.
	Hops int `json:"hops"`
}

// maxHops is how many hosts an ask may cross before it goes to the owner.
const maxHops = 8

type relayRoute struct {
	Seq       int64  `json:"seq"`
	Recipient string `json:"recipient"`
}

type relayYM struct {
	Args []string `json:"args"`
}

type relayNotify struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// RelaySync is one round trip between a host and a host it reaches over ssh: what the caller carries there and what it confirms.
type RelaySync struct {
	// From is the caller's name for itself; As is the caller's name for the host it calls, which every host uses for itself.
	From string `json:"from"`
	As   string `json:"as"`
	// Peers are the other hosts the caller reaches, by the caller's names.
	Peers   []string `json:"peers"`
	Entries []Relay  `json:"entries"`
	// Ack confirms the called host's entries, by its own seq, that the caller now holds.
	Ack []int64 `json:"ack"`
}

// RelayReply is what the called host still has to carry, by its own seq.
type RelayReply struct {
	Entries []Relay `json:"entries"`
}

// Courier carries one round trip to a host.
type Courier interface {
	Sync(host string, req RelaySync) (RelayReply, error)
}

const relaySchema = `
CREATE TABLE IF NOT EXISTS ym_relay (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	origin TEXT NOT NULL DEFAULT '',
	origin_seq INTEGER NOT NULL DEFAULT 0,
	dest TEXT NOT NULL,
	kind TEXT NOT NULL,
	body TEXT NOT NULL,
	created INTEGER NOT NULL,
	delivered INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS ym_relay_origin ON ym_relay(origin, origin_seq) WHERE origin != '';
CREATE TABLE IF NOT EXISTS ym_relay_seen (
	origin TEXT NOT NULL,
	origin_seq INTEGER NOT NULL,
	at INTEGER NOT NULL,
	PRIMARY KEY (origin, origin_seq)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS ym_relay_self (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	name TEXT NOT NULL,
	courier TEXT NOT NULL,
	peers TEXT NOT NULL,
	at INTEGER NOT NULL
);
`

// RelaySelf is how the host that last carried this host's relays names it and the hosts it reaches.
type RelaySelf struct {
	Name, Courier string
	Peers         []string
	At            time.Time
}

func (s *Store) addRelay(r Relay) (Relay, bool, error) {
	if r.Created.IsZero() {
		r.Created = time.Now()
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO ym_relay (origin, origin_seq, dest, kind, body, created) VALUES (?, ?, ?, ?, ?, ?)`,
		r.Origin, r.OriginSeq, r.Dest, r.Kind, r.Body, r.Created.UnixNano())
	if err != nil {
		return r, false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return r, false, err
	}
	r.Seq, err = res.LastInsertId()
	return r, true, err
}

// PendingRelays lists the relays no host has confirmed carrying yet, oldest first.
func (s *Store) PendingRelays() ([]Relay, error) {
	rows, err := s.db.Query(`SELECT seq, origin, origin_seq, dest, kind, body, created FROM ym_relay WHERE delivered IS NULL ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Relay
	for rows.Next() {
		var r Relay
		var created int64
		if err := rows.Scan(&r.Seq, &r.Origin, &r.OriginSeq, &r.Dest, &r.Kind, &r.Body, &created); err != nil {
			return nil, err
		}
		r.Created = time.Unix(0, created)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) markRelayed(seqs []int64, at time.Time) error {
	for _, seq := range seqs {
		if _, err := s.db.Exec(`UPDATE ym_relay SET delivered = ? WHERE seq = ? AND delivered IS NULL`, at.UnixNano(), seq); err != nil {
			return err
		}
	}
	return nil
}

// firstSight records that the relay origin numbered seq arrived; false when it arrived before.
func (s *Store) firstSight(origin string, seq int64, at time.Time) (bool, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO ym_relay_seen (origin, origin_seq, at) VALUES (?, ?, ?)`, origin, seq, at.UnixNano())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// RelaySelf is how this host was last named by a host that carries its relays; zero when none has.
func (s *Store) RelaySelf() (RelaySelf, error) {
	var r RelaySelf
	var peers string
	var at int64
	err := s.db.QueryRow(`SELECT name, courier, peers, at FROM ym_relay_self WHERE id = 1`).Scan(&r.Name, &r.Courier, &peers, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if peers != "" {
		r.Peers = strings.Split(peers, "\n")
	}
	r.At = time.Unix(0, at)
	return r, err
}

func (s *Store) setRelaySelf(r RelaySelf) error {
	_, err := s.db.Exec(`INSERT INTO ym_relay_self (id, name, courier, peers, at) VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, courier=excluded.courier, peers=excluded.peers, at=excluded.at`,
		r.Name, r.Courier, strings.Join(r.Peers, "\n"), r.At.UnixNano())
	return err
}

// SelfName is what other hosts call this one: the name the host carrying its relays gave it, else its own host name.
func (c *Core) SelfName() string {
	if r, err := c.Store.RelaySelf(); err == nil && r.Name != "" {
		return r.Name
	}
	h, err := os.Hostname()
	if err != nil {
		return "localhost"
	}
	first, _, _ := strings.Cut(h, ".")
	return strings.ToLower(first)
}

// SplitRemote splits a host-qualified manager, chief or goal id into its host and the id that host knows it by.
func SplitRemote(id string) (host, rest string, ok bool) {
	i := strings.LastIndex(id, "/")
	if i <= 0 || i == len(id)-1 {
		return "", id, false
	}
	return id[:i], id[i+1:], true
}

// Relay queues body for dest; ym serve, here or on a host that reaches this one, carries it there.
func (c *Core) Relay(dest, kind string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, _, err = c.Store.addRelay(Relay{Dest: dest, Kind: kind, Body: string(raw), Created: c.now()})
	return err
}

// peerHosts are hosts this one routes to only through relays: the host that carries its relays and the hosts that one reaches.
func (c *Core) peerHosts() []string {
	if c.Store == nil {
		return nil
	}
	r, err := c.Store.RelaySelf()
	if err != nil || r.Courier == "" {
		return nil
	}
	return append([]string{r.Courier}, r.Peers...)
}

// outgoing is every pending relay with its origin named, as another host receives it.
func (c *Core) outgoing() ([]Relay, error) {
	pending, err := c.Store.PendingRelays()
	if err != nil {
		return nil, err
	}
	self := c.SelfName()
	for i := range pending {
		if pending[i].Origin == "" {
			pending[i].Origin, pending[i].OriginSeq = self, pending[i].Seq
		}
	}
	return pending, nil
}

// ServeSync answers a host that reaches this one: it records the names that host uses, drops the relays it confirms, takes in what
// it carries, and hands back everything this host still has to deliver.
func (c *Core) ServeSync(req RelaySync) (RelayReply, error) {
	if req.As != "" && req.From != "" {
		if err := c.Store.setRelaySelf(RelaySelf{Name: req.As, Courier: req.From, Peers: req.Peers, At: c.now()}); err != nil {
			return RelayReply{}, err
		}
	}
	if err := c.Store.markRelayed(req.Ack, c.now()); err != nil {
		return RelayReply{}, err
	}
	recvErr := c.Receive(req.Entries)
	out, err := c.outgoing()
	if out == nil {
		out = []Relay{}
	}
	return RelayReply{Entries: out}, errors.Join(recvErr, err)
}

// SyncRelays carries relays between this host and each host it reaches: what is due there goes out, what that host holds comes back
// and is acted on or carried on. A host that does not answer keeps its relays and gets them on a later cycle.
func (c *Core) SyncRelays(hosts []string, cr Courier) error {
	if len(hosts) == 0 {
		return nil
	}
	self := c.SelfName()
	var errs []error
	for _, h := range hosts {
		out, err := c.outgoing()
		if err != nil {
			return err
		}
		mine := []Relay{}
		var sent []int64
		for _, r := range out {
			if r.Dest == h {
				mine, sent = append(mine, r), append(sent, r.Seq)
			}
		}
		var peers []string
		for _, p := range hosts {
			if p != h {
				peers = append(peers, p)
			}
		}
		reply, err := cr.Sync(h, RelaySync{From: self, As: h, Peers: peers, Entries: mine, Ack: []int64{}})
		if err != nil {
			errs = append(errs, fmt.Errorf("relay %s: %w", h, err))
			continue
		}
		if err := c.Store.markRelayed(sent, c.now()); err != nil {
			return err
		}
		if len(reply.Entries) == 0 {
			continue
		}
		var ack []int64
		for _, r := range reply.Entries {
			ack = append(ack, r.Seq)
		}
		if err := c.Receive(reply.Entries); err != nil {
			errs = append(errs, err)
		}
		if _, err := cr.Sync(h, RelaySync{From: self, As: h, Peers: peers, Entries: []Relay{}, Ack: ack}); err != nil {
			errs = append(errs, fmt.Errorf("relay %s: %w", h, err))
		}
	}
	return errors.Join(errs...)
}

// Receive acts on every relay addressed to this host and keeps the rest to carry on; one already received does nothing again.
func (c *Core) Receive(entries []Relay) error {
	self := c.SelfName()
	var errs []error
	var b Board
	var berr error
	loaded := false
	for _, r := range entries {
		if r.Origin == self {
			continue
		}
		if r.Dest != self {
			if _, _, err := c.Store.addRelay(Relay{Origin: r.Origin, OriginSeq: r.OriginSeq, Dest: r.Dest, Kind: r.Kind, Body: r.Body, Created: r.Created}); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		fresh, err := c.Store.firstSight(r.Origin, r.OriginSeq, c.now())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !fresh {
			continue
		}
		if !loaded {
			b, berr = c.Board()
			loaded = true
		}
		if err := c.act(b, berr, r); err != nil {
			errs = append(errs, fmt.Errorf("relay %s %s from %s: %w", r.Kind, r.Body, r.Origin, err))
		}
	}
	return errors.Join(errs...)
}

func (c *Core) act(b Board, berr error, r Relay) error {
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	switch r.Kind {
	case RelayTell:
		var t relayTell
		if err := json.Unmarshal([]byte(r.Body), &t); err != nil {
			return err
		}
		up := o.Above(t.Manager)
		if up == Owner {
			return nil
		}
		return c.tellManager(b, o, up, t.Text)
	case RelayAsk:
		var a relayAsk
		if err := json.Unmarshal([]byte(r.Body), &a); err != nil {
			return err
		}
		if a.Hops == 0 && a.Item != r.Origin {
			// An ask queued before any host named its origin carries the origin's own host name; name it as this side does.
			a.Text = strings.ReplaceAll(a.Text, a.Item+"/#", r.Origin+"/#")
			a.Item = r.Origin
		}
		up := o.Above(a.Recipient)
		if _, _, remote := SplitRemote(up); remote && a.Hops >= maxHops {
			up = Owner
		}
		if up != a.Recipient {
			to := up
			if _, _, remote := SplitRemote(up); !remote && up != Owner && a.Item != c.SelfName() {
				to = RemoteKey(c.SelfName(), up)
			}
			var err error
			if a.Item == c.SelfName() {
				if h, r, remote := SplitRemote(to); remote && h == a.Item {
					to = r
				}
				err = c.Store.SetRecipient(a.Seq, to)
			} else {
				err = c.Relay(a.Item, RelayRoute, relayRoute{Seq: a.Seq, Recipient: to})
			}
			if err != nil || up == Owner {
				return err
			}
		}
		if host, rest, remote := SplitRemote(up); remote {
			return c.Relay(host, RelayAsk, relayAsk{Item: a.Item, Seq: a.Seq, Recipient: rest, Text: a.Text, Hops: a.Hops + 1})
		}
		return c.tell(b, o.Managers[up].Session, a.Text, false)
	case RelayRoute:
		var rt relayRoute
		if err := json.Unmarshal([]byte(r.Body), &rt); err != nil {
			return err
		}
		if h, rest, remote := SplitRemote(rt.Recipient); remote && h == c.SelfName() {
			rt.Recipient = rest
		}
		it, err := c.Store.Item("#" + strconv.FormatInt(rt.Seq, 10))
		if err != nil || it.State != ItemOpen || it.Recipient == rt.Recipient {
			return err
		}
		if err := c.Store.SetRecipient(rt.Seq, rt.Recipient); err != nil {
			return err
		}
		if it.Recipient = rt.Recipient; it.Recipient == Owner {
			return nil
		}
		if _, _, remote := SplitRemote(it.Recipient); remote {
			return nil
		}
		return c.askManager(b, o, it)
	case RelayEvents:
		var t relayTell
		if err := json.Unmarshal([]byte(r.Body), &t); err != nil {
			return err
		}
		return c.tellChiefs(b, o, t.Manager, t.Text)
	case RelayNotify:
		var n relayNotify
		if err := json.Unmarshal([]byte(r.Body), &n); err != nil {
			return err
		}
		return c.notify(strings.Replace(n.Title, "ym: ", "ym: "+r.Origin+" ", 1), n.Body)
	case RelayYM:
		var y relayYM
		if err := json.Unmarshal([]byte(r.Body), &y); err != nil {
			return err
		}
		if c.RunSelf == nil {
			return errors.New("ym cannot run itself here")
		}
		_, err := c.RunSelf(y.Args...)
		return err
	}
	return fmt.Errorf("unknown relay kind %q", r.Kind)
}

// tellManager hands text to the manager or chief id names, on this host or through a relay to the host it runs on.
func (c *Core) tellManager(b Board, o Org, id, text string) error {
	if host, rest, ok := SplitRemote(id); ok {
		return c.Relay(host, RelayTell, relayTell{Manager: rest, Text: text})
	}
	return c.tell(b, o.Managers[id].Session, text, false)
}

// relayAsk routes the item to a manager or chief on another host, naming it as that host sees it.
func (c *Core) relayAsk(b Board, o Org, it Item) error {
	host, rest, _ := SplitRemote(it.Recipient)
	shown := it
	shown.Host = c.SelfName()
	return c.Relay(host, RelayAsk, relayAsk{Item: shown.Host, Seq: it.Seq, Recipient: rest, Text: askText(b, o, shown)})
}

// tellChiefs hands text to the chief id names, or the first one above it that can answer, and to every chief above that one, here
// or through a relay to the host the next one runs on.
func (c *Core) tellChiefs(b Board, o Org, id, text string) error {
	seen := map[string]bool{}
	for at := id; at != "" && !seen[at]; {
		seen[at] = true
		up := o.Above(at)
		if up == Owner {
			return nil
		}
		if host, rest, remote := SplitRemote(up); remote {
			return c.Relay(host, RelayEvents, relayTell{Manager: rest, Text: text})
		}
		if err := c.tell(b, o.Managers[up].Session, text, false); err != nil {
			return err
		}
		at = o.Managers[up].ParentID
	}
	return nil
}

// notify raises the owner's desktop notification here, or relays it to the host that carries this one's relays, where the owner sits.
func (c *Core) notify(title, body string) error {
	if r, err := c.Store.RelaySelf(); err == nil && r.Courier != "" {
		return c.Relay(r.Courier, RelayNotify, relayNotify{Title: title, Body: body})
	}
	if c.Notify == nil {
		return nil
	}
	return c.Notify(title, body)
}

// wakeRemoteChiefs hands each chief on another host the state changes of the goals below it here: the goals of every manager and
// chief whose chief runs there. A chief hears only what happened after this host first reported to it.
func (c *Core) wakeRemoteChiefs(o Org) error {
	var errs []error
	for _, m := range o.Ordered() {
		if _, _, remote := SplitRemote(m.ParentID); !remote || !o.Active(m) {
			continue
		}
		var goals []string
		for _, below := range o.Subtree(m.ID) {
			if g, ok := o.Goals[below.GoalID]; ok {
				goals = append(goals, g.ID)
			}
		}
		heard, err := c.Store.heardAny(m.ParentID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		events, err := c.Store.chiefPending(m.ParentID, goals, time.Time{})
		if err != nil || len(events) == 0 && heard {
			errs = append(errs, err)
			continue
		}
		if heard {
			shown := Org{Goals: map[string]Goal{}}
			self := c.SelfName()
			for id, g := range o.Goals {
				g.ID, g.ManagerID = RemoteKey(self, g.ID), RemoteKey(self, g.ManagerID)
				shown.Goals[id] = g
			}
			host, rest, _ := SplitRemote(m.ParentID)
			if err := c.Relay(host, RelayEvents, relayTell{Manager: rest, Text: "ym: " + chiefText(shown, events)}); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		errs = append(errs, c.Store.markHeard(m.ParentID, events, c.now()))
	}
	return errors.Join(errs...)
}
