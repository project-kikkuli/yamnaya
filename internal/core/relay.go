package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
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
	// RelayVouchClose and RelayVouchedClose are the same for a close; a host too old to know them refuses them rather than steer.
	RelayVouchClose   = "vouch-close"
	RelayVouchedClose = "vouched-close"
	// RelayNotify raises the owner's desktop notification on the destination, the host the owner sits at.
	RelayNotify = "notify"
	// RelayResult hands a relayed ym command's outcome back to the host that queued it, from a host that carried it on.
	RelayResult = "result"
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
	// Action and Quiet carry a chief's digest, which its host merges into what already waits for it.
	Action Rollup `json:"action,omitempty"`
	Quiet  Rollup `json:"quiet,omitempty"`
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
	// Requester is the session that asked for the command on its origin, which hears there how it turned out.
	Requester string `json:"requester,omitempty"`
	// ByName is how the chief in --by shows; the host that runs the command hands it to --by-name.
	ByName string `json:"by_name,omitempty"`
}

type relayVouch struct {
	// Chief is the chief on the destination the climb reached; Target names the steered manager by its host.
	Chief  string `json:"chief"`
	Author string `json:"author"`
	// AuthorName is how the author shows, so a host that has no board of the author's can still name it.
	AuthorName string `json:"author_name,omitempty"`
	Target     string `json:"target"`
	Text       string `json:"text"`
	Hops       int    `json:"hops"`
	// Close is the close to land, nil on a steer.
	Close *CloseOrder `json:"close,omitempty"`
}

type relayNotify struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// RelaySync is one round trip between a host and a host it reaches over ssh: what the caller carries there and what it confirms.
type RelaySync struct {
	Start *RemoteAssignment `json:"start,omitempty"`
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
	// Done names the called host's ym commands the caller ran, with what they printed.
	Done          []RelayRef              `json:"done,omitempty"`
	ItemSnapshots map[string]ItemSnapshot `json:"item_snapshots,omitempty"`
	// Usage is the caller's own limits and spend, for the board of a host that cannot read the caller.
	Usage *RelayUsage `json:"usage,omitempty"`
}

// RelayReply is what the called host still has to carry, by its own seq.
type RelayReply struct {
	StartFinal bool               `json:"start_final,omitempty"`
	StartError string             `json:"start_error,omitempty"`
	Launches   []RemoteAssignment `json:"launches,omitempty"`
	Entries    []Relay            `json:"entries"`
	// Refused names the caller's entries this host could not act on or keep yet; the caller keeps them and sends them again.
	Refused []RelayRef `json:"refused,omitempty"`
	// Done names the caller's ym commands this host ran, with what they printed.
	Done          []RelayRef              `json:"done,omitempty"`
	ItemSnapshots map[string]ItemSnapshot `json:"item_snapshots,omitempty"`
}

