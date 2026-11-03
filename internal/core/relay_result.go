package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// commandMax caps how much of a relayed command its outcome repeats; the outcome itself is kept whole.
const commandMax = 120

// noteRelayed records how a relayed ym command turned out on the goal it names on this host, once per outcome, and returns that
// goal; "" when it names none here, so its origin tells the session that asked for it.
func (c *Core) noteRelayed(r Relay, out string, err error) (string, error) {
	var y relayYM
	if r.Kind != RelayYM || errors.Is(err, errUnusable) || json.Unmarshal([]byte(r.Body), &y) != nil || len(y.Args) < 2 {
		return "", nil
	}
	o, lerr := c.LoadOrg()
	if lerr != nil {
		return "", lerr
	}
	g, ok := relayedGoal(o, y.Args)
	if !ok {
		return "", nil
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
		return "", err
	}
	return g.ID, nil
}

// relayedGoal is the goal on this host that the first manager, chief or goal args name belongs to.
func relayedGoal(o Org, args []string) (Goal, bool) {
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if m, err := o.lookup(a); err == nil {
			g, ok := o.Goals[m.GoalID]
			return g, ok
		}
	}
	return Goal{}, false
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

// relayOutcome tells the session that queued ym command r how it turned out where it ran: what it printed, or an item when it
// failed, which goes to the owner when no session queued a command refused for good. A goal there that recorded the outcome
// reaches its chiefs instead.
func (c *Core) relayOutcome(r Relay, ref RelayRef) error {
	var y relayYM
	if r.Kind != RelayYM || json.Unmarshal([]byte(r.Body), &y) != nil || len(y.Args) == 0 || ref.Goal != "" {
		return nil
	}
	if y.Requester == "" && (ref.Error == "" || !ref.Final) {
		return nil
	}
	cmd := "ym " + clip(oneLine(strings.Join(y.Args, " ")), commandMax)
	b, berr := c.Board()
	if ref.Error == "" {
		out := oneLine(ref.Output)
		if out == "" {
			out = "done"
		}
		if berr != nil {
			return berr
		}
		return c.tell(b, y.Requester, fmt.Sprintf("ym: %s on %s: %s", cmd, r.Dest, out), false)
	}
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	why := strings.TrimPrefix(ref.Error, "ym "+strings.Join(y.Args, " ")+": ")
	it := Item{Kind: KindChore, Session: y.Requester, CreatedAt: c.now(), Evidence: []string{"ym " + strings.Join(y.Args, " ")},
		Question: fmt.Sprintf("%s on %s failed: %s", cmd, r.Dest, why),
		Rec:      fmt.Sprintf("ym retries it each cycle: fix the cause on %s, or stop it with ym relay drop %s", r.Dest, strconv.FormatInt(r.Seq, 10))}
	switch {
	case ref.Final && len(y.Args) > 1 && y.Args[0] == "goal" && strings.HasPrefix(y.Args[1], "--"):
		it.Question = fmt.Sprintf("ym goal on %s did not start %q: %s", r.Dest, clip(oneLine(y.Args[len(y.Args)-1]), lineMax), why)
		it.Rec = fmt.Sprintf("rerun ym goal --host %s once its refusal is dealt with, or start the goal on a host that can take it", r.Dest)
	case ref.Final:
		it.Question = fmt.Sprintf("%s on %s was refused for good: %s", cmd, r.Dest, why)
		it.Rec = fmt.Sprintf("rerun %s once %s can take it", cmd, r.Dest)
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
