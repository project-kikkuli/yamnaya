package cli

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

// unknownResetBar is how long a stop with no known reset bars its login: the shortest window either harness has. After it the login gets
// one attempt, and a new stop bars it again, so no login is hammered and none is lost for good.
const unknownResetBar = 5 * time.Hour

// longestWindow bounds a reset worth believing, as a screen read late can name a time far ahead.
const longestWindow = 7 * 24 * time.Hour

// loginBar is all ym knows about a login's quota beyond its rows, the one answer to whether it can be used. It is per login because quota
// is: whichever session or row learns something tells every other. Serve and CLI commands both write it, under updateBar's lock.
type loginBar struct {
	Harness string    `json:"harness"`
	Profile string    `json:"profile"`
	Account string    `json:"account,omitempty"`
	Stopped time.Time `json:"stopped"`
	Until   time.Time `json:"until"`
	Known   bool      `json:"reset_known,omitempty"`
	// Showing is a stop on some session's screen right now, which no row can disprove.
	Showing bool `json:"showing,omitempty"`
	// Held means this stop came soon after the last, so a row reading well is no proof until the shortest window has passed.
	Held bool `json:"held,omitempty"`
	// Unclaimed is a stop no landing has tested yet; the first takes Claim, and others wait to see whether the stop comes back.
	Unclaimed bool      `json:"unclaimed,omitempty"`
	Claim     time.Time `json:"claim,omitzero"`
	// Item is the login's one owner item, filed at Filed's stop; Said is the state its text was written for, and SaidReset the reset in it.
	Item      string    `json:"item,omitempty"`
	Filed     time.Time `json:"filed,omitzero"`
	Filing    time.Time `json:"filing,omitzero"`
	Said      string    `json:"said,omitempty"`
	SaidReset time.Time `json:"said_reset,omitzero"`
}

func barPath(dir string, p profile.Profile) string {
	login := p.Account
	if login == "" {
		login = "profile:" + profile.ID(p.ID)
	}
	sum := sha256.Sum256([]byte(p.Harness + "\x00" + login))
	return filepath.Join(dir, "logins", p.Harness+"-"+hex.EncodeToString(sum[:12])+".json")
}

func readBar(path string) (loginBar, error) {
	var b loginBar
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	return b, json.Unmarshal(raw, &b)
}

