package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// commandMax caps how much of a relayed command its outcome repeats; the outcome itself is kept whole.
const commandMax = 120

// noteRelayed records how a relayed ym command turned out on the goal it targets on this host, once per outcome, and names that goal
// and the chief above it on another host in ref.
func (c *Core) noteRelayed(r Relay, out string, err error, ref *RelayRef) error {
	var y relayYM
	if r.Kind != RelayYM || errors.Is(err, errUnusable) || json.Unmarshal([]byte(r.Body), &y) != nil {
		return nil
	}
	target := relayTarget(y.Args)
	if target == "" {
		return nil
	}
	o, lerr := c.LoadOrg()
	if lerr != nil {
		return lerr
	}
	m, lerr := o.lookup(target)
	if lerr != nil {
		return nil
	}
	g, ok := o.Goals[m.GoalID]
	if !ok {
		return nil
	}
	what := "ym " + clip(oneLine(strings.Join(y.Args, " ")), commandMax) + " from " + r.Origin
	kind, outcome, text := EventRelayed, "done", what+": done"
	if out != "" {
		text = what + ": " + oneLine(out)
	}
	if err != nil {
		kind, outcome = EventRelayFailed, "failed"
		if errors.Is(err, ErrFinal) {
			outcome = "refused"
		}
		text = what + " " + outcome + ": " + strings.TrimPrefix(err.Error(), "ym "+strings.Join(y.Args, " ")+": ")
	}
	key := fmt.Sprintf("relayed:%s:%d:%s", r.Origin, r.OriginSeq, outcome)
	if _, _, err := c.RecordGoalEvent(g.ID, kind, text, key); err != nil {
		return err
	}
	ref.Goal, ref.Chief = g.ID, remoteChiefAbove(o, m)
	return nil
}

// relayTarget is the manager, chief or goal a ym command acts on, "" for one that acts on none: a goal start, an item, a session.
func relayTarget(args []string) string {
	if len(args) < 3 {
		return ""
	}
	switch args[0] + " " + args[1] {
	case "goal relaunch", "goal ack", "goal feed", "chief attach":
		return args[2]
	case "goal send":
		for _, a := range args[2:] {
			if !strings.HasPrefix(a, "-") {
				return a
			}
		}
	case "goal steer":
		return args[len(args)-1]
	case "goal pr":
		if args[2] == "drop" && len(args) > 3 {
			return args[3]
		}
	}
	return ""
}

// remoteChiefAbove is the first chief above m that runs on another host, which hears its goal's feed, as this host names it.
func remoteChiefAbove(o Org, m Manager) string {
	seen := map[string]bool{}
	for at := m.ParentID; at != "" && !seen[at]; at = o.Managers[at].ParentID {
		seen[at] = true
		if _, _, remote := SplitRemote(at); remote {
			return at
		}
	}
	return ""
}

// hearsFeed reports whether session is the chief ref names, or one above it, so the goal's feed already reaches it.
func (c *Core) hearsFeed(o Org, session string, ref RelayRef) bool {
	host, chief, remote := SplitRemote(ref.Chief)
	if !remote || host != c.SelfName() {
		return false
	}
	m, ok := o.ManagerOf(c.Resolve(session))
	if !ok {
		return false
	}
	seen := map[string]bool{}
	for at := chief; at != "" && !seen[at]; at = o.Managers[at].ParentID {
		seen[at] = true
		if at == m.ID {
			return true
		}
	}
	return false
}

// noteRefused records refs as Store.noteRefused does, and reports each relay refused for the first time or for good.
func (c *Core) noteRefused(refs []RelayRef) error {
	self := c.SelfName()
	news, err := c.Store.noteRefused(self, refs, c.now())
	errs := []error{err}
	byRef := map[RelayRef]RelayRef{}
	for _, ref := range refs {
		byRef[RelayRef{Origin: ref.Origin, OriginSeq: ref.OriginSeq}] = ref
	}
	for _, r := range news {
		key := RelayRef{Origin: r.Origin, OriginSeq: r.OriginSeq}
		if r.Origin == "" {
			key = RelayRef{Origin: self, OriginSeq: r.Seq}
		}
		errs = append(errs, c.outcome(r, byRef[key]))
	}
	return errors.Join(errs...)
}

