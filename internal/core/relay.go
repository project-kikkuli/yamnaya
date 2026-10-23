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
	// RelayVouch asks a chief's host whether a steer's author is that chief or above it, for the goal's host that cannot see that far.
	RelayVouch = "vouch"
	// RelayVouched tells the goal's host the author is above its chief, so the steer lands.
	RelayVouched = "vouched"
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
	// Error is why the host it went to last refused it; it is sent again until that host acts on it or the owner drops it.
	Error string `json:"error,omitempty"`
	// Refusals counts the times a host refused it in a way a later try may change, the first of them at RefusedSince.
	Refusals     int       `json:"-"`
	RefusedSince time.Time `json:"-"`
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

type relayVouch struct {
	// Chief is the chief on the destination the climb reached; Target names the steered manager by its host.
	Chief  string `json:"chief"`
	Author string `json:"author"`
	Target string `json:"target"`
	Text   string `json:"text"`
	Hops   int    `json:"hops"`
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
	// Refused names the called host's entries the caller could not act on yet, with why; the called host keeps them.
	Refused []RelayRef `json:"refused,omitempty"`
}

// RelayReply is what the called host still has to carry, by its own seq.
type RelayReply struct {
	Entries []Relay `json:"entries"`
	// Refused names the caller's entries this host could not act on or keep yet; the caller keeps them and sends them again.
	Refused []RelayRef `json:"refused,omitempty"`
}

// RelayRef names a relay on every host it passes through.
type RelayRef struct {
	Origin    string `json:"origin"`
	OriginSeq int64  `json:"origin_seq"`
	// Error is why the host refused it, for ym relay to show.
	Error string `json:"error,omitempty"`
	// Final says no later try can change the refusal, so the relay ends with it; the receiver recorded where the owner looks.
	Final bool `json:"final,omitempty"`
}

func (r Relay) ref() RelayRef { return RelayRef{Origin: r.Origin, OriginSeq: r.OriginSeq} }

const (
	// RefusalsBeforeOwner and RefusedBeforeOwner are how often, or how long, a relay is refused before the owner's queue shows it.
	RefusalsBeforeOwner = 3
	RefusedBeforeOwner  = 15 * time.Minute
)

// errUnusable marks a relay that no later attempt can act on, such as one this ym cannot read; it is dropped, not sent again.
var errUnusable = errors.New("unusable relay")

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
	rows, err := s.db.Query(`SELECT seq, origin, origin_seq, dest, kind, body, created, last_error, refusals, refused_since FROM ym_relay WHERE delivered IS NULL ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Relay
	for rows.Next() {
		var r Relay
		var created int64
		var since sql.NullInt64
		if err := rows.Scan(&r.Seq, &r.Origin, &r.OriginSeq, &r.Dest, &r.Kind, &r.Body, &created, &r.Error, &r.Refusals, &since); err != nil {
			return nil, err
		}
		r.Created = time.Unix(0, created)
		if since.Valid {
			r.RefusedSince = time.Unix(0, since.Int64)
		}
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

// relaySeen reports whether the relay origin numbered seq was acted on already.
func (s *Store) relaySeen(origin string, seq int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM ym_relay_seen WHERE origin = ? AND origin_seq = ?`, origin, seq).Scan(&n)
	return n > 0, err
}