// updateBar changes the login's record under barLock, as serve, ym revive and ym switch all reach it. Rows are read, never held here.
func updateBar(dir string, p profile.Profile, change func(*loginBar) error) error {
	if err := os.MkdirAll(filepath.Join(dir, "logins"), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "logins", ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	path := barPath(dir, p)
	b, err := readBar(path)
	if err != nil {
		return err
	}
	b.Harness, b.Profile, b.Account = p.Harness, profile.ID(p.ID), p.Account
	if err = change(&b); err != nil {
		return err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return profile.Write(path, raw)
}

// holding reports a stop rows cannot lift yet.
func (b loginBar) holding(now time.Time) bool {
	return b.Held && now.Before(b.Stopped.Add(unknownResetBar))
}

// settle clears a bar a good row has disproved, with the reset that came with it, so nothing later inherits them. A stop on screen is newer
// than any row, and a landing to test the login is no longer owed once one shows usage.
func (b *loginBar) settle(limits []core.Limit, p profile.Profile, now time.Time) {
	if !b.Until.IsZero() && !b.Showing && !b.holding(now) && showsUsage(limits, p, b.Stopped, now) {
		b.Until, b.Known, b.Held, b.Unclaimed = time.Time{}, false, false, false
	}
}

// barredUntil is when the login can next be used, zero when it can be now. Every path that picks, resumes or moves onto a login asks this.
func barredUntil(dir string, p profile.Profile, limits []core.Limit, now time.Time) time.Time {
	if reset := exhaustedUntil(limits, p.Harness, p.ID, p.Account, now); !reset.IsZero() {
		return reset
	}
	b, err := readBar(barPath(dir, p))
	if err != nil {
		return time.Time{}
	}
	b.settle(limits, p, now)
	if b.Claim.After(now) {
		return b.Claim
	}
	if !b.Until.After(now) {
		return time.Time{}
	}
	return b.Until
}

// barEvidence is a bar proved by more than one stop's guess: a row at the limit or a reset the login stated.
func barEvidence(dir string, p profile.Profile, limits []core.Limit, now time.Time) bool {
	if !exhaustedUntil(limits, p.Harness, p.ID, p.Account, now).IsZero() {
		return true
	}
	b, err := readBar(barPath(dir, p))
	return err == nil && b.Known && b.Until.After(now)
}

// setShowing says whether a stop shows on the login's screens now.
func setShowing(dir string, p profile.Profile, showing bool) error {
	if b, err := readBar(barPath(dir, p)); err == nil && b.Showing == showing {
		return nil
	}
	return updateBar(dir, p, func(b *loginBar) error {
		b.Showing = showing
		return nil
	})
}

// recordStop is a session stopping on the login at at. A reset the stop states is kept, the latest of those known standing, unless it is
// beyond the longest window or already past, which a live stop contradicts; with none the bar is the shortest window. A stop within two of
// those of the last means what let it back was wrong.
func recordStop(dir string, p profile.Profile, at, reset time.Time, limits []core.Limit) error {
	return updateBar(dir, p, func(b *loginBar) error {
		b.settle(limits, p, at)
		if !at.After(b.Stopped) {
			return nil
		}
		b.Held = !b.Stopped.IsZero() && at.Before(b.Stopped.Add(2*unknownResetBar))
		b.Stopped, b.Unclaimed, b.Claim = at, true, time.Time{}
		if reset.After(at.Add(longestWindow)) || !reset.After(at) {
			reset = time.Time{}
		}
		switch {
		case !reset.IsZero() && !(b.Known && b.Until.After(reset)):
			b.Until, b.Known = reset, true
		case reset.IsZero() && (!b.Known || !b.Until.After(at)):
			b.Until, b.Known = at.Add(unknownResetBar), false
		}
		return nil
	})
}

// learnRows keeps what the login's rows say used it up. The latest known reset stands, as it is out until every used-up window has passed.
func learnRows(dir string, p profile.Profile, limits []core.Limit, now time.Time) error {
	reset := exhaustedUntil(limits, p.Harness, p.ID, p.Account, now)
	if reset.IsZero() {
		return nil
	}
	return updateBar(dir, p, func(b *loginBar) error {
		if !b.Known || reset.After(b.Until) {
			b.Until, b.Known = reset, true
		}
		return nil
	})
}

// claimLogin is a landing on the login after a stop: the first keeps others off until its stop returns or the claim passes.
func claimLogin(dir string, p profile.Profile, now time.Time) error {
	return updateBar(dir, p, func(b *loginBar) error {
		if b.Unclaimed {
			b.Unclaimed, b.Claim = false, now.Add(selfContinueGrace)
		}
		return nil
	})
}

// sessionOn is a session on the login that an owner item can be filed against, empty when nobody is on it.
func (l launcher) sessionOn(p profile.Profile, b core.Board, all []launch.Record) string {
	on := func(harness, id, account string) bool {
		return harness == p.Harness && (p.Account != "" && account == p.Account || profile.ID(id) == profile.ID(p.ID))
	}
	for _, s := range b.Sessions {
		if s.Remote == "" && s.ID != "" && on(s.Harness, s.Profile, s.Account) {
			return s.ID
		}
	}
	for _, r := range all {
		if !r.IsLanded() && r.Killed.IsZero() && r.Session != "" && on(r.Harness, r.Profile, r.Account) {
			return r.Session
		}
	}
	return ""
}

// noteLogin keeps the login's one owner item true while it is barred. It is filed once and restated when the reset or the logins it offers
// change. An item the owner closed stands for its own stop; a later stop on the wall files another. Filing reads the board, so it is outside the lock.
func (l launcher) noteLogin(dir string, ps []profile.Profile, p profile.Profile, session string, limits []core.Limit, now time.Time) error {
	if l.core == nil {
		return nil
	}
	current, err := readBar(barPath(dir, p))
	if err != nil {
		return err
	}
	open := current.Item != ""
	if open {
		it, err := l.core.Store.Item(current.Item)
		if err != nil {
			return err
		}
		if open = it.State == core.ItemOpen || it.State == core.ItemSnoozed; !open && !current.Stopped.After(current.Filed) {
			return nil
		}
	}
	usable := usableLogins(dir, failoverAttempt{}, ps, limits, now)
	reset := time.Time{}
	if current.Known {
		reset = current.Until
	}
	said := quotaReset(reset) + " " + quotaReset(current.Until) + " " + fmt.Sprint(l.portGate(dir, failoverAttempt{On: p.Account}, ps, core.Exhaustion{Harness: p.Harness, Profile: p.ID, Account: p.Account}, limits, now))
	for _, q := range usable {
		said += " " + q.Harness + "/" + q.ID
	}
	if open && current.Said == said || !open && current.Filing.After(now) {
		return nil
	}
	signedOut, unanswered := l.loginChecks(usable, p.Account)
	usable = slices.DeleteFunc(usable, func(q profile.Profile) bool { return signedOut[loginKey(q)] })
	e := core.Exhaustion{Harness: p.Harness, Profile: p.ID, Account: p.Account, Session: session}
	held := l.portGate(dir, failoverAttempt{On: p.Account}, ps, e, limits, now)
	it := quotaItem(ps, usable, unanswered, e, reset, current.Until, held)
	if open {
		told := ""
		if !reset.Equal(current.SaidReset) {
			told = "this login now resets " + quotaReset(reset)
		}
		if err = l.core.Store.RewordItem(current.Item, it, told); err != nil {
			return err
		}
		return updateBar(dir, p, func(b *loginBar) error {
			b.Said, b.SaidReset = said, reset
			return nil
		})
	}
	claimed := false
	if err = updateBar(dir, p, func(b *loginBar) error {
		if claimed = b.Item == current.Item && !b.Filing.After(now); claimed {
			b.Filing = now.Add(time.Minute)
		}
		return nil
	}); err != nil || !claimed {
		return err
	}
	added, err := l.core.AddOwnerItem(it)
	if err != nil {
		return err
	}
	return updateBar(dir, p, func(b *loginBar) error {
		b.Item, b.Filed, b.Filing, b.Said, b.SaidReset = added.Key(), current.Stopped, time.Time{}, said, reset
		return nil
	})
}

// settleLogins keeps each login's record and item true: only a look that found no stop says none shows; it learns what rows say, clears a bar a good row disproved, gives a barred login with
// a session on it its item, and closes the item of one usable again, on a row's word or once its bar has passed with no stop coming straight back.
func (l launcher) settleLogins(dir string, ps []profile.Profile, b core.Board, all []launch.Record, showing, unlooked map[string]bool) error {
	var errs []error
	limits := b.Fleet.Limits
	for _, p := range ps {
		if showing[p.Account] || !unlooked[p.Account] {
			errs = append(errs, setShowing(dir, p, showing[p.Account]))
		}
		errs = append(errs, learnRows(dir, p, limits, b.At))
		record, err := readBar(barPath(dir, p))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		settled := record
		settled.settle(limits, p, b.At)
		if !settled.Until.Equal(record.Until) {
			errs = append(errs, updateBar(dir, p, func(r *loginBar) error {
				r.settle(limits, p, b.At)
				return nil
			}))
		}
		barred := !barredUntil(dir, p, limits, b.At).IsZero()
		if session := l.sessionOn(p, b, all); barred && (session != "" || record.Item != "") {
			errs = append(errs, l.noteLogin(dir, ps, p, session, limits, b.At))
		}
		if !barred && record.Item != "" && b.At.After(settled.Until.Add(selfContinueGrace)) {
			errs = append(errs, l.closeLoginItem(dir, p, "this login has usage again"))
		}
	}
	return errors.Join(append(errs, l.closeOrphanItems(dir, ps))...)
}

// closeOrphanItems retires the items of logins no profile is on any more, re-logged in or removed: nothing else visits them.
func (l launcher) closeOrphanItems(dir string, ps []profile.Profile) error {
	files, err := filepath.Glob(filepath.Join(dir, "logins", "*.json"))
	if err != nil {
		return err
	}
	var errs []error
	for _, path := range files {
		r, err := readBar(path)
		if err != nil || r.Item == "" || slices.ContainsFunc(ps, func(p profile.Profile) bool {
			return barPath(dir, p) == path || p.Harness == r.Harness && p.ID == r.Profile && p.Account == ""
		}) {
			continue
		}
		errs = append(errs, l.closeLoginItem(dir, profile.Profile{Harness: r.Harness, ID: r.Profile, Account: r.Account}, "this login is no longer on this host"))
	}
	return errors.Join(errs...)
}

// closeLoginItem retires every item open for the login, without telling the session that happened to file one.
func (l launcher) closeLoginItem(dir string, p profile.Profile, why string) error {
	if l.core == nil {
		return nil
	}
	items, err := l.core.Store.Items(core.ItemFilter{})
	if err != nil {
		return err
	}
	return updateBar(dir, p, func(b *loginBar) error {
		mine := b.Item
		b.Item, b.Said, b.SaidReset, b.Filed = "", "", time.Time{}, time.Time{}
		var errs []error
		for _, it := range items {
			filed := p.Account != "" && it.Harness == p.Harness && slices.Contains(it.Evidence, "account "+p.Account) && strings.Contains(it.Question, "hit its subscription limit")
			if (it.Key() == mine || filed) && (it.State == core.ItemOpen || it.State == core.ItemSnoozed) {
				errs = append(errs, l.core.Store.RetireItem(it.Key(), why))
			}
		}
		return errors.Join(errs...)
	})
}

// authBudget is how long ym waits on native status checks, as one can hang for its whole timeout inside serve's cycle.
const authBudget = 2 * time.Second

// statusChecks runs native status checks out of serve's pass, which only reads what they answered: one slower than a pass still counts on the
// next, however long it took. A timeout of zero is statusTimeout.
type statusChecks struct {
	timeout time.Duration
	mu      sync.Mutex
	answers map[string]statusAnswer
	running map[string]chan struct{}
	started map[string]time.Time
}

// loginKey tells logins apart, those with no identity readable right now included.
func loginKey(p profile.Profile) string { return cmp.Or(p.Account, p.Harness+"/"+p.ID) }

type statusAnswer struct {
	err error
	at  time.Time
}

// statusFresh is how long an answer stands, so a pass does not spawn a native process for every login it looks at.
const statusFresh = time.Minute

// ask starts q's check unless an answer stands or one is under way, and returns what closes when it is known.
func (c *statusChecks) ask(q profile.Profile) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running == nil {
		c.running, c.answers, c.started = map[string]chan struct{}{}, map[string]statusAnswer{}, map[string]time.Time{}
	}
	if done, running := c.running[loginKey(q)]; running {
		return done
	}
	done := make(chan struct{})
	if a, known := c.answers[loginKey(q)]; known && time.Since(a.at) < statusFresh {
		close(done)
		return done
	}
	if c.started[loginKey(q)].IsZero() {
		c.started[loginKey(q)] = time.Now()
	}
	c.running[loginKey(q)] = done
	go func() {
		started := time.Now()
		err := nativeStatus(q, cmp.Or(c.timeout, statusTimeout))
		if took := time.Since(started); took > authBudget {
			log.Printf("ym: status check of %s/%s took %s: %v", q.Harness, q.ID, took.Round(time.Millisecond), err)
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.running, loginKey(q))
		if !errors.Is(err, errStatusUnanswered) {
			c.answers[loginKey(q)] = statusAnswer{err, time.Now()}
			delete(c.started, loginKey(q))
		}
		close(done)
	}()
	return done
}

func (c *statusChecks) answer(q profile.Profile) (statusAnswer, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, known := c.answers[loginKey(q)]
	return a, known && time.Since(a.at) < statusFresh
}

// waited is how long q has gone without an answer.
func (c *statusChecks) waited(q profile.Profile) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if at := c.started[loginKey(q)]; !at.IsZero() {
		return time.Since(at)
	}
	return 0
}