// noteDone reports each ym command another host ran for this one, or for a host this one carried it from.
func (c *Core) noteDone(refs []RelayRef) error {
	self := c.SelfName()
	var errs []error
	for _, ref := range refs {
		if ref.Origin != self {
			errs = append(errs, c.Relay(ref.Origin, RelayResult, ref))
			continue
		}
		own, err := c.Store.ownRelay(ref.OriginSeq)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, c.relayOutcome(own, ref))
	}
	return errors.Join(errs...)
}

// outcome reports ref, how relay r turned out, to the host that queued it: here, or back to the host it was carried from.
func (c *Core) outcome(r Relay, ref RelayRef) error {
	if r.Origin != "" {
		ref.Holder, ref.HolderSeq = c.SelfName(), r.Seq
		return c.Relay(r.Origin, RelayResult, ref)
	}
	return c.relayOutcome(r, ref)
}

// heardOutcome is an outcome a host that carried this host's relay on sent back.
func (c *Core) heardOutcome(ref RelayRef) error {
	own, err := c.Store.ownRelay(ref.OriginSeq)
	if err != nil {
		return fmt.Errorf("%w: %w", errUnusable, err)
	}
	return c.relayOutcome(own, ref)
}

// relayOutcome tells the session that queued ym command r how it turned out where it ran, unless its goal's feed reaches that session
// already: what it printed, a message while ym retries it, or an item when it was refused for good, which goes to the owner when no
// session queued it and no goal's feed records it.
func (c *Core) relayOutcome(r Relay, ref RelayRef) error {
	var y relayYM
	if r.Kind != RelayYM || json.Unmarshal([]byte(r.Body), &y) != nil || len(y.Args) == 0 {
		return nil
	}
	if y.Requester == "" && (!ref.Final || ref.Goal != "") {
		return nil
	}
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	if c.hearsFeed(o, y.Requester, ref) {
		return nil
	}
	b, berr := c.Board()
	cmd := "ym " + clip(oneLine(strings.Join(y.Args, " ")), commandMax)
	why := strings.TrimPrefix(ref.Error, "ym "+strings.Join(y.Args, " ")+": ")
	switch {
	case ref.Error == "":
		out := oneLine(ref.Output)
		if out == "" {
			out = "done"
		}
		if berr != nil {
			return berr
		}
		return c.tell(b, y.Requester, fmt.Sprintf("ym: %s on %s: %s", cmd, r.Dest, out), false)
	case !ref.Final:
		if berr != nil {
			return berr
		}
		drop := fmt.Sprintf("ym relay drop %d", r.Seq)
		if ref.Holder != "" {
			drop = fmt.Sprintf("ym relay drop %d on %s", ref.HolderSeq, ref.Holder)
		}
		return c.tell(b, y.Requester, fmt.Sprintf("ym: %s on %s failed: %s; ym retries it each cycle, %s stops it", cmd, r.Dest, oneLine(why), drop), false)
	}
	it := Item{Kind: KindChore, Session: y.Requester, CreatedAt: c.now(), Evidence: []string{"ym " + strings.Join(y.Args, " ")},
		Question: fmt.Sprintf("%s on %s was refused for good: %s", cmd, r.Dest, why),
		Rec:      fmt.Sprintf("rerun %s once %s can take it", cmd, r.Dest)}
	if len(y.Args) > 1 && y.Args[0] == "goal" && strings.HasPrefix(y.Args[1], "--") {
		it.Question = fmt.Sprintf("ym goal on %s did not start %q: %s", r.Dest, clip(oneLine(y.Args[len(y.Args)-1]), lineMax), why)
		it.Rec = fmt.Sprintf("rerun ym goal --host %s once its refusal is dealt with, or start the goal on a host that can take it", r.Dest)
	}
	if it.Session == "" {
		it.Session = "ym"
	}
	if m, ok := o.ManagerOf(c.Resolve(y.Requester)); ok {
		it.Recipient = m.ID
	} else if y.Requester != "" {
		it.Recipient = o.Recipient(c.Resolve(y.Requester), c.parentsFor(b, berr))
	}
	it.Scope = c.ScopeOf(b, it.Session)
	added, err := c.Store.addItem(it)
	if err != nil || added.Recipient == Owner {
		return err
	}
	return c.askManager(b, o, added)
}

// ownRelay is the relay this host queued as seq.
func (s *Store) ownRelay(seq int64) (Relay, error) {
	r := Relay{Seq: seq}
	err := s.db.QueryRow(`SELECT dest, kind, body FROM ym_relay WHERE seq = ? AND origin = ''`, seq).Scan(&r.Dest, &r.Kind, &r.Body)
	return r, err
}
