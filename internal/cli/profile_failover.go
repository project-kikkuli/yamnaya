package cli

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

var errNoUsablePool = errors.New("no different verified usable subscription pool")

type failoverAttempt struct {
	State core.FailoverState   `json:"state"`
	Pools map[string]time.Time `json:"attempted_pools"`
	Event string               `json:"event"`
	// Failed holds back, until then, a login a switch failed to land on, so the next usable one is tried meanwhile.
	Failed map[string]time.Time `json:"failed,omitempty"`
}

// untried is the logins this attempt has not just failed to land on.
func (a failoverAttempt) untried(ps []profile.Profile, now time.Time) []profile.Profile {
	return slices.DeleteFunc(slices.Clone(ps), func(p profile.Profile) bool { return a.Failed[p.Account].After(now) })
}

// failed holds back the login a switch could not land on.
func (a *failoverAttempt) failed(account string, until time.Time) {
	if a.Failed == nil {
		a.Failed = map[string]time.Time{}
	}
	a.Failed[account] = until
}

func failoverPath(dir, harness, session string) string {
	return filepath.Join(dir, "failovers", harness+"-"+session+".json")
}
func loadFailover(dir, harness, session string) (failoverAttempt, error) {
	a := failoverAttempt{Pools: map[string]time.Time{}}
	b, err := os.ReadFile(failoverPath(dir, harness, session))
	if os.IsNotExist(err) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	err = json.Unmarshal(b, &a)
	if a.Pools == nil {
		a.Pools = map[string]time.Time{}
	}
	return a, err
}
func saveFailover(dir, h string, a failoverAttempt) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return profile.Write(failoverPath(dir, h, a.State.Session), b)
}

// loginRow reports whether w is a row about the whole login rather than one model.
func loginRow(w core.Limit, harness, id, account string) bool {
	if w.Harness != harness || harness == "codex" && !strings.HasPrefix(w.Name, "codex ") || harness == "claude" && w.Name != "5h" && w.Name != "7d" {
		return false
	}
	if w.Account == "" {
		return profile.ID(w.Profile) == profile.ID(id)
	}
	return w.Account == account
}

// loginWindow reports whether w, a window that stops the whole login, is used up.
func loginWindow(w core.Limit, harness, id, account string, now time.Time) bool {
	return w.Used >= 100 && w.ResetsAt.After(now) && loginRow(w, harness, id, account)
}

// exhaustedUntil is the reset of the login's latest used-up window, zero while it has usage left.
func exhaustedUntil(limits []core.Limit, harness, id, account string, now time.Time) time.Time {
	var reset time.Time
	for _, w := range limits {
		if loginWindow(w, harness, id, account, now) && w.ResetsAt.After(reset) {
			reset = w.ResetsAt
		}
	}
	return reset
}

// showsUsable reports whether the login's own rows show usage left, which lifts a bar recorded when it ran out: it reset early.
func showsUsable(limits []core.Limit, p profile.Profile, now time.Time) bool {
	seen := false
	for _, w := range limits {
		if w.ResetsAt.After(now) && loginRow(w, p.Harness, p.ID, p.Account) {
			if w.Used >= 100 {
				return false
			}
			seen = true
		}
	}
	return seen
}

func trueExhaustion(e core.Exhaustion, windows []core.Limit, now time.Time) (time.Time, bool) {
	if e.Recovered || e.Account == "" || e.Seen.IsZero() {
		return time.Time{}, false
	}
	if e.Harness != "codex" && e.Harness != "claude" || e.Reason != quotaReason(e.Harness) {
		return time.Time{}, false
	}
	var reset time.Time
	for _, w := range windows {
		if w.Account == e.Account && loginWindow(w, e.Harness, e.Profile, e.Account, now) && w.ResetsAt.After(reset) {
			reset = w.ResetsAt
		}
	}
	return reset, !reset.IsZero()
}
func quotaReason(harness string) string {
	if harness == "codex" {
		return "usage_limit_exceeded"
	}
	return "subscription_rate_limit"
}