// loginChecks asks the logins' native status at once, but for the account given, waiting at most authBudget, and not at all on one already
// past that. One that does not answer in time, or answers nothing readable, is unanswered: that says nothing about the login, so it costs it no
// place among the candidates.
func (l launcher) loginChecks(ps []profile.Profile, except string) (signedOut, unanswered map[string]bool) {
	checks := l.statuses
	if checks == nil {
		checks = &statusChecks{}
	}
	signedOut, unanswered = map[string]bool{}, map[string]bool{}
	wait := time.After(authBudget)
	var asked []profile.Profile
	var dones []<-chan struct{}
	for _, q := range ps {
		if q.Account == "" || q.Account != except {
			asked = append(asked, q)
			done := checks.ask(q)
			if checks.waited(q) < authBudget {
				dones = append(dones, done)
			}
		}
	}
waiting:
	for _, done := range dones {
		select {
		case <-done:
		case <-wait:
			break waiting
		}
	}
	for _, q := range asked {
		if a, known := checks.answer(q); !known {
			unanswered[loginKey(q)] = true
		} else if answeredUnusable(a.err) {
			signedOut[loginKey(q)] = true
		} else if a.err != nil {
			unanswered[loginKey(q)] = true
		}
	}
	return signedOut, unanswered
}

// authentication is switchAuthentication for serve, which must not wait on a process: it reads what the check last answered, and says when
// there is no answer yet. Anywhere else someone is waiting, so it asks.
func (l launcher) authentication(p profile.Profile, cwd string) error {
	if l.statuses == nil {
		return switchAuthentication(p, cwd)
	}
	if err := loginConfigured(p, cwd); err != nil {
		return err
	}
	l.loginChecks([]profile.Profile{p}, "")
	a, known := l.statuses.answer(p)
	if !known {
		return fmt.Errorf("the status check of login %s: %w", p.ID, errStatusUnanswered)
	}
	return a.err
}

