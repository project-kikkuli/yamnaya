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
	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

var errNoUsablePool = errors.New("no different verified usable subscription pool")

// failoverAttempt is what is about one conversation: how its failover stands and logins a switch just failed to land it on. Whether a login
// can be used is the login's own record.
type failoverAttempt struct {
	State core.FailoverState `json:"state"`
	Event string             `json:"event"`
	// Recorded is the last stop of this conversation's told to the record of On, its login: its id, the process it was seen in, and when.
	Recorded string    `json:"recorded,omitempty"`
	On       string    `json:"on,omitempty"`
	PID      int       `json:"pid,omitempty"`
	LastAt   time.Time `json:"last_at,omitzero"`
	// Unproven is that stop still waiting on its proof, which arriving makes it no second stop.
	Unproven bool `json:"unproven,omitempty"`
	// Continues is when Claude's footer said it carries on by itself.
	Continues time.Time `json:"continues,omitzero"`
	// Returning is a session ym stopped to continue that did not come back.
	Returning bool `json:"returning,omitempty"`
	// Failed holds back, until then, a login a switch failed to land on, so the next usable one is tried meanwhile; Why says why.
	Failed map[string]time.Time `json:"failed,omitempty"`
	Why    map[string]string    `json:"why,omitempty"`
}

// failed holds back the login a switch could not land on, because of err.
func (a *failoverAttempt) failed(account string, until time.Time, err error) {
	if a.Failed == nil {
		a.Failed, a.Why = map[string]time.Time{}, map[string]string{}
	}
	if a.Why == nil {
		a.Why = map[string]string{}
	}
	a.Failed[account], a.Why[account] = until, err.Error()
}

// noteStop tells the login's record of a stop of this conversation once: one stop however many ways ym hears of it, while its process lasts
// and no later turn has stopped.
func (a *failoverAttempt) noteStop(dir string, p profile.Profile, e core.Exhaustion, pid int, at, reset time.Time, limits []core.Limit) error {
	if a.Recorded == e.Event || a.Recorded != "" && a.On == p.Account && pid != 0 && a.PID == pid && (a.Unproven || !e.Seen.After(a.LastAt)) {
		if a.On != p.Account || a.Unproven {
			a.On = p.Account
			a.Unproven = false
			if e.Seen.After(a.LastAt) {
				a.LastAt = e.Seen
			}
			return saveFailover(dir, e.Harness, *a)
		}
		return nil
	}
	if err := recordStop(dir, p, at, reset, limits); err != nil {
		return err
	}
	a.Recorded, a.On, a.PID, a.LastAt, a.State.Session = e.Event, p.Account, pid, at, e.Session
	if e.Seen.After(a.LastAt) {
		a.LastAt = e.Seen
	}
	return saveFailover(dir, e.Harness, *a)
}

