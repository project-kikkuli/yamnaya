package core

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// How a chief or the owner closes a goal; CloseFinishedOverride is the disposition of a finish past the verifier.
const (
	CloseFinished         = "finished"
	CloseSuperseded       = "superseded"
	CloseAbandoned        = "abandoned"
	CloseFinishedOverride = "finished-override"
)

// CloseOrder is one ym goal close. Author is Owner, or the chief closing.
type CloseOrder struct {
	Ref      string `json:"-"`
	As       string `json:"as"`
	Why      string `json:"why"`
	Override bool   `json:"override,omitempty"`
	Author   string `json:"-"`
	// AuthorName is the name a chief on another host came with.
	AuthorName string `json:"-"`
}

// CloseAuthor is who closes for the calling session: session is "" for a shell no harness runs, which is the owner, or for a relayed
// close the chief by names on another host. A session counts only when it runs a chief.
func (c *Core) CloseAuthor(session, by string) (string, error) {
	if session == "" {
		if _, _, remote := SplitRemote(by); remote {
			return by, nil
		}
		return Owner, nil
	}
	o, err := c.LoadOrg()
	if err != nil {
		return "", err
	}
	session = c.Resolve(session)
	m, ok := o.ManagerOf(session)
	if !ok {
		return "", Final(fmt.Errorf("session %s is not a chief; only a chief above a goal or the owner closes it", Short(session)))
	}
	if m.Role != RoleChief {
		return "", Final(fmt.Errorf("%s is a manager; a manager finishes its goal with ym goal done, and only a chief above a goal or the owner closes it", m.Shown()))
	}
	return m.ID, nil
}

// CloseGoal ends the goal order.Ref names with a recorded reason: its manager, agents and verifier end as in a kill, with their
// work kept for ym revive; its open items are dismissed; a live goal becomes dropped, and a done or killed one keeps its state and
// is acked. A chief closes only goals below it, at any depth.
func (c *Core) CloseGoal(b Board, order CloseOrder) (Closed, error) {
	order.Why = oneLine(order.Why)
	if !slices.Contains([]string{CloseFinished, CloseSuperseded, CloseAbandoned}, order.As) {
		return Closed{}, Final(fmt.Errorf("--as takes %s, %s or %s, not %q", CloseFinished, CloseSuperseded, CloseAbandoned, order.As))
	}
	if order.Why == "" {
		return Closed{}, Final(errors.New("a close needs --why"))
	}
	if order.Override && order.As != CloseFinished {
		return Closed{}, Final(fmt.Errorf("--override-verifier goes only with --as %s", CloseFinished))
	}
	if host, rest, ok := c.remoteHost(order.Ref); ok {
		args := []string{"goal", "close", rest, "--as", order.As, "--why", order.Why}
		if order.Override {
			args = append(args, "--override-verifier")
		}
		if order.Author != Owner {
			args = append(args, "--by", RemoteKey(c.SelfName(), order.Author))
		}
		y := relayYM{Args: args}
		if order.Author != Owner {
			y.ByName = c.RefName(order.Author)
		}
		out, err := c.runOrRelay(host, y)
		if err != nil {
			return Closed{Goal: order.Ref}, fmt.Errorf("%s: %w", c.RefName(order.Ref), err)
		}
		return Closed{Goal: order.Ref, Output: out}, nil
	}
	o, err := c.LoadOrg()
	if err != nil {
		return Closed{}, err
	}
	m, err := o.lookup(order.Ref)
	if err != nil {
		return Closed{}, Final(err)
	}
	g, isGoal := o.Goals[m.GoalID]
	if !isGoal {
		return Closed{}, Final(fmt.Errorf("%s is a chief, not a goal; ym kill retires a chief", m.Shown()))
	}
	var via string
	if order.Author != Owner {
		var reports bool
		if reports, via = climb(o, order.Author, m.ID); !reports && via == "" {
			return Closed{}, Final(fmt.Errorf("%s does not report to %s; a chief closes only goals below it", g.Shown(), c.actorName(order.Author, order.AuthorName)))
		}
	}
	if err := c.closable(g); err != nil {
		return Closed{}, err
	}
	if _, err := c.closeDisposition(g, order); err != nil {
		return Closed{}, err
	}
	if via != "" {
		host, rest, _ := SplitRemote(via)
		close := order
		v := relayVouch{Chief: rest, Author: c.fromHere(order.Author), AuthorName: cmp.Or(order.AuthorName, c.RefName(order.Author)), Target: RemoteKey(c.SelfName(), m.ID), Close: &close}
		return Closed{Goal: g.Shown(), Pending: via}, c.Relay(host, RelayVouchClose, v)
	}
	return c.applyClose(b, m, g, order)
}

// closable refuses a goal that ended some other way than a close and cannot take one: one dropped because it moved host.
func (c *Core) closable(g Goal) error {
	if g.State == GoalDropped && g.Closure == nil {
		return Final(fmt.Errorf("%s was dropped without a close: %s", g.Shown(), g.Evidence))
	}
	return nil
}

// closeDisposition is what order records on g. A finish needs a pass at the goal's current work revision, or the override.
func (c *Core) closeDisposition(g Goal, order CloseOrder) (string, error) {
	if order.As != CloseFinished || g.State == GoalDone {
		return order.As, nil
	}
	vs, err := c.Store.Verifications(g.ID)
	if err != nil {
		return "", err
	}
	if slices.ContainsFunc(vs, func(v Verification) bool { return v.Verdict == VerdictPass && v.WorkRevision == g.WorkRevision }) {
		return CloseFinished, nil
	}
	if order.Override {
		return CloseFinishedOverride, nil
	}
	return "", Final(fmt.Errorf("%s has no verifier pass at work revision %d; --override-verifier records it as %s and writes no pass", g.Shown(), g.WorkRevision, CloseFinishedOverride))
}