// silentStops are the sessions held at the quota dialog with no event to show for it, as they stop on starting on a used-up login without
// running a turn. That is the session's own quota stop like any other.
func (l launcher) silentStops(b core.Board) []core.Exhaustion {
	if l.core == nil {
		return nil
	}
	var out []core.Exhaustion
	for _, s := range b.Sessions {
		if s.Remote != "" || s.ID == "" || s.Account == "" || s.Status != core.Waiting || s.Prompt != nil {
			continue
		}
		at, stopped, err := l.core.SilentQuotaStop(s.Session)
		if err != nil || !stopped {
			continue
		}
		out = append(out, core.Exhaustion{Harness: s.Harness, Profile: s.Profile, Account: s.Account, Session: s.ID, Reason: quotaReason(s.Harness), Event: fmt.Sprintf("dialog-%s-%d", s.ID, at.Unix()), Seen: at})
	}
	return out
}

// verifiedStop reports a quota stop proved by the session itself, sat at the quota dialog on the turn the event names, however its limit
// rows read: they lag, and a row at 99% does not un-stop it. The reset is unknown until a row shows it.
func (l launcher) verifiedStop(e core.Exhaustion, b core.Board) (time.Time, bool) {
	if l.core == nil || e.Recovered || e.Account == "" || e.Seen.IsZero() || e.Reason != quotaReason(e.Harness) {
		return time.Time{}, false
	}
	s, alive := b.Session(e.Session)
	if !alive || s.Remote != "" || s.Status != core.Waiting || s.Prompt != nil || s.Account != e.Account {
		return time.Time{}, false
	}
	at, err := l.core.InspectHandoff(s.Session)
	if err != nil || e.Seen.Before(at) {
		return time.Time{}, false
	}
	return exhaustedUntil(b.Fleet.Limits, e.Harness, e.Profile, e.Account, b.At), true
}

// noteLimitRows files the owner's item for each login a live session runs on that a limit row shows used up, though no turn has stopped
// on it yet; such a session stays put until it does. A turn that recovered after the row was read shows the row is stale.
func (l launcher) noteLimitRows(dir string, ps []profile.Profile, b core.Board) error {
	var errs []error
	for _, s := range b.Sessions {
		if s.Remote != "" || s.ID == "" || s.Account == "" {
			continue
		}
		reset := exhaustedUntil(b.Fleet.Limits, s.Harness, s.Profile, s.Account, b.At)
		if reset.IsZero() {
			continue
		}
		var seen time.Time
		for _, w := range b.Fleet.Limits {
			if loginWindow(w, s.Harness, s.Profile, s.Account, b.At) && w.Seen.After(seen) {
				seen = w.Seen
			}
		}
		if slices.ContainsFunc(b.Fleet.Exhaustions, func(e core.Exhaustion) bool {
			return e.Session == s.ID && e.Account == s.Account && e.Recovered && e.Seen.After(seen)
		}) {
			continue
		}
		e := core.Exhaustion{Harness: s.Harness, Profile: s.Profile, Account: s.Account, Session: s.ID}
		errs = append(errs, l.fileQuotaItem(dir, ps, e, reset, b.Fleet.Limits, b.At))
	}
	return errors.Join(errs...)
}
func failoverTarget(ps []profile.Profile, e core.Exhaustion, limits []core.Limit, pools map[string]time.Time, now time.Time, cwds ...string) (profile.Profile, error) {
	cwd := ""
	if len(cwds) > 0 {
		cwd = cwds[0]
	}
	for _, p := range ps {
		if p.Harness != e.Harness || p.Account == "" || p.Account == e.Account || !exhaustedUntil(limits, p.Harness, p.ID, p.Account, now).IsZero() {
			continue
		}
		if pools[p.Account].After(now) && !showsUsable(limits, p, now) {
			continue
		}
		if p.SubscriptionSafe(cwd) {
			return p, nil
		}
	}
	return profile.Profile{}, errNoUsablePool
}