// stopLogin is the login a stop really happened on: the one the session started on, if the profile has since been logged in again.
func (l launcher) stopLogin(p profile.Profile, session string) profile.Profile {
	if rec, ok := l.records.Find(session); ok && profile.ID(rec.Profile) == profile.ID(p.ID) && rec.Account != "" && rec.Account != p.Account {
		p.Account = rec.Account
	}
	return p
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

// idleScreenEvery is how often an idle session that showed no stop has its pane captured again: a footer comes at a turn's end, which
// shows as a new Since, so only one printed late waits this long. One showing a stop is looked at every pass, to see it go.
const idleScreenEvery = time.Minute

// silentStops are the sessions whose screen shows them held on their limit, whatever else says so: the dialog, which starting on a used-up
// login shows without running a turn, and the footer an idle session shows. Either is the session's own quota stop like any other. One a
// session lands on is a new stop on that login, however long ago its last turn ran, so it is known by the session, the login and the
// landing. unproven are those whose stop cannot be proved over.
func (l launcher) silentStops(b core.Board) (stops []silentStop, unproven []core.Exhaustion, unlooked map[string]bool) {
	unlooked = map[string]bool{}
	if l.core == nil {
		return nil, nil, unlooked
	}
	for _, s := range b.Sessions {
		if s.Remote != "" || s.ID == "" || s.Account == "" || s.Prompt != nil {
			continue
		}
		if s.Status != core.Waiting && s.Status != core.Idle {
			if s.Status != core.Busy {
				unlooked[s.Account] = true
			}
			continue
		}
		key := fmt.Sprintf("%s-%d-%d", s.ID, s.PID, s.Since.Unix())
		if l.idleScreens != nil && s.Status == core.Idle {
			if last, seen := l.idleScreens.Load(key); seen && b.At.Sub(last.(time.Time)) < idleScreenEvery {
				unlooked[s.Account] = true
				continue
			}
		}
		stop, stopped, err := l.core.SilentQuotaStop(s.Session)
		if stop.Unread {
			unlooked[s.Account] = true
		}
		if !stopped {
			if l.idleScreens != nil && s.Status == core.Idle && !stop.Unread {
				l.idleScreens.Store(key, b.At)
			}
			continue
		}
		e := core.Exhaustion{Harness: s.Harness, Profile: s.Profile, Account: s.Account, Session: s.ID, Reason: quotaReason(s.Harness), Event: fmt.Sprintf("screen-%s-%s-%d-%d", s.ID, s.Account, s.PID, stop.At.Unix()), Seen: stop.At}
		if err != nil {
			e.Reason = err.Error()
			unproven = append(unproven, e)
			continue
		}
		stops = append(stops, silentStop{e, stop})
	}
	return stops, unproven, unlooked
}

// showUnproven gives a session whose stop cannot be proved over its login's item and its reason, as nothing can move it yet.
func (l launcher) showUnproven(dir string, ps []profile.Profile, e core.Exhaustion, at time.Time, limits []core.Limit, b core.Board) error {
	selected, err := profile.Find(ps, e.Harness, e.Profile)
	if err != nil {
		return err
	}
	a, err := loadFailover(dir, e.Harness, e.Session)
	if err != nil {
		return err
	}
	pid := 0
	if s, ok := b.Session(e.Session); ok {
		pid = s.PID
	}
	if err = a.noteStop(dir, l.stopLogin(selected, e.Session), e, pid, at, time.Time{}, limits); err != nil {
		return err
	}
	a.Unproven = true
	if a.State.Reason != e.Reason || a.State.Status != "blocked" {
		a.State = core.FailoverState{Session: e.Session, Profile: e.Profile, Account: e.Account, Status: "blocked", Reason: e.Reason}
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
	l.core.ResumedNow()
	selected := profile.Profile{Harness: e.Harness, ID: profile.ID(e.Profile), Account: e.Account}
	if err := claimLogin(dir, selected, now); err != nil {
		return err
	}
	a.Event = e.Event
	a.State = core.FailoverState{Session: e.Session, Profile: e.Profile, Account: e.Account, Status: "continued", Reason: "the limit on this login has passed"}
	once := l
	once.operation = rec.Session
	once.resumeNote = fmt.Sprintf("ym: the subscription limit on login %s has passed, so ym resumed this conversation on it. Carry on with any unfinished work.", profile.ID(e.Profile))
	err := prepareRecord(rec)
	if err == nil {
		_, err = resumeArgv(rec, once.resumeNote)
	}
	stopped := false
	if err == nil {
		_, err = l.core.Stop(s)
		stopped = err == nil
	}
	if err == nil {
		_, err = once.revive(rec)
	}
	if err != nil {
		a.State.Status, a.State.Reason = "blocked", "could not continue after the limit passed: "+err.Error()
		a.failed(e.Account, now.Add(core.QuotaRecheck), err)
		if stopped {
			a.Returning = true
			if err := l.core.Store.ClearKill(rec.Session); err != nil {
				return err
			}
			if back, ok := l.records.Find(rec.Key); ok {
				back.Killed = time.Time{}
				if err := l.records.Save(back); err != nil {
					return err
				}
			}
		}
	}
	return saveFailover(dir, e.Harness, a)
}

// verifiedStop reports a quota stop proved by the session itself, sat at the quota dialog on the turn the event names, however its limit
// rows read: they lag, and a row at 99% does not un-stop it. The reset is what the dialog says, else what its rows show. stuck is a stop the
// dialog shows but ym cannot prove over, such as a child still busy.
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
	stop, _, _ := l.core.SilentQuotaStop(s.Session)
	return cmp.Or(stop.Reset, exhaustedUntil(b.Fleet.Limits, e.Harness, e.Profile, e.Account, b.At)), true, nil
}

func failoverTarget(ps []profile.Profile, e core.Exhaustion, cwds ...string) (profile.Profile, error) {
	cwd := ""
	if len(cwds) > 0 {
		cwd = cwds[0]
	}
	for _, p := range ps {
		if p.Harness != e.Harness || p.Account == "" || p.Account == e.Account {
			continue
		}
		if p.SubscriptionSafe(cwd) {
			return p, nil
		}
	}
	return profile.Profile{}, errNoUsablePool
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
	if err := l.bringBack(dir, killed); err != nil {
		return err
	}
	silent, unproven, unlooked := l.silentStops(b)
	var notices []error
	showing := map[string]bool{}
	show := func(e core.Exhaustion) {
		showing[e.Account] = true
		if p, err := profile.Find(ps, e.Harness, e.Profile); err == nil {
			notices = append(notices, setShowing(dir, l.stopLogin(p, e.Session), true))
		}
	}
	for _, stop := range unproven {
		show(stop)
		notices = append(notices, l.showUnproven(dir, ps, stop, b.At, b.Fleet.Limits, b))
	}
	for _, stop := range silent {
		show(stop.Exhaustion)
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
	standing, err := l.standingStops(dir, ps, b, events)
	if err != nil {
		return err
	}
	events = append(events, standing...)
	for _, e := range events {
		screen := slices.IndexFunc(silent, func(s silentStop) bool { return s.Exhaustion.Event == e.Event })
		stands := slices.ContainsFunc(standing, func(s core.Exhaustion) bool { return s.Session == e.Session })
		if screen < 0 && !stands && l.copiedStop(e) {
			continue
		}
		reset, actual, dialog, selfContinues := time.Time{}, false, false, false
		at := e.Seen
		if screen >= 0 {
			reset, actual, dialog, selfContinues = silent[screen].Reset, true, true, silent[screen].SelfContinues
			at = b.At
		} else if stands {
			actual = true
		} else if reset, actual = trueExhaustion(e, b.Fleet.Limits, b.At); !actual {
			var stuck error
			reset, actual, stuck = l.verifiedStop(e, b)
			dialog = actual
			if s, alive := b.Session(e.Session); actual && alive {
				at = b.At
				e.Event += fmt.Sprintf("-%d", s.PID)
				show(e)
			}
			if stuck != nil {
				e.Reason = stuck.Error()
				notices = append(notices, l.showUnproven(dir, ps, e, b.At, b.Fleet.Limits, b))
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
		recorded := a.Recorded
		pid := 0
		if s, ok := b.Session(e.Session); ok {
			pid = s.PID
		}
		if err = a.noteStop(dir, l.stopLogin(selected, e.Session), e, pid, at, reset, b.Fleet.Limits); err != nil {
			return err
		}
		if selfContinues && a.Recorded != recorded {
			a.Continues = reset
			if !a.Continues.After(at) || a.Continues.After(at.Add(longestWindow)) {
				a.Continues = at.Add(unknownResetBar)
			}
			if err = saveFailover(dir, e.Harness, a); err != nil {
				return err
			}
		}
		s, alive := b.Session(e.Session)
		if !alive || s.Remote != "" || l.core != nil && l.core.RowsUnloaded(b.Fleet) {
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
			// The footer outranks what rows say. Past its reset Claude carries on by itself, so ym only steps in if it plainly has not.
			until = a.Continues
			if !until.After(b.At) && b.At.Sub(until) < selfContinueGrace {
				continue
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
		target, chooseErr := l.destination(dir, a, ps, e, rec.Worktree.Path, b.Fleet.Limits, b.At)
		if a.Event == e.Event && (a.State.Status == "switched" || a.State.Status == "closed" || a.State.Status == "blocked" && chooseErr != nil && a.State.Reason == chooseErr.Error()) {
			continue
		}
		if a.Event == e.Event && a.State.Status == "blocked" && a.Failed[a.State.Target].After(b.At) {
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
		a.State.Target = target.ID
		if err = saveFailover(dir, e.Harness, a); err != nil {
			return err
		}
		if err = claimLogin(dir, target, b.At); err != nil {
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
		case errors.Is(switchErr, profile.ErrOperationBusy), errors.Is(switchErr, errStatusUnanswered), errors.Is(switchErr, errPortHeld):
			a.State.Status = "blocked"
			a.State.Target = ""
			a.State.Reason = switchErr.Error()
		case switchErr != nil:
			a.State.Status = "blocked"
			a.State.Reason = switchErr.Error()
			a.failed(target.Account, b.At.Add(core.QuotaRecheck), switchErr)
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
	notices = append(notices, l.settleLogins(dir, ps, b, all, showing, unlooked))
	return errors.Join(notices...)
}

// bringBack retries the sessions ym stopped to continue and could not start again, paced like any landing. An owner's kill since ends that.
func (l launcher) bringBack(dir string, killed map[string]bool) error {
	files, err := filepath.Glob(filepath.Join(dir, "failovers", "*.json"))
	if err != nil || l.core == nil {
		return err
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var a failoverAttempt
		if json.Unmarshal(raw, &a) != nil || !a.Returning {
			continue
		}
		harness, _, _ := strings.Cut(filepath.Base(path), "-")
		rec, ok := l.records.Find(a.State.Session)
		if !ok || rec.IsLanded() || !rec.Killed.IsZero() || killed[a.State.Session] {
			a.Returning = false
			if err = saveFailover(dir, harness, a); err != nil {
				return err
			}
			continue
		}
		if !l.core.CanResume() {
			return nil
		}
		l.core.ResumedNow()
		once := l
		once.operation = rec.Session
		if _, err = once.revive(rec); err != nil {
			a.State.Reason = "could not continue after the limit passed: " + err.Error()
		} else {
			a.Returning = false
		}
		if err = saveFailover(dir, harness, a); err != nil {
			return err
		}
	}
	return nil
}

// standingStops are stops ym saw that the session's prompt coming back says nothing about: Esc cancels Claude's wait, not the stop, so a
// conversation still idle on the login it stopped on, with no turn since and the login never usable since, stays stopped.
func (l launcher) standingStops(dir string, ps []profile.Profile, b core.Board, events []core.Exhaustion) ([]core.Exhaustion, error) {
	var out []core.Exhaustion
	for _, s := range b.Sessions {
		if s.Remote != "" || s.ID == "" || s.Account == "" || slices.ContainsFunc(events, func(e core.Exhaustion) bool { return e.Session == s.ID }) {
			continue
		}
		a, err := loadFailover(dir, s.Harness, s.ID)
		if err != nil {
			return nil, err
		}
		if a.On == "" || a.On != s.Account {
			continue
		}
		p := profile.Profile{Harness: s.Harness, ID: profile.ID(s.Profile), Account: s.Account}
		turn := time.Time{}
		if rec, ok := l.records.Find(s.ID); ok && rec.Harness == claude.Name {
			turn = recordClaude(rec).LastTurnAt(s.Cwd, s.ID)
		}
		if s.Status == core.Busy || turn.After(a.LastAt) || barredUntil(dir, p, b.Fleet.Limits, b.At).IsZero() {
			a.On = ""
			if err = saveFailover(dir, s.Harness, a); err != nil {
				return nil, err
			}
			continue
		}
		if s.Status == core.Idle && !l.core.Drafting(s) {
			out = append(out, core.Exhaustion{Harness: s.Harness, Profile: s.Profile, Account: s.Account, Session: s.ID, Reason: quotaReason(s.Harness), Event: a.Recorded, Seen: b.At})
		}
	}
	return out, nil
}

// copiedStop is a ledger stop older than the session's arrival on the login it is charged to: a switch copies the transcript, so that line is
// the other login's history.
func (l launcher) copiedStop(e core.Exhaustion) bool {
	rec, ok := l.records.Find(e.Session)
	if !ok || rec.Account != e.Account || !e.Seen.Before(rec.AttachedAt) {
		return false
	}
	m, err := loadMigration(filepath.Dir(l.records.Dir), e.Harness, e.Session)
	return err == nil && m.Version != 0 && m.Destination.Account == e.Account
}

// failoverSwitch is the one place ym moves a conversation off an exhausted login by itself, with note as its next message. A refused
// apply that left the switch queued is still queued: ym serve retries it at the next turn end.
func (l launcher) failoverSwitch(rec launch.Record, target profile.Profile, note, reason string) (core.SwitchOutcome, error) {
	next := l
	next.resumeNote = note
	next.toHarness = target.Harness
	next.automatic = true
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