// unansweredReason names a login whose check has not answered and what unblocks it.
func unansweredReason(p profile.Profile) string {
	return fmt.Sprintf("the status check of login %s has not answered; it moves once that answers, or run ym profiles login %s %s", p.ID, p.Harness, p.ID)
}

// destination is where a conversation stopped on e's login goes: a login of its harness, which keeps its id, whose status check has answered,
// else, past portGate, the other harness.
func (l launcher) destination(dir string, a failoverAttempt, ps []profile.Profile, e core.Exhaustion, cwd string, limits []core.Limit, now time.Time) (profile.Profile, error) {
	if l.core != nil && l.core.Meter != nil && l.core.RowsUnloaded(l.core.Meter.Fleet(now)) {
		return profile.Profile{}, errRowsUnread
	}
	usable := usableLogins(dir, a, ps, limits, now)
	same := slices.DeleteFunc(slices.Clone(usable), func(p profile.Profile) bool { return p.Harness != e.Harness })
	_, unanswered := l.loginChecks(same, e.Account)
	answering := slices.DeleteFunc(slices.Clone(same), func(p profile.Profile) bool { return unanswered[loginKey(p)] })
	if target, err := failoverTarget(answering, e, cwd); err == nil {
		return target, nil
	}
	for _, p := range same {
		if unanswered[loginKey(p)] {
			return profile.Profile{}, errors.New(unansweredReason(p))
		}
	}
	other, err := failoverTarget(usable, core.Exhaustion{Harness: core.Other(e.Harness), Account: e.Account}, cwd)
	if err != nil {
		return profile.Profile{}, err
	}
	if err = l.portGate(dir, a, ps, e, limits, now); err != nil {
		return profile.Profile{}, err
	}
	return other, nil
}