// applyClose records order on g, unless a close already did and this one only finishes its teardown, then ends g's sessions.
func (c *Core) applyClose(b Board, m Manager, g Goal, order CloseOrder) (Closed, error) {
	if err := c.closable(g); err != nil {
		return Closed{}, err
	}
	disposition, err := c.closeDisposition(g, order)
	if err != nil {
		return Closed{}, err
	}
	_, targets, err := c.KillTargets(b, m.ID)
	if err != nil {
		return Closed{}, err
	}
	already := g.Ended() && g.Closure != nil
	if already && len(targets) == 0 {
		return Closed{}, Final(fmt.Errorf("%s is closed already, as %s by %s", g.Shown(), g.Closure.Disposition, g.Closure.Actor))
	}
	out := Closed{Goal: g.Shown(), Disposition: disposition, State: g.State, Toll: c.Weigh(targets)}
	if already {
		out.Disposition, out.PRs = g.Closure.Disposition, g.Closure.PRs
	} else {
		if out.PRs, err = c.openPRs(g); err != nil {
			return out, err
		}
		now := c.now()
		if _, _, err := c.Store.AddGoalEvent(GoalEvent{Goal: g.ID, Kind: EventGoalClosed, Payload: closureText(disposition, c.actorName(order.Author, order.AuthorName), order.Why, out.PRs), Key: closureKey(order.Author, now, int64(g.Version)), Created: now, Delivered: now}); err != nil {
			return out, err
		}
		if !g.Ended() {
			g.State, g.DoneAt, g.Evidence = GoalDropped, now, ""
		}
		g.AckedAt, g.LastMovedAt = now, now
		if err := c.Store.SaveGoal(g); err != nil {
			return out, err
		}
		out.State = g.State
		reason := fmt.Sprintf("%s closed as %s: %s", g.Shown(), disposition, order.Why)
		if out.Items, err = c.dismissGoalItems(g.ID, reason); err != nil {
			return out, err
		}
		if out.Elsewhere, err = c.dismissItemsElsewhere(g, reason); err != nil {
			return out, err
		}
	}
	stayed, err := c.teardown(m, g.ID, targets)
	out.Stayed, out.Toll.Worktrees = stayed, max(out.Toll.Worktrees-len(stayed), 0)
	if err != nil {
		return out, fmt.Errorf("%s is closed as %s, but its sessions did not all end; run the close again to finish: %w", g.Shown(), out.Disposition, err)
	}
	return out, nil
}

// openPRs lists the pull requests g owns that have not merged or closed.
func (c *Core) openPRs(g Goal) ([]string, error) {
	owned, err := c.Store.GoalPRs()
	if err != nil {
		return nil, err
	}
	statuses, err := c.Store.PRStatuses()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, url := range owned[g.ID] {
		if st := statuses[url]; st.State != "MERGED" && st.State != "CLOSED" && st.State != PRGone && !slices.Contains(out, url) {
			out = append(out, url)
		}
	}
	return out, nil
}

// dismissGoalItems dismisses what owner's sessions still ask here, which would otherwise climb to the owner once its manager ends.
func (c *Core) dismissGoalItems(owner, reason string) (int, error) {
	items, err := c.Store.Items(ItemFilter{States: []string{ItemOpen, ItemSnoozed}})
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, it := range items {
		if it.GoalOwner != owner {
			continue
		}
		if _, err := c.Store.Transition(it.Key(), ItemDismissed, reason); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// dismissItemsElsewhere has each host g's agents ran on dismiss their asks, which live on the host that filed them.
func (c *Core) dismissItemsElsewhere(g Goal, reason string) ([]string, error) {
	assignments, err := c.Store.Assignments(g.ID)
	if err != nil {
		return nil, err
	}
	var hosts []string
	for _, a := range assignments {
		host, _, remote := SplitRemote(a.Session)
		if !remote || slices.Contains(hosts, host) {
			continue
		}
		hosts = append(hosts, host)
		if err := c.queueWork(host, WorkMessage{Op: "goal-closed", Work: RemoteAssignment{Origin: c.SelfName(), Goal: g.ID}, Summary: reason}); err != nil {
			return hosts, err
		}
	}
	return hosts, nil
}

// closeVouched lands a close whose author another host confirmed, telling that author when it cannot.
func (c *Core) closeVouched(b Board, o Org, v relayVouch) error {
	_, id, _ := SplitRemote(v.Target)
	m, err := o.lookup(id)
	if err != nil {
		return fmt.Errorf("%w: %w", errUnusable, err)
	}
	g, isGoal := o.Goals[m.GoalID]
	if !isGoal || v.Close == nil {
		return fmt.Errorf("%w: %s is not a goal to close", errUnusable, id)
	}
	order := *v.Close
	order.Author, order.AuthorName = c.hereFrom(v.Author), v.AuthorName
	if _, err := c.applyClose(b, m, g, order); err != nil {
		if errors.Is(err, ErrFinal) {
			return c.tellManager(b, o, order.Author, fmt.Sprintf("ym: your close of %s did not land: %s", RemoteKey(c.SelfName(), g.Shown()), err))
		}
		return err
	}
	return nil
}

// ClosedLine prints g's closure for ym goal show, "" when it has none.
func ClosedLine(g Goal) string {
	cl := g.Closure
	if cl == nil {
		return ""
	}
	line := fmt.Sprintf("closed as %s by %s at %s: %s", cl.Disposition, cl.Actor, cl.At.Local().Format("2006-01-02 15:04:05"), cl.Reason)
	if len(cl.PRs) > 0 {
		line += "\n  still open at close: " + strings.Join(cl.PRs, ", ")
	}
	return line
}