// failoverDestination is a usable login of e's harness, which keeps the conversation's id, else of the other harness as the last resort.
func failoverDestination(ps []profile.Profile, e core.Exhaustion, limits []core.Limit, pools map[string]time.Time, now time.Time, cwd string) (profile.Profile, error) {
	target, err := failoverTarget(ps, e, limits, pools, now, cwd)
	if err == nil {
		return target, nil
	}
	other, otherErr := failoverTarget(ps, core.Exhaustion{Harness: core.Other(e.Harness), Account: e.Account}, limits, pools, now, cwd)
	if otherErr != nil {
		return profile.Profile{}, err
	}
	return other, nil
}
func (l launcher) Failover(b core.Board) error {
	dir := filepath.Dir(l.records.Dir)
	ps, err := profile.Load(dir)
	if err != nil {
		return err
	}
	if err := l.settleBlocked(dir); err != nil {
		return err
	}
	if err := l.closeQuotaItems(dir, b); err != nil {
		return err
	}
	killed := map[string]bool{}
	if l.core != nil {
		kills, err := l.core.Store.Kills(time.Time{})
		if err != nil {
			return err
		}
		for session := range kills {
			killed[session] = true
		}
	}
	notices := []error{l.noteLimitRows(dir, ps, b)}
	silent := l.silentStops(b)
	for _, e := range append(slices.Clone(b.Fleet.Exhaustions), silent...) {
		reset, actual := trueExhaustion(e, b.Fleet.Limits, b.At)
		if !actual {
			reset, actual = l.verifiedStop(e, b)
		}
		if !actual && slices.ContainsFunc(silent, func(s core.Exhaustion) bool { return s.Event == e.Event }) {
			reset, actual = exhaustedUntil(b.Fleet.Limits, e.Harness, e.Profile, e.Account, b.At), true
		}
		if !actual {
			continue
		}
		notices = append(notices, l.fileQuotaItem(dir, ps, e, cmp.Or(exhaustedUntil(b.Fleet.Limits, e.Harness, e.Profile, e.Account, b.At), reset), b.Fleet.Limits, b.At))
		s, alive := b.Session(e.Session)
		if !alive || s.Remote != "" {
			continue
		}
		rec, ok := l.records.Find(e.Session)
		if !ok || profile.ID(rec.Profile) != profile.ID(e.Profile) || rec.IsLanded() || !rec.Killed.IsZero() || killed[e.Session] {
			continue
		}
		if l.core != nil && (l.core.Off(e.Session) || b.ThrottleHolds(e.Session)) {
			continue
		}
		selected, err := profile.Find(ps, e.Harness, e.Profile)
		if err != nil || selected.Account != e.Account || !selected.SubscriptionSafe(rec.Worktree.Path) {
			continue
		}
		a, err := loadFailover(dir, e.Harness, e.Session)
		if err != nil {
			return err
		}
		pending, err := loadMigration(dir, e.Harness, e.Session)
		if err != nil {
			return err
		}
		if pending.Version != 0 && pending.State != "running" {
			continue
		}
		target, chooseErr := failoverDestination(a.untried(ps, b.At), e, b.Fleet.Limits, a.Pools, b.At, rec.Worktree.Path)
		if a.Event == e.Event && (a.State.Status == "switched" || a.State.Status == "closed" || a.State.Status == "blocked" && chooseErr != nil) {
			continue
		}
		if err := switchIdentity(s, rec); err != nil {
			continue
		}
		a.Event = e.Event
		a.Pools[e.Account] = reset
		a.State = core.FailoverState{Session: e.Session, Profile: e.Profile, Account: e.Account, Status: "switching", Reason: e.Reason, Reset: reset}
		if chooseErr != nil {
			a.State.Status = "blocked"
			a.State.Reason = chooseErr.Error()
			if err = saveFailover(dir, e.Harness, a); err != nil {
				return err
			}
			continue
		}
		if l.core != nil && !l.core.CanResume() {
			continue
		}
		a.State.Target = target.ID
		if err = saveFailover(dir, e.Harness, a); err != nil {
			return err
		}
		until := quotaReset(reset)
		note := fmt.Sprintf("The prior turn stopped because the %s subscription exhausted its usage window, which resets %s. ym moved this same conversation from profile %s to %s profile %s.", e.Harness, until, e.Profile, target.Harness, target.ID)
		if target.Harness == e.Harness {
			note += " Inspect recorded tool results and filesystem effects before continuing the unfinished work. Do not blindly replay the previous prompt or repeat a tool action whose effects are already recorded."
		} else {
			note += " Inspect filesystem effects before continuing the unfinished work, and do not blindly replay the previous prompt."
		}
		out, switchErr := l.failoverSwitch(rec, target, note, fmt.Sprintf("the %s login %s is out of its subscription limit until %s", e.Harness, profile.ID(e.Profile), until))
		switch {
		case errors.Is(switchErr, profile.ErrOperationBusy):
			a.State.Status = "blocked"
			a.State.Target = ""
			a.State.Reason = switchErr.Error()
		case switchErr != nil:
			a.State.Status = "blocked"
			a.State.Reason = switchErr.Error()
			a.failed(target.Account, b.At.Add(core.QuotaRecheck))
		case out.Queued:
			a.State.Status = "queued"
			a.State.Reason = out.Message
		default:
			a.State.Status = "switched"
			a.State.Profile = target.ID
			a.State.Account = target.Account
			if l.core != nil {
				l.core.ResumedNow()
			}
		}
		if err = saveFailover(dir, e.Harness, a); err != nil {
			return err
		}
	}
	return errors.Join(notices...)
}

