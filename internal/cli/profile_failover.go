package cli

import (
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

// failoverAttempt is what is about one conversation: how its failover stands and logins a switch just failed to land it on. Whether a login
// can be used is the login's own record.
type failoverAttempt struct {
	State core.FailoverState `json:"state"`
	Event string             `json:"event"`
	// Recorded is the last stop of this conversation's already told to its login's record.
	Recorded string `json:"recorded,omitempty"`
	// Failed holds back, until then, a login a switch failed to land on, so the next usable one is tried meanwhile.
	Failed map[string]time.Time `json:"failed,omitempty"`
}

// failed holds back the login a switch could not land on.
func (a *failoverAttempt) failed(account string, until time.Time) {
	if a.Failed == nil {
		a.Failed = map[string]time.Time{}
	}
	a.Failed[account] = until
}

// usableLogins are the logins this conversation can go to now.
func usableLogins(dir string, a failoverAttempt, ps []profile.Profile, limits []core.Limit, now time.Time) []profile.Profile {
	return slices.DeleteFunc(slices.Clone(ps), func(p profile.Profile) bool {
		return a.Failed[p.Account].After(now) || !barredUntil(dir, p, limits, now).IsZero()
	})
}

func failoverPath(dir, harness, session string) string {
	return filepath.Join(dir, "failovers", harness+"-"+session+".json")
}
func loadFailover(dir, harness, session string) (failoverAttempt, error) {
	var a failoverAttempt
	b, err := os.ReadFile(failoverPath(dir, harness, session))
	if os.IsNotExist(err) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	err = json.Unmarshal(b, &a)
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

// usageEvidence is how far below the limit a row must read to show a login has usage: one reading 99% says nothing about a login that
// just stopped a session.
const usageEvidence = 90

// showsUsage reports every current window of the login, as last written after since, reading well below the limit, which is what later
// evidence of usage looks like. One window reading low says nothing about another that stopped it.
func showsUsage(limits []core.Limit, p profile.Profile, since, now time.Time) bool {
	seen := false
	for _, w := range limits {
		if !w.ResetsAt.After(now) || !loginRow(w, p.Harness, p.ID, p.Account) {
			continue
		}
		if w.Used >= usageEvidence || !w.Seen.After(since) {
			return false
		}
		seen = true
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

// selfContinueGrace is how long past its reset ym leaves a session whose harness carries on by itself.
const selfContinueGrace = 5 * time.Minute

// silentStop is a session only its screen shows held on its limit.
type silentStop struct {
	core.Exhaustion
	core.QuotaStop
}

// silentStops are the sessions whose screen shows them held on their limit, whatever else says so: the dialog, which starting on a used-up
// login shows without running a turn, and the footer an idle session shows. Either is the session's own quota stop like any other. One a
// session lands on is a new stop on that login, however long ago its last turn ran, so it is known by the session, the login and the
// landing. unproven are those whose stop cannot be proved over.
func (l launcher) silentStops(b core.Board) (stops []silentStop, unproven []core.Exhaustion) {
	if l.core == nil {
		return nil, nil
	}
	for _, s := range b.Sessions {
		if s.Remote != "" || s.ID == "" || s.Account == "" || s.Status != core.Waiting && s.Status != core.Idle || s.Prompt != nil {
			continue
		}
		stop, stopped, err := l.core.SilentQuotaStop(s.Session)
		if !stopped {
			continue
		}
		landed := stop.At
		if rec, ok := l.records.Find(s.ID); ok && rec.AttachedAt.After(landed) {
			landed = rec.AttachedAt
		}
		e := core.Exhaustion{Harness: s.Harness, Profile: s.Profile, Account: s.Account, Session: s.ID, Reason: quotaReason(s.Harness), Event: fmt.Sprintf("screen-%s-%s-%d", s.ID, s.Account, landed.Unix()), Seen: landed}
		if err != nil {
			e.Reason = err.Error()
			unproven = append(unproven, e)
			continue
		}
		stops = append(stops, silentStop{e, stop})
	}
	return stops, unproven
}

// showUnproven gives a session whose stop cannot be proved over its login's item and its reason, as nothing can move it yet.
func (l launcher) showUnproven(dir string, ps []profile.Profile, e core.Exhaustion, at time.Time) error {
	why := e.Reason
	selected, err := profile.Find(ps, e.Harness, e.Profile)
	if err != nil {
		return err
	}
	a, err := loadFailover(dir, e.Harness, e.Session)
	if err != nil {
		return err
	}
	if a.Recorded != e.Event {
		if err = recordStop(dir, selected, at, time.Time{}); err != nil {
			return err
		}
		a.Recorded = e.Event
	}
	if a.State.Reason != why || a.State.Status != "blocked" {
		a.State = core.FailoverState{Session: e.Session, Profile: e.Profile, Account: e.Account, Status: "blocked", Reason: why}
	}
	return saveFailover(dir, e.Harness, a)
}

// continueHere brings back a session still showing its stop on a login that is usable again. Nothing presses keys in a dialog: stopping
// the session and resuming it on the same login is how every switch continues a conversation, and it reads the same for each harness. If
// the stop returns, that is a new stop on the login, which bars it again.
func (l launcher) continueHere(dir string, rec launch.Record, s core.Live, a failoverAttempt, e core.Exhaustion, now time.Time) error {
	if l.core == nil || !l.core.CanResume() {
		return nil
	}
	a.Event = e.Event
	a.State = core.FailoverState{Session: e.Session, Profile: e.Profile, Account: e.Account, Status: "continued", Reason: "the limit on this login has passed"}
	once := l
	once.operation = rec.Session
	once.resumeNote = fmt.Sprintf("ym: the subscription limit on login %s has passed, so ym resumed this conversation on it. Carry on with any unfinished work.", profile.ID(e.Profile))
	_, err := l.core.Stop(s)
	if err == nil {
		_, err = once.revive(rec)
	}
	if err != nil {
		a.State.Status, a.State.Reason = "blocked", "could not continue after the limit passed: "+err.Error()
		a.failed(e.Account, now.Add(core.QuotaRecheck))
	} else {
		l.core.ResumedNow()
	}
	return saveFailover(dir, e.Harness, a)
}

// verifiedStop reports a quota stop proved by the session itself, sat at the quota dialog on the turn the event names, however its limit
// rows read: they lag, and a row at 99% does not un-stop it. The reset is what its rows show, if anything. stuck is a stop the dialog shows
// but ym cannot prove over, such as a child still busy.
func (l launcher) verifiedStop(e core.Exhaustion, b core.Board) (reset time.Time, ok bool, stuck error) {
	if l.core == nil || e.Recovered || e.Account == "" || e.Seen.IsZero() || e.Reason != quotaReason(e.Harness) {
		return time.Time{}, false, nil
	}
	s, alive := b.Session(e.Session)
	if !alive || s.Remote != "" || s.Status != core.Waiting || s.Prompt != nil || s.Account != e.Account {
		return time.Time{}, false, nil
	}
	at, err := l.core.InspectHandoff(s.Session)
	if err != nil {
		var unproven *core.QuotaStopUnproven
		if errors.As(err, &unproven) {
			return time.Time{}, false, err
		}
		return time.Time{}, false, nil
	}
	if e.Seen.Before(at) {
		return time.Time{}, false, nil
	}
	return exhaustedUntil(b.Fleet.Limits, e.Harness, e.Profile, e.Account, b.At), true, nil
}

func failoverTarget(ps []profile.Profile, e core.Exhaustion, limits []core.Limit, now time.Time, cwds ...string) (profile.Profile, error) {
	cwd := ""
	if len(cwds) > 0 {
		cwd = cwds[0]
	}
	for _, p := range ps {
		if p.Harness != e.Harness || p.Account == "" || p.Account == e.Account || !exhaustedUntil(limits, p.Harness, p.ID, p.Account, now).IsZero() {
			continue
		}
		if p.SubscriptionSafe(cwd) {
			return p, nil
		}
	}
	return profile.Profile{}, errNoUsablePool
}

// failoverDestination is a usable login of e's harness, which keeps the conversation's id, else of the other harness as the last resort.
func failoverDestination(ps []profile.Profile, e core.Exhaustion, limits []core.Limit, now time.Time, cwd string) (profile.Profile, error) {
	target, err := failoverTarget(ps, e, limits, now, cwd)
	if err == nil {
		return target, nil
	}
	other, otherErr := failoverTarget(ps, core.Exhaustion{Harness: core.Other(e.Harness), Account: e.Account}, limits, now, cwd)
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
	all, err := l.records.All()
	if err != nil {
		return err
	}
	hung := map[string]bool{}
	silent, unproven := l.silentStops(b)
	var notices []error
	for _, stop := range unproven {
		notices = append(notices, l.showUnproven(dir, ps, stop, b.At))
	}
	events := make([]core.Exhaustion, 0, len(b.Fleet.Exhaustions)+len(silent))
	for _, e := range b.Fleet.Exhaustions {
		if !slices.ContainsFunc(silent, func(s silentStop) bool { return s.Exhaustion.Session == e.Session && s.Exhaustion.Account == e.Account }) {
			events = append(events, e)
		}
	}
	for _, s := range silent {
		events = append(events, s.Exhaustion)
	}
	for _, e := range events {
		screen := slices.IndexFunc(silent, func(s silentStop) bool { return s.Exhaustion.Event == e.Event })
		reset, actual, dialog, selfContinues := time.Time{}, false, false, false
		at := e.Seen
		if screen >= 0 {
			reset, actual, dialog, selfContinues = silent[screen].Reset, true, true, silent[screen].SelfContinues
			at = b.At
		} else if reset, actual = trueExhaustion(e, b.Fleet.Limits, b.At); !actual {
			var stuck error
			reset, actual, stuck = l.verifiedStop(e, b)
			dialog = actual
			if stuck != nil {
				e.Reason = stuck.Error()
				notices = append(notices, l.showUnproven(dir, ps, e, b.At))
			}
		}
		if !actual {
			continue
		}
		selected, selectedErr := profile.Find(ps, e.Harness, e.Profile)
		if selectedErr != nil || selected.Account != e.Account {
			continue
		}
		a, err := loadFailover(dir, e.Harness, e.Session)
		if err != nil {
			return err
		}
		if a.Recorded != e.Event {
			if err = recordStop(dir, selected, at, reset); err != nil {
				return err
			}
			a.Recorded = e.Event
			a.State.Session = e.Session
			if err = saveFailover(dir, e.Harness, a); err != nil {
				return err
			}
		}
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
		if !selected.SubscriptionSafe(rec.Worktree.Path) {
			continue
		}
		pending, err := loadMigration(dir, e.Harness, e.Session)
		if err != nil {
			return err
		}
		if pending.Version != 0 && pending.State != "running" {
			continue
		}
		if err := switchIdentity(s, rec); err != nil {
			continue
		}
		until := barredUntil(dir, selected, b.Fleet.Limits, b.At)
		if selfContinues {
			// The footer outranks what rows say. Past its bar Claude carries on by itself, so ym only steps in if it plainly has not.
			record, err := readBar(barPath(dir, selected))
			if err != nil {
				return err
			}
			until = record.Until
			if !until.After(b.At) && b.At.Sub(until) < selfContinueGrace {
				continue
			}
			if !until.After(b.At) {
				until = time.Time{}
			}
		}
		if until.IsZero() || !until.After(b.At) {
			if dialog && !a.Failed[e.Account].After(b.At) {
				if err = l.continueHere(dir, rec, s, a, e, b.At); err != nil {
					return err
				}
			}
			continue
		}
		target, chooseErr := failoverDestination(usableLogins(dir, a, ps, b.Fleet.Limits, b.At), e, b.Fleet.Limits, b.At, rec.Worktree.Path)
		if a.Event == e.Event && (a.State.Status == "switched" || a.State.Status == "closed" || a.State.Status == "blocked" && chooseErr != nil) {
			continue
		}
		a.Event = e.Event
		a.State = core.FailoverState{Session: e.Session, Profile: e.Profile, Account: e.Account, Status: "switching", Reason: e.Reason, Reset: until}
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
		if authHangs(target, hung) {
			a.State.Status = "blocked"
			a.State.Reason = fmt.Sprintf("the status check of login %s has not answered, so ym tries it again shortly", target.ID)
			a.failed(target.Account, b.At.Add(core.QuotaRecheck))
			if err = saveFailover(dir, e.Harness, a); err != nil {
				return err
			}
			continue
		}
		a.State.Target = target.ID
		if err = saveFailover(dir, e.Harness, a); err != nil {
			return err
		}
		when := quotaReset(until)
		note := fmt.Sprintf("The prior turn stopped because the %s subscription exhausted its usage window, which resets %s. ym moved this same conversation from profile %s to %s profile %s.", e.Harness, when, e.Profile, target.Harness, target.ID)
		if target.Harness == e.Harness {
			note += " Inspect recorded tool results and filesystem effects before continuing the unfinished work. Do not blindly replay the previous prompt or repeat a tool action whose effects are already recorded."
		} else {
			note += " Inspect filesystem effects before continuing the unfinished work, and do not blindly replay the previous prompt."
		}
		out, switchErr := l.failoverSwitch(rec, target, note, fmt.Sprintf("the %s login %s is out of its subscription limit until %s", e.Harness, profile.ID(e.Profile), when))
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
	notices = append(notices, l.settleLogins(dir, ps, b, all))
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
		if json.Unmarshal(b, &a) == nil && a.State.Status != "" {
			out = append(out, a.State)
		}
	}
	return out
}
