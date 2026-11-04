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
	what := "ym " + clip(oneLine(strings.Join(o.NameArgs("", y.Args), " ")), commandMax) + " from " + r.Origin
	kind, outcome, text := EventRelayed, "done", what+": done"
	if out != "" {
		text = what + ": " + oneLine(out)
	}
	if err != nil {
		kind, outcome = EventRelayFailed, "failed"
		if errors.Is(err, ErrFinal) {
			outcome = "refused"
		}
		text = what + " " + outcome + ": " + relayWhy(y, err.Error())
	}
	key := fmt.Sprintf("relayed:%s:%d:%s", r.Origin, r.OriginSeq, outcome)
	if _, _, err := c.RecordGoalEvent(g.ID, kind, text, key); err != nil {
		return err
	}
	ref.Goal, ref.Chief = g.ID, feedChief(o, m)
	return nil
}

// againRec recommends what to do about a command a host refused for good, naming what it was for and no command: the one it ran would not
// run here as it reads.
func againRec(o Org, y relayYM, host string) string {
	target := relayTarget(y.Args)
	if len(y.Args) > 2 && y.Args[0] == "goal" && y.Args[1] == "close" {
		target = y.Args[2]
	}
	if target == "" {
		return fmt.Sprintf("send the same request again once %s can take it", host)
	}
	return fmt.Sprintf("send the same request for %s again once %s can take it", o.RefOf(RemoteKey(host, target)), host)
}

// relayWhy is what went wrong with a relayed command, without the command as the far ym ran it, which holds ids.
func relayWhy(y relayYM, text string) string {
	raw := strings.Join(y.Args, " ")
	for _, prefix := range []string{"ym " + raw + " --by-name " + y.ByName + ": ", "ym " + raw + ": ", "ym " + CommandName(y.Args) + ": "} {
		if rest, ok := strings.CutPrefix(text, prefix); ok {
			return rest
		}
	}
	return text
}