// failoverSwitch is the one place ym moves a conversation off an exhausted login by itself, with note as its next message. A refused
// apply that left the switch queued is still queued: ym serve retries it at the next turn end.
func (l launcher) failoverSwitch(rec launch.Record, target profile.Profile, note, reason string) (core.SwitchOutcome, error) {
	next := l
	next.resumeNote = note
	next.toHarness = target.Harness
	out, err := next.QueueProfileSwitch(rec.Session, target.ID, reason)
	if err != nil {
		m, loadErr := loadMigration(filepath.Dir(l.records.Dir), rec.Harness, rec.Session)
		if loadErr == nil && m.State == "queued" && m.Destination.ID == target.ID {
			return core.SwitchOutcome{Queued: true, Message: err.Error()}, nil
		}
	}
	return out, err
}

// settleBlocked settles a blocked record whose session no longer waits on it: on another login it has moved, and landed or killed it
// has ended. One with a switch still pending is left for that switch to settle.
func (l launcher) settleBlocked(dir string) error {
	files, _ := filepath.Glob(filepath.Join(dir, "failovers", "*.json"))
	for _, path := range files {
		harness, _, _ := strings.Cut(filepath.Base(path), "-")
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var a failoverAttempt
		if err = json.Unmarshal(raw, &a); err != nil {
			return err
		}
		if a.State.Status != "blocked" {
			continue
		}
		rec, ok := l.records.Find(a.State.Session)
		moved := ok && profile.ID(rec.Profile) != profile.ID(a.State.Profile)
		if !moved && (!ok || !rec.IsLanded() && rec.Killed.IsZero()) {
			continue
		}
		m, err := loadMigration(dir, harness, a.State.Session)
		if err != nil {
			return err
		}
		if m.Version != 0 && m.State != "running" {
			continue
		}
		if moved {
			a.State.Status, a.State.Profile, a.State.Account, a.State.Reason = "switched", profile.ID(rec.Profile), rec.Account, "switched to "+profile.ID(rec.Profile)
		} else {
			a.State.Status, a.State.Reason = "closed", "the session ended"
		}
		if err = saveFailover(dir, harness, a); err != nil {
			return err
		}
	}
	return nil
}

// failoverPending reports whether a is a failover still waiting on its chosen login's switch.
func failoverPending(a failoverAttempt) bool {
	if a.State.Status == "queued" {
		return true
	}
	if a.State.Status != "blocked" {
		return false
	}
	return a.State.Target != ""
}

// settleFailover records that the failover switch of session has now applied.
func settleFailover(dir, harness, session string, dest profile.Profile) error {
	a, err := loadFailover(dir, harness, session)
	if err != nil || !failoverPending(a) {
		return err
	}
	a.State.Status, a.State.Profile, a.State.Account, a.State.Reason = "switched", dest.ID, dest.Account, "switched to "+dest.ID
	return saveFailover(dir, harness, a)
}

// retargetFailover names the login a replaced queued switch now moves to on the failover waiting on it.
func retargetFailover(dir, harness, session string, prior migration, dest profile.Profile) error {
	if prior.State != "queued" || prior.Destination.ID == dest.ID {
		return nil
	}
	a, err := loadFailover(dir, harness, session)
	if err != nil || !failoverPending(a) {
		return err
	}
	a.State.Target, a.State.Status, a.State.Reason = dest.ID, "queued", "switch to "+dest.ID+" waits for the turn end"
	return saveFailover(dir, harness, a)
}

// closeFailover records that the failover switch of session will not apply, and why.
func closeFailover(dir, harness, session, why string) error {
	a, err := loadFailover(dir, harness, session)
	if err != nil || !failoverPending(a) {
		return err
	}
	a.State.Status, a.State.Reason = "closed", why
	return saveFailover(dir, harness, a)
}
func failoverStates(dir string) []core.FailoverState {
	files, _ := filepath.Glob(filepath.Join(dir, "failovers", "*.json"))
	var out []core.FailoverState
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var a failoverAttempt
		if json.Unmarshal(b, &a) == nil {
			out = append(out, a.State)
		}
	}
	return out
}