func unmarshal(body string, v any) error {
	if err := json.Unmarshal([]byte(body), v); err != nil {
		return fmt.Errorf("%w: %w", errUnusable, err)
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

// relayErrors adds the columns holding why a relay was last refused, how often and since when.
func relayErrors(db *sql.DB) error {
	for _, col := range []struct{ name, def string }{
		{"last_error", "TEXT NOT NULL DEFAULT ''"}, {"refusals", "INTEGER NOT NULL DEFAULT 0"}, {"refused_since", "INTEGER"},
	} {
		var has int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('ym_relay') WHERE name = ?`, col.name).Scan(&has); err != nil {
			return err
		}
		if has > 0 {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE ym_relay ADD COLUMN ` + col.name + ` ` + col.def); err != nil {
			return err
		}
	}
	return nil
}

// noteRefused records why another host refused each of this host's relays refs name, own or carried on: a final refusal ends the
// relay, any other counts toward the owner hearing of it.
func (s *Store) noteRefused(self string, refs []RelayRef, at time.Time) error {
	for _, r := range refs {
		set := `last_error = ?, refusals = refusals + 1, refused_since = COALESCE(refused_since, ?)`
		args := []any{r.Error, at.UnixNano()}
		if r.Final {
			set = `last_error = ?, delivered = ?`
		}
		where, key := ` WHERE delivered IS NULL AND origin = ? AND origin_seq = ?`, []any{r.Origin, r.OriginSeq}
		if r.Origin == self {
			where, key = ` WHERE delivered IS NULL AND origin = '' AND seq = ?`, []any{r.OriginSeq}
		}
		if _, err := s.db.Exec(`UPDATE ym_relay SET `+set+where, append(args, key...)...); err != nil {
			return err
		}
	}
	return nil
}

// DropRelay stops carrying relay seq, as the owner decides for one that keeps being refused.
func (s *Store) DropRelay(seq int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE ym_relay SET delivered = ? WHERE seq = ? AND delivered IS NULL`, time.Now().UnixNano(), seq)
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
	if err := c.Store.noteRefused(c.SelfName(), req.Refused, c.now()); err != nil {
		return RelayReply{}, err
	}
	refused, recvErr := c.Receive(req.Entries)
	out, err := c.outgoing()
	if out == nil {
		out = []Relay{}
	}
	return RelayReply{Entries: out, Refused: refused}, errors.Join(recvErr, err)
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
		for _, r := range out {
			if r.Dest == h {
				mine = append(mine, r)
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
		if err := c.Store.noteRefused(self, reply.Refused, c.now()); err != nil {
			return err
		}
		refused := map[RelayRef]bool{}
		for _, ref := range reply.Refused {
			refused[RelayRef{Origin: ref.Origin, OriginSeq: ref.OriginSeq}] = true
		}
		var sent []int64
		for _, r := range mine {
			if !refused[r.ref()] {
				sent = append(sent, r.Seq)
			}
		}
		if err := c.Store.markRelayed(sent, c.now()); err != nil {
			return err
		}
		if len(reply.Entries) == 0 {
			continue
		}
		failed, err := c.Receive(reply.Entries)
		if err != nil {
			errs = append(errs, err)
		}
		held := map[RelayRef]bool{}
		for _, ref := range failed {
			held[RelayRef{Origin: ref.Origin, OriginSeq: ref.OriginSeq}] = true
		}
		ack := []int64{}
		for _, r := range reply.Entries {
			if !held[r.ref()] {
				ack = append(ack, r.Seq)
			}
		}
		if _, err := cr.Sync(h, RelaySync{From: self, As: h, Peers: peers, Entries: []Relay{}, Ack: ack, Refused: failed}); err != nil {
			errs = append(errs, fmt.Errorf("relay %s: %w", h, err))
		}
	}
	return errors.Join(errs...)
}

// Receive acts on every relay addressed to this host and keeps the rest to carry on; one already acted on does nothing again. It
// returns the relays it could neither act on nor keep, which their sender holds and sends again.
func (c *Core) Receive(entries []Relay) ([]RelayRef, error) {
	self := c.SelfName()
	var errs []error
	var failed []RelayRef
	var b Board
	var berr error
	loaded := false
	for _, r := range entries {
		if r.Origin == self {
			continue
		}
		if r.Dest != self {
			if _, _, err := c.Store.addRelay(Relay{Origin: r.Origin, OriginSeq: r.OriginSeq, Dest: r.Dest, Kind: r.Kind, Body: r.Body, Created: r.Created}); err != nil {
				errs, failed = append(errs, err), append(failed, refused(r, err))
			}
			continue
		}
		seen, err := c.Store.relaySeen(r.Origin, r.OriginSeq)
		if err != nil {
			errs, failed = append(errs, err), append(failed, refused(r, err))
			continue
		}
		if seen {
			continue
		}
		if !loaded {
			b, berr = c.Board()
			loaded = true
		}
		if err := c.act(b, berr, r); err != nil {
			errs = append(errs, fmt.Errorf("relay %s %s from %s: %w", r.Kind, r.Body, r.Origin, err))
			switch {
			case errors.Is(err, ErrFinal):
				ref := refused(r, err)
				ref.Final = true
				failed = append(failed, ref)
				if err := c.noteFinal(r, err); err != nil {
					errs = append(errs, err)
				}
			case !errors.Is(err, errUnusable):
				failed = append(failed, refused(r, err))
				continue
			}
		}
		if _, err := c.Store.firstSight(r.Origin, r.OriginSeq, c.now()); err != nil {
			errs, failed = append(errs, err), append(failed, refused(r, err))
		}
	}
	return failed, errors.Join(errs...)
}

func refused(r Relay, err error) RelayRef {
	ref := r.ref()
	ref.Error = err.Error()
	return ref
}

func (c *Core) act(b Board, berr error, r Relay) error {
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	switch r.Kind {
	case RelayTell:
		var t relayTell
		if err := unmarshal(r.Body, &t); err != nil {
			return err
		}
		up := o.Above(t.Manager)
		if up == Owner {
			return nil
		}
		return c.tellManager(b, o, up, t.Text)
	case RelayAsk:
		var a relayAsk
		if err := unmarshal(r.Body, &a); err != nil {
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
		if err := unmarshal(r.Body, &rt); err != nil {
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
		if err := unmarshal(r.Body, &t); err != nil {
			return err
		}
		return c.tellChiefs(b, o, t.Manager, t.Text)
	case RelayVouch:
		var v relayVouch
		if err := unmarshal(r.Body, &v); err != nil {
			return err
		}
		return c.vouch(b, o, v)
	case RelayVouched:
		var v relayVouch
		if err := unmarshal(r.Body, &v); err != nil {
			return err
		}
		_, id, _ := SplitRemote(v.Target)
		m, err := o.lookup(id)
		if err != nil {
			return fmt.Errorf("%w: %w", errUnusable, err)
		}
		if !o.Active(m) {
			return c.tellManager(b, o, v.Author, fmt.Sprintf("ym: your steer of %s did not land: it has ended.", v.Target))
		}
		_, err = c.applySteer(b, o, m, v.Text, v.Author)
		return err
	case RelayNotify:
		var n relayNotify
		if err := unmarshal(r.Body, &n); err != nil {
			return err
		}
		return c.notify(strings.Replace(n.Title, "ym: ", "ym: "+r.Origin+" ", 1), n.Body)
	case RelayYM:
		var y relayYM
		if err := unmarshal(r.Body, &y); err != nil {
			return err
		}
		if c.RunSelf == nil {
			return fmt.Errorf("ym cannot run itself here: %w", errUnusable)
		}
		_, err := c.RunSelf(y.Args...)
		return err
	}
	return fmt.Errorf("unknown relay kind %q: %w", r.Kind, errUnusable)
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

// vouch answers whether a steer's author is the chief v names or above it: it lets the steer land on the goal's host, asks the next
// host up when the climb leaves this one, or tells the author the goal does not report to it.
func (c *Core) vouch(b Board, o Org, v relayVouch) error {
	self := c.SelfName()
	author := v.Author
	if h, rest, remote := SplitRemote(author); remote && h == self {
		author = rest
	}
	reports, via := climb(o, author, v.Chief)
	reports = reports || author == v.Chief
	host, _, _ := SplitRemote(v.Target)
	switch {
	case reports:
		return c.Relay(host, RelayVouched, v)
	case via != "" && v.Hops < maxHops:
		next, rest, _ := SplitRemote(via)
		v.Chief, v.Hops = rest, v.Hops+1
		return c.Relay(next, RelayVouch, v)
	}
	if _, _, remote := SplitRemote(author); !remote && o.Managers[author].Session == "" {
		return nil
	}
	return c.tellManager(b, o, author, fmt.Sprintf("ym: your steer of %s was refused: it does not report to you.", v.Target))
}

// noteFinal records a relay this host refused for good where the owner reads its outcome: an item's history, or the goal's feed.
func (c *Core) noteFinal(r Relay, err error) error {
	why := err.Error()
	switch r.Kind {
	case RelayYM:
		var y relayYM
		if json.Unmarshal([]byte(r.Body), &y) != nil || len(y.Args) < 3 {
			return nil
		}
		what := "ym " + strings.Join(y.Args, " ") + " from " + r.Origin
		switch y.Args[0] + " " + y.Args[1] {
		case "goal steer":
			return c.noteGoal(y.Args[len(y.Args)-1], what, why, r)
		}
		if y.Args[0] != "item" {
			return nil
		}
		it, ierr := c.Store.Item(y.Args[2])
		if ierr != nil {
			return nil
		}
		note := what + " did not land: " + strings.TrimPrefix(why, "ym "+strings.Join(y.Args, " ")+": ")
		if it.Answer != "" {
			note += "; answered there already: " + it.Answer
		} else if it.Reason != "" {
			note += "; dismissed there already: " + it.Reason
		}
		return c.Store.NoteItem(it, note)
	case RelayVouched:
		var v relayVouch
		if json.Unmarshal([]byte(r.Body), &v) != nil {
			return nil
		}
		_, id, _ := SplitRemote(v.Target)
		return c.noteGoal(id, fmt.Sprintf("the steer %q by %s", v.Text, v.Author), why, r)
	}
	return nil
}

func (c *Core) noteGoal(ref, what, why string, r Relay) error {
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	m, err := o.lookup(ref)
	if err != nil {
		return nil
	}
	g, ok := o.Goals[m.GoalID]
	if !ok {
		return nil
	}
	_, _, err = c.RecordGoalEvent(g.ID, EventUpdate, what+" did not land: "+why, fmt.Sprintf("%s:refused:%s:%d", g.ID, r.Origin, r.OriginSeq))
	return err
}