// CommandName is the ym command args run, without what it acts on or says.
func CommandName(args []string) string {
	if len(args) > 1 && (args[0] == "goal" || args[0] == "chief" || args[0] == "item" || args[0] == "project") && !strings.HasPrefix(args[1], "-") {
		if args[0] == "goal" && idShaped.MatchString(args[1]) {
			return args[0]
		}
		return args[0] + " " + args[1]
	}
	if len(args) > 0 {
		return args[0]
	}
	return ""
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

// feedChief is the chief on another host that m's goal's feed reaches, as this host names it: wakeRemoteChiefs passes over a retired
// manager below it, and one with no live goal right under it, and carries on past a retired chief above it.
func feedChief(o Org, m Manager) string {
	seen := map[string]bool{}
	for !seen[m.ID] && !m.Retired {
		seen[m.ID] = true
		chief := o.chiefOver(m)
		if _, _, remote := SplitRemote(chief); remote {
			if o.Active(m) {
				return chief
			}
			return ""
		}
		next, ok := o.Managers[chief]
		if !ok {
			return ""
		}
		m = next
	}
	return ""
}

// hearsFeed reports whether session is among the chiefs tellChiefs hands the feed ref names to on this host.
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
	for at := chief; at != "" && !seen[at]; {
		seen[at] = true
		up := o.Above(at)
		if up == m.ID {
			return true
		}
		if _, _, remote := SplitRemote(up); remote || up == Owner {
			return false
		}
		at = o.Managers[up].ParentID
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
		ref := byRef[key]
		err := c.outcome(r, ref)
		if err == nil && ref.Final {
			err = c.Store.markRelayed([]int64{r.Seq}, c.now())
		}
		errs = append(errs, err)
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

// relayOutcome reports how ym command r ended once, however often the news of it arrives; a refusal that may clear is only ever
// news once. One left to a goal's feed stays unreported, for the host that ran it to hand back if that feed stops carrying it.
func (c *Core) relayOutcome(r Relay, ref RelayRef) error {
	if r.Kind != RelayYM {
		return nil
	}
	reported, err := c.Store.outcomeReported(r.Seq)
	if err != nil {
		return err
	}
	fed, err := c.leftToFeed(r, ref)
	if err != nil {
		return err
	}
	if ref.Error != "" && !ref.Final {
		// Handed back, it is news only to a session it was left from; any other heard at the first refusal.
		if fed != ref.Unfed || ref.Unfed && reported {
			return nil
		}
		return c.tellOutcome(r, ref)
	}
	if reported || fed && !ref.Unfed {
		return nil
	}
	if err := c.tellOutcome(r, ref); err != nil {
		return err
	}
	return c.Store.markOutcomeReported(r.Seq)
}

// leftToFeed reports whether the feed ref names reaches the session that queued ym command r.
func (c *Core) leftToFeed(r Relay, ref RelayRef) (bool, error) {
	var y relayYM
	if ref.Chief == "" || json.Unmarshal([]byte(r.Body), &y) != nil {
		return false, nil
	}
	o, err := c.LoadOrg()
	if err != nil {
		return false, err
	}
	return c.hearsFeed(o, y.Requester, ref), nil
}

// returnUnfed hands the host that queued a ym command its outcome when the goal's feed it was left to has not carried it and no longer
// can, because the goal ended or a manager on the way retired.
func (c *Core) returnUnfed(o Org) error {
	unfed, err := c.Store.unfedOutcomes()
	if err != nil {
		return err
	}
	var errs []error
	for _, u := range unfed {
		if o.feeds(u.ref.Goal, u.ref.Chief) {
			continue
		}
		u.ref.Unfed = true
		err := c.Relay(u.ref.Origin, RelayResult, u.ref)
		if err == nil {
			err = c.Store.markHeard(u.ref.Chief, []GoalEvent{{Seq: u.event}}, c.now())
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// feeds reports whether goal's feed still reaches chief.
func (o Org) feeds(goal, chief string) bool {
	for _, m := range o.Managers {
		if m.GoalID == goal && feedChief(o, m) == chief {
			return true
		}
	}
	return false
}

// tellOutcome tells the session that queued ym command r how it turned out where it ran: what it printed, a message while ym retries
// it, or an item when it was refused for good, which goes to the owner when no session queued it and no goal's feed records it. A board
// that cannot be read leaves a message queued for the session.
func (c *Core) tellOutcome(r Relay, ref RelayRef) error {
	var y relayYM
	if json.Unmarshal([]byte(r.Body), &y) != nil || len(y.Args) == 0 {
		return nil
	}
	if y.Requester == "" && (!ref.Final || ref.Goal != "") {
		return nil
	}
	o, err := c.orgWithHosts()
	if err != nil {
		return err
	}
	b, berr := c.Board()
	cmd := "ym " + clip(oneLine(strings.Join(o.NameArgs(r.Dest, y.Args), " ")), commandMax)
	why := relayWhy(y, ref.Error)
	switch {
	case ref.Error == "":
		out := oneLine(ref.Output)
		if out == "" {
			out = "done"
		}
		return c.tell(b, y.Requester, fmt.Sprintf("ym: %s on %s: %s", cmd, r.Dest, out), false)
	case !ref.Final:
		text := fmt.Sprintf("ym: %s on %s failed: %s; ym retries it each cycle", cmd, r.Dest, oneLine(why))
		drop := fmt.Sprintf("ym relay drop %d", r.Seq)
		if ref.Holder != "" {
			drop = fmt.Sprintf("ym relay drop %d on %s", ref.HolderSeq, ref.Holder)
		}
		if ref.Unfed {
			// The host handing it back does not know who carried it there.
			held, err := c.Store.relayHeld(r.Seq)
			if err != nil {
				return err
			}
			if !held {
				return c.tell(b, y.Requester, text, false)
			}
		}
		return c.tell(b, y.Requester, text+", "+drop+" stops it", false)
	}
	it := Item{Kind: KindChore, DoLine: "Fix relay to " + clip(r.Dest, MaxDoLine-len("Fix relay to ")), Session: y.Requester, CreatedAt: c.now(), Evidence: []string{cmd},
		Question: fmt.Sprintf("%s on %s was refused for good: %s", cmd, r.Dest, why),
		Rec:      againRec(o, y, r.Dest)}
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
	if err != nil {
		return err
	}
	if err := c.Store.markOutcomeReported(r.Seq); err != nil || added.Recipient == Owner {
		return err
	}
	return c.askManager(b, o, added)
}

func (s *Store) outcomeReported(seq int64) (bool, error) {
	var reported bool
	err := s.db.QueryRow(`SELECT reported FROM ym_relay WHERE seq = ?`, seq).Scan(&reported)
	return reported, err
}

func (s *Store) markOutcomeReported(seq int64) error {
	_, err := s.db.Exec(`UPDATE ym_relay SET reported = 1 WHERE seq = ?`, seq)
	return err
}

// unfedOutcome is a ym command this host ran, how it ended, and the event of it that the chief ref names has yet to be handed.
type unfedOutcome struct {
	ref   RelayRef
	event int64
}

func (s *Store) unfedOutcomes() ([]unfedOutcome, error) {
	rows, err := s.db.Query(`SELECT r.outcome, e.seq FROM ym_relay_seen r JOIN ym_goal_events e
		ON e.key = 'relayed:' || r.origin || ':' || r.origin_seq || ':' || IIF(json_extract(r.outcome, '$.final'), 'refused', 'done')
		WHERE r.outcome != '' AND json_extract(r.outcome, '$.chief') IS NOT NULL AND NOT EXISTS
		(SELECT 1 FROM ym_goal_event_deliveries d WHERE d.event = e.seq AND d.manager = json_extract(r.outcome, '$.chief'))
		UNION ALL
		SELECT f.outcome, e.seq FROM ym_relay_failed f JOIN ym_goal_events e
		ON e.key = 'relayed:' || f.origin || ':' || f.origin_seq || ':failed'
		WHERE NOT EXISTS (SELECT 1 FROM ym_relay_seen r WHERE r.origin = f.origin AND r.origin_seq = f.origin_seq) AND NOT EXISTS
		(SELECT 1 FROM ym_goal_event_deliveries d WHERE d.event = e.seq AND d.manager = json_extract(f.outcome, '$.chief'))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []unfedOutcome
	for rows.Next() {
		var raw string
		var u unfedOutcome
		if err := rows.Scan(&raw, &u.event); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &u.ref); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// noteFailedOutcome keeps how a ym command this host ran first failed while it is left to its goal's feed to tell.
func (s *Store) noteFailedOutcome(ref RelayRef) error {
	raw, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO ym_relay_failed (origin, origin_seq, outcome) VALUES (?, ?, ?)`, ref.Origin, ref.OriginSeq, string(raw))
	return err
}

// relayHeld reports whether this host still holds relay seq to send.
func (s *Store) relayHeld(seq int64) (bool, error) {
	var held bool
	err := s.db.QueryRow(`SELECT delivered IS NULL FROM ym_relay WHERE seq = ?`, seq).Scan(&held)
	return held, err
}

// ownRelay is the relay this host queued as seq.
func (s *Store) ownRelay(seq int64) (Relay, error) {
	r := Relay{Seq: seq}
	err := s.db.QueryRow(`SELECT dest, kind, body FROM ym_relay WHERE seq = ? AND origin = ''`, seq).Scan(&r.Dest, &r.Kind, &r.Body)
	return r, err
}