// portGate is the one test of a move to the other harness, which has no way back. The stop must be proved, by the conversation's own stop on the
// login, a row at the limit or a stated reset. Every other login of its harness must have positive evidence it cannot take the conversation: one
// of those two, or its own answer that it is signed out. Anything else is unknown, and the conversation waits with this error as its reason.
func (l launcher) portGate(dir string, a failoverAttempt, ps []profile.Profile, e core.Exhaustion, limits []core.Limit, now time.Time) error {
	source := profile.Profile{Harness: e.Harness, ID: profile.ID(e.Profile), Account: e.Account}
	if barredUntil(dir, source, limits, now).IsZero() {
		return fmt.Errorf("its own login %s has usage again", source.ID)
	}
	if (a.On != e.Account || l.turnSince(a)) && !barEvidence(dir, source, limits, now) {
		return fmt.Errorf("the bar on login %s is one stop's guess, so ym tries it again %s", source.ID, quotaReset(barredUntil(dir, source, limits, now)))
	}
	var unknown []profile.Profile
	for _, p := range ps {
		if p.Harness == e.Harness && (p.Account == "" || p.Account != e.Account) && !barEvidence(dir, p, limits, now) {
			unknown = append(unknown, p)
		}
	}
	signedOut, unanswered := l.loginChecks(unknown, "")
	for _, p := range unknown {
		record, _ := readBar(barPath(dir, p))
		relogin := fmt.Sprintf("or run ym profiles login %s %s", p.Harness, p.ID)
		switch {
		case signedOut[loginKey(p)]:
		case unanswered[loginKey(p)]:
			return errors.New(unansweredReason(p))
		case a.Failed[p.Account].After(now):
			return fmt.Errorf("the switch to login %s failed and ym tries it again shortly: %s", p.ID, a.Why[p.Account])
		case p.Account == "":
			return fmt.Errorf("the identity of login %s cannot be read; it moves once that reads again, %s", p.ID, relogin)
		case !p.SubscriptionSafe(""):
			return fmt.Errorf("the settings of login %s cannot be read as safe; it moves once they are fixed, %s", p.ID, relogin)
		case record.Until.After(now) || record.Claim.After(now):
			return fmt.Errorf("login %s stopped a session and may still have usage; ym tries it again %s, or a newer row clears it", p.ID, quotaReset(cmp.Or(record.Claim, record.Until)))
		default:
			return fmt.Errorf("the status of login %s cannot be told; it moves once it answers, %s", p.ID, relogin)
		}
	}
	return nil
}