// RelayRef names a relay on every host it passes through.
type RelayRef struct {
	Origin    string `json:"origin"`
	OriginSeq int64  `json:"origin_seq"`
	// Error is why the host refused it, for ym relay to show.
	Error string `json:"error,omitempty"`
	// Final says no later try can change the refusal, so the relay ends with it; the receiver recorded where the owner looks.
	Final bool `json:"final,omitempty"`
	// Output is what a ym command printed where it ran; Goal is the goal there whose feed recorded how it turned out, if any.
	Output string `json:"output,omitempty"`
	Goal   string `json:"goal,omitempty"`
	// Chief is the first chief above Goal on another host, as the host that ran it names it; that chief hears the goal's feed.
	Chief string `json:"chief,omitempty"`
	// Holder and HolderSeq are the host carrying a relay on and its row there, once the origin no longer holds it.
	Holder    string `json:"holder,omitempty"`
	HolderSeq int64  `json:"holder_seq,omitempty"`
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

// firstSight records that the relay origin numbered seq arrived and how it ended here; false when it arrived before.
func (s *Store) firstSight(origin string, seq int64, at time.Time, outcome *RelayRef) (bool, error) {
	stored := ""
	if outcome != nil {
		raw, err := json.Marshal(outcome)
		if err != nil {
			return false, err
		}
		stored = string(raw)
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO ym_relay_seen (origin, origin_seq, at, outcome) VALUES (?, ?, ?, ?)`, origin, seq, at.UnixNano(), stored)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// seenOutcome is how the relay origin numbered seq ended here, if it was a ym command that ended.
func (s *Store) seenOutcome(origin string, seq int64) (RelayRef, bool, error) {
	var stored string
	err := s.db.QueryRow(`SELECT outcome FROM ym_relay_seen WHERE origin = ? AND origin_seq = ?`, origin, seq).Scan(&stored)
	if err != nil || stored == "" {
		return RelayRef{}, false, err
	}
	var ref RelayRef
	err = json.Unmarshal([]byte(stored), &ref)
	return ref, err == nil, err
}

// relayErrors adds the columns holding why a relay was last refused, how often and since when, and how each one acted on ended.
func relayErrors(db *sql.DB) error {
	var has int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('ym_relay_seen') WHERE name = 'outcome'`).Scan(&has); err != nil {
		return err
	}
	if has == 0 {
		if _, err := db.Exec(`ALTER TABLE ym_relay_seen ADD COLUMN outcome TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	for _, col := range []struct{ name, def string }{
		{"last_error", "TEXT NOT NULL DEFAULT ''"}, {"refusals", "INTEGER NOT NULL DEFAULT 0"}, {"refused_since", "INTEGER"}, {"reported", "INTEGER NOT NULL DEFAULT 0"},
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

// noteRefused records why another host refused each of this host's relays refs name, own or carried on: a final refusal is ended by
// its caller once reported, any other counts toward the owner hearing of it. It returns the relays refused for the first time or for good.
func (s *Store) noteRefused(self string, refs []RelayRef, at time.Time) ([]Relay, error) {
	var news []Relay
	for _, r := range refs {
		set := `last_error = ?, refusals = refusals + 1, refused_since = COALESCE(refused_since, ?)`
		args := []any{r.Error, at.UnixNano()}
		if r.Final {
			set, args = `last_error = ?`, []any{r.Error}
		}
		where, key := ` WHERE delivered IS NULL AND origin = ? AND origin_seq = ?`, []any{r.Origin, r.OriginSeq}
		if r.Origin == self {
			where, key = ` WHERE delivered IS NULL AND origin = '' AND seq = ?`, []any{r.OriginSeq}
		}
		res, err := s.db.Exec(`UPDATE ym_relay SET `+set+where, append(args, key...)...)
		if err != nil {
			return news, err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			continue
		}
		var got Relay
		if err := s.db.QueryRow(`SELECT seq, origin, origin_seq, dest, kind, body, refusals FROM ym_relay WHERE`+strings.TrimPrefix(where, " WHERE delivered IS NULL AND"),
			key...).Scan(&got.Seq, &got.Origin, &got.OriginSeq, &got.Dest, &got.Kind, &got.Body, &got.Refusals); err != nil {
			return news, err
		}
		if r.Final || got.Refusals == 1 {
			got.Error = r.Error
			news = append(news, got)
		}
	}
	return news, nil
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
	if req.Start != nil && req.Peers == nil {
		prior, err := c.Store.RelaySelf()
		if err != nil {
			return RelayReply{}, err
		}
		req.Peers = prior.Peers
	}
	if req.As != "" && req.From != "" {
		if err := c.Store.setRelaySelf(RelaySelf{Name: req.As, Courier: req.From, Peers: req.Peers, At: c.now()}); err != nil {
			return RelayReply{}, err
		}
	}
	if err := c.keepItemSnapshots(req.ItemSnapshots); err != nil {
		return RelayReply{}, err
	}
	if req.Start != nil {
		if err := c.StartWork(*req.Start); err != nil {
			return RelayReply{StartError: err.Error(), StartFinal: errors.Is(err, ErrFinal)}, err
		}
	}
	if err := c.drainLocalRevocations(); err != nil {
		return RelayReply{}, err
	}
	if req.Usage != nil && req.From != "" {
		if err := c.Store.keepRelayedUsage(req.From, *req.Usage); err != nil {
			return RelayReply{}, err
		}
	}
	if err := c.noteRefused(req.Refused); err != nil {
		return RelayReply{}, err
	}
	if err := c.noteDone(req.Done); err != nil {
		return RelayReply{}, err
	}
	if err := c.Store.markRelayed(req.Ack, c.now()); err != nil {
		return RelayReply{}, err
	}
	refused, done, recvErr := c.Receive(req.Entries)
	out, err := c.outgoing()
	if out == nil {
		out = []Relay{}
	}
	launches, launchErr := c.Store.pendingLaunches()
	snapshots, snapshotErr := c.itemSnapshots()
	return RelayReply{Entries: out, Refused: refused, Done: done, Launches: launches, ItemSnapshots: snapshots}, errors.Join(recvErr, err, launchErr, snapshotErr)
}

// SyncRelays carries relays between this host and each host it reaches: what is due there goes out, what that host holds comes back
// and is acted on or carried on. A host that does not answer keeps its relays and gets them on a later cycle.
func (c *Core) SyncRelays(hosts []string, cr Courier) error {
	if err := c.drainLocalRevocations(); err != nil {
		return err
	}
	if len(hosts) == 0 {
		return nil
	}
	self := c.SelfName()
	usage := c.ownUsage()
	var errs []error
	if work, ok := cr.(WorkCourier); ok {
		if err := c.dispatchOwn(hosts, work); err != nil {
			errs = append(errs, err)
		}
	}
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
		snapshots, err := c.itemSnapshots()
		if err != nil {
			return err
		}
		reply, err := cr.Sync(h, RelaySync{From: self, As: h, Peers: peers, Entries: mine, Ack: []int64{}, ItemSnapshots: snapshots, Usage: usage})
		if err != nil {
			errs = append(errs, fmt.Errorf("relay %s: %w", h, err))
			continue
		}
		if err := c.keepItemSnapshots(reply.ItemSnapshots); err != nil {
			return err
		}
		if err := errors.Join(c.noteRefused(reply.Refused), c.noteDone(reply.Done)); err != nil {
			errs = append(errs, fmt.Errorf("relay %s: %w", h, err))
			continue
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
		if work, ok := cr.(WorkCourier); ok {
			if err := c.dispatchCarried(h, reply.Launches, hosts, work); err != nil {
				errs = append(errs, err)
			}
		}
		if len(reply.Entries) == 0 {
			continue
		}
		failed, done, err := c.Receive(reply.Entries)
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
		snapshots, err = c.itemSnapshots()
		if err != nil {
			return err
		}
		if _, err := cr.Sync(h, RelaySync{From: self, As: h, Peers: peers, Entries: []Relay{}, Ack: ack, Refused: failed, Done: done, ItemSnapshots: snapshots}); err != nil {
			errs = append(errs, fmt.Errorf("relay %s: %w", h, err))
		}
	}
	return errors.Join(errs...)
}

// Receive acts on every relay addressed to this host and keeps the rest to carry on; one already acted on does nothing again. It
// returns the relays it could neither act on nor keep, which their sender holds and sends again, and the ym commands it ran.
func (c *Core) Receive(entries []Relay) (failed, done []RelayRef, err error) {
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
			// Its sender still holds it, so the answer that said how it ended was lost; say it again.
			ref, ok, err := c.Store.seenOutcome(r.Origin, r.OriginSeq)
			switch {
			case err != nil:
				errs = append(errs, err)
			case ok && ref.Final:
				failed = append(failed, ref)
			case ok:
				done = append(done, ref)
			}
			continue
		}
		if !loaded {
			b, berr = c.Board()
			loaded = true
		}
		out, err := c.act(b, berr, r)
		ref := r.ref()
		if nerr := c.noteRelayed(r, out, err, &ref); nerr != nil {
			errs = append(errs, nerr)
		}
		var ended *RelayRef
		if err != nil {
			errs = append(errs, fmt.Errorf("relay %s %s from %s: %w", r.Kind, r.Body, r.Origin, err))
			ref.Error = err.Error()
			switch {
			case errors.Is(err, ErrFinal):
				ref.Final = true
				failed = append(failed, ref)
				ended = &ref
				if err := c.noteFinal(r, err); err != nil {
					errs = append(errs, err)
				}
			case !errors.Is(err, errUnusable):
				failed = append(failed, ref)
				continue
			}
		} else if r.Kind == RelayYM {
			ref.Output = out
			done = append(done, ref)
			ended = &ref
		}
		if _, err := c.Store.firstSight(r.Origin, r.OriginSeq, c.now(), ended); err != nil {
			errs, failed = append(errs, err), append(failed, refused(r, err))
		}
	}
	return failed, done, errors.Join(errs...)
}

func refused(r Relay, err error) RelayRef {
	ref := r.ref()
	ref.Error = err.Error()
	return ref
}

// act acts on r and returns what a ym command it runs printed.
func (c *Core) act(b Board, berr error, r Relay) (string, error) {
	if r.Kind != RelayYM {
		return "", c.actOn(b, berr, r)
	}
	var y relayYM
	if err := unmarshal(r.Body, &y); err != nil {
		return "", err
	}
	if c.RunSelf == nil {
		return "", fmt.Errorf("ym cannot run itself here: %w", errUnusable)
	}
	args := y.Args
	if y.ByName != "" {
		args = append(slices.Clone(args), "--by-name", y.ByName)
	}
	out, err := c.RunSelf(args...)
	if errors.Is(err, ErrGone) {
		return err.Error(), nil
	}
	return out, err
}

func (c *Core) actOn(b Board, berr error, r Relay) error {
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	switch r.Kind {
	case RelayWork:
		var message WorkMessage
		if err := unmarshal(r.Body, &message); err != nil {
			return err
		}
		return c.WorkReceive(message)
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
		if len(t.Action) > 0 || len(t.Quiet) > 0 {
			return c.digestChiefs(o, t)
		}
		return c.tellChiefs(b, o, t.Manager, t.Text)
	case RelayVouch, RelayVouchClose:
		var v relayVouch
		if err := unmarshal(r.Body, &v); err != nil {
			return err
		}
		return c.vouch(b, o, v)
	case RelayVouchedClose:
		var v relayVouch
		if err := unmarshal(r.Body, &v); err != nil {
			return err
		}
		return c.closeVouched(b, o, v)
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
		if g := o.Goals[m.GoalID]; m.Retired || g.State == GoalKilled || g.State == GoalDropped {
			return c.tellManager(b, o, c.hereFrom(v.Author), fmt.Sprintf("ym: your steer of %s did not land: it has ended.", RemoteKey(c.SelfName(), m.Shown())))
		}
		_, err = c.applySteer(b, o, m, v.Text, c.hereFrom(v.Author), v.AuthorName)
		return err
	case RelayNotify:
		var n relayNotify
		if err := unmarshal(r.Body, &n); err != nil {
			return err
		}
		return c.notify(strings.Replace(n.Title, "ym: ", "ym: "+r.Origin+" ", 1), n.Body)
	case RelayResult:
		var ref RelayRef
		if err := unmarshal(r.Body, &ref); err != nil {
			return err
		}
		return c.heardOutcome(ref)
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

// digestChiefs merges a relayed digest into what waits for the chief it names, or the first one above it that can answer, and for
// every chief above that one, here or through a relay to the host the next one runs on. ym serve hands it over from there.
func (c *Core) digestChiefs(o Org, t relayTell) error {
	seen := map[string]bool{}
	for at := t.Manager; at != "" && !seen[at]; {
		seen[at] = true
		up := o.Above(at)
		if up == Owner {
			return nil
		}
		if host, rest, remote := SplitRemote(up); remote {
			t.Manager = rest
			return c.Relay(host, RelayEvents, t)
		}
		if err := c.Store.addDigest(up, t.Action, t.Quiet); err != nil {
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

// wakeRemoteChiefs hands each chief on another host the state changes of the goals below it here, once they are due: the goals of
// every manager and chief whose chief runs there. A chief hears only what happened after this host first reported to it.
func (c *Core) wakeRemoteChiefs(o Org) error {
	var errs []error
	for _, m := range o.Ordered() {
		chief := o.chiefOver(m)
		if _, _, remote := SplitRemote(chief); !remote {
			continue
		}
		ended := !o.Active(m)
		var goals []string
		for _, below := range o.Subtree(m.ID) {
			if g, ok := o.Goals[below.GoalID]; ok {
				goals = append(goals, g.ID)
			}
		}
		heard, err := c.Store.heardAny(chief)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		events, err := c.Store.chiefPending(chief, goals, time.Time{})
		if ended {
			events = slices.DeleteFunc(events, func(e GoalEvent) bool { return !endingKinds[e.Kind] })
		}
		if err != nil || len(events) == 0 && heard {
			errs = append(errs, err)
			continue
		}
		if !heard {
			before := slices.DeleteFunc(slices.Clone(events), func(e GoalEvent) bool { return endingKinds[e.Kind] })
			errs = append(errs, c.Store.markHeard(chief, before, c.now()))
			if events = slices.DeleteFunc(events, func(e GoalEvent) bool { return !endingKinds[e.Kind] }); len(events) == 0 {
				continue
			}
		}
		host, rest, _ := SplitRemote(chief)
		self := c.SelfName()
		errs = append(errs, c.rollChief(chief, o, events, func(action, quiet Rollup) error {
			text := ""
			if len(action) > 0 {
				text = action.merged(quiet).text()
			}
			return c.Relay(host, RelayEvents, relayTell{Manager: rest, Text: text, Action: action, Quiet: quiet})
		}, self))
	}
	return errors.Join(errs...)
}

// vouch answers whether a steer's or close's author is the chief v names or above it: it lets the order land on the goal's host,
// asks the next host up when the climb leaves this one, or tells the author the goal does not report to it.
func (c *Core) vouch(b Board, o Org, v relayVouch) error {
	author := c.hereFrom(v.Author)
	reports, via := climb(o, author, v.Chief)
	reports = reports || author == v.Chief
	host, _, _ := SplitRemote(v.Target)
	ask, answer, what := RelayVouch, RelayVouched, "steer"
	if v.Close != nil {
		ask, answer, what = RelayVouchClose, RelayVouchedClose, "close"
	}
	switch {
	case reports:
		return c.Relay(host, answer, v)
	case via != "" && v.Hops < maxHops:
		next, rest, _ := SplitRemote(via)
		v.Chief, v.Hops = rest, v.Hops+1
		return c.Relay(next, ask, v)
	}
	if _, _, remote := SplitRemote(author); !remote && o.Managers[author].Session == "" {
		return nil
	}
	return c.tellManager(b, o, author, fmt.Sprintf("ym: your %s of %s was refused: it does not report to you.", what, c.RefName(v.Target)))
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
		return c.noteGoal(id, fmt.Sprintf("the steer %q by %s", v.Text, c.actorName(v.Author, v.AuthorName)), why, r)
	case RelayVouchedClose:
		var v relayVouch
		if json.Unmarshal([]byte(r.Body), &v) != nil {
			return nil
		}
		_, id, _ := SplitRemote(v.Target)
		return c.noteGoal(id, "the close by "+c.actorName(v.Author, v.AuthorName), why, r)
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