// turnSince is a turn having ended in the conversation after its last stop, which then says nothing about now.
func (l launcher) turnSince(a failoverAttempt) bool {
	if a.State.Session == "" {
		return false
	}
	rec, ok := l.records.Find(a.State.Session)
	return ok && rec.Harness == claude.Name && recordClaude(rec).LastTurnAt(rec.Worktree.Path, rec.Session).After(a.LastAt)
}

// errRowsUnread is a meter that has not read its rows yet, which is no reason to think a login without one is free.
var errRowsUnread = errors.New("usage rows have not been read yet, so ym waits a moment before choosing a login")

// barredText says how the login a session stopped on stands, and what moves it from where it sits.
func barredText(b loginBar, harness, session string) string {
	when := "resets at a time not yet known"
	switch {
	case b.Known:
		when = "resets " + quotaReset(b.Until)
	case !b.Until.IsZero():
		when = "tries this login again " + quotaReset(b.Until)
	}
	return fmt.Sprintf("%s; ym switch %s --profile <%s login> moves it, or --to %s once it is at its prompt", when, core.Short(session), harness, core.Other(harness))
}

// atStopReason is what a row shows while a session's screen holds it at its limit and it cannot move.
func atStopReason(dir string, a failoverAttempt, harness string) string {
	if a.State.Reason != errNoUsablePool.Error() {
		return "stopped at its limit: " + a.State.Reason
	}
	b, _ := readBar(barPath(dir, profile.Profile{Harness: harness, ID: profile.ID(a.State.Profile), Account: a.State.Account}))
	return "stopped at its limit, no usable login, " + barredText(b, harness, a.State.Session)
}
