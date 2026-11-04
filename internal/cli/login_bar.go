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
	"sync"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
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

// settle clears a bar a good row has disproved, with the reset that came with it, so nothing later inherits them.
func (b *loginBar) settle(limits []core.Limit, p profile.Profile, now time.Time) {
	if !b.Until.IsZero() && !b.holding(now) && showsUsage(limits, p, b.Stopped, now) {
		b.Until, b.Known, b.Held = time.Time{}, false, false
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
	if end := b.Stopped.Add(unknownResetBar); b.Held && end.Before(b.Until) && showsUsage(limits, p, b.Stopped, now) {
		return end
	}
	return b.Until
}

// recordStop is a session stopping on the login at at. A reset the stop states is kept, the latest of those known standing, unless it is
// beyond the longest window; with none the bar is the shortest window. A stop within two of those of the last means what let it back was wrong.
func recordStop(dir string, p profile.Profile, at, reset time.Time, limits []core.Limit) error {
	return updateBar(dir, p, func(b *loginBar) error {
		b.settle(limits, p, at)
		if !at.After(b.Stopped) {
			return nil
		}
		b.Held = !b.Stopped.IsZero() && at.Before(b.Stopped.Add(2*unknownResetBar))
		b.Stopped, b.Unclaimed, b.Claim = at, true, time.Time{}
		if reset.After(at.Add(longestWindow)) {
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

// releaseClaim gives back a claim whose landing never happened, so it tested nothing.
func releaseClaim(dir string, p profile.Profile) error {
	return updateBar(dir, p, func(b *loginBar) error {
		b.Unclaimed, b.Claim = true, time.Time{}
		return nil
	})
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
	said := quotaReset(reset) + " " + quotaReset(current.Until)
	for _, q := range usable {
		said += " " + q.Harness + "/" + q.ID
	}
	if open && current.Said == said || !open && current.Filing.After(now) {
		return nil
	}
	signedOut, unanswered := l.loginChecks(usable, p.Account)
	usable = slices.DeleteFunc(usable, func(q profile.Profile) bool { return signedOut[q.Account] })
	it := quotaItem(ps, usable, unanswered, core.Exhaustion{Harness: p.Harness, Profile: p.ID, Account: p.Account, Session: session}, reset, current.Until)
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
	if err = updateBar(dir, p, func(b *loginBar) error {
		b.Filing = now.Add(time.Minute)
		return nil
	}); err != nil {
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

// settleLogins keeps each login's record and item true: it learns what rows say, clears a bar a good row disproved, gives a barred login with
// a session on it its item, and closes the item of one usable again, on a row's word or once its bar has passed with no stop coming straight back.
func (l launcher) settleLogins(dir string, ps []profile.Profile, b core.Board, all []launch.Record) error {
	var errs []error
	limits := b.Fleet.Limits
	for _, p := range ps {
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
		if err != nil || r.Item == "" || slices.ContainsFunc(ps, func(p profile.Profile) bool { return barPath(dir, p) == path }) {
			continue
		}
		errs = append(errs, l.closeLoginItem(dir, profile.Profile{Harness: r.Harness, ID: r.Profile, Account: r.Account}, "this login is no longer on this host"))
	}
	return errors.Join(errs...)
}

// closeLoginItem retires the login's item, without telling the session that happened to file it.
func (l launcher) closeLoginItem(dir string, p profile.Profile, why string) error {
	if l.core == nil {
		return nil
	}
	return updateBar(dir, p, func(b *loginBar) error {
		item := b.Item
		b.Item, b.Said, b.SaidReset, b.Filed = "", "", time.Time{}, time.Time{}
		it, err := l.core.Store.Item(item)
		if err != nil || it.State != core.ItemOpen && it.State != core.ItemSnoozed {
			return nil
		}
		return l.core.Store.RetireItem(item, why)
	})
}

// authBudget is how long ym waits on native status checks, as one can hang for its whole timeout inside serve's cycle.
const authBudget = 2 * time.Second

// statusChecks keeps what native status checks answered: one slower than a pass waits still answers the next pass, however long it took.
type statusChecks struct {
	mu      sync.Mutex
	answers map[string]statusAnswer
	running map[string]chan struct{}
}

type statusAnswer struct {
	out bool
	at  time.Time
}

// statusFresh is how long an answer stands, so a pass does not spawn a native process for every login it looks at.
const statusFresh = time.Minute

// ask starts q's check unless an answer stands or one is under way, and returns what closes when it is known.
func (c *statusChecks) ask(q profile.Profile) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if done, running := c.running[q.Account]; running {
		return done
	}
	done := make(chan struct{})
	if a, known := c.answers[q.Account]; known && time.Since(a.at) < statusFresh {
		close(done)
		return done
	}
	if c.running == nil {
		c.running, c.answers = map[string]chan struct{}{}, map[string]statusAnswer{}
	}
	c.running[q.Account] = done
	go func() {
		started := time.Now()
		err := switchAuthentication(q, "")
		if took := time.Since(started); took > authBudget {
			log.Printf("ym: status check of %s/%s took %s: %v", q.Harness, q.ID, took.Round(time.Millisecond), err)
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.running, q.Account)
		if !errors.Is(err, errStatusUnanswered) {
			c.answers[q.Account] = statusAnswer{err != nil, time.Now()}
		}
		close(done)
	}()
	return done
}

func (c *statusChecks) answer(q profile.Profile) (statusAnswer, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, known := c.answers[q.Account]
	return a, known && time.Since(a.at) < statusFresh
}

// loginChecks asks the logins' native status at once, but for the account given, waiting at most authBudget. One that does not answer in
// time is unanswered: that says nothing about the login, so it costs it no place among the candidates.
func (l launcher) loginChecks(ps []profile.Profile, except string) (signedOut, unanswered map[string]bool) {
	checks := l.statuses
	if checks == nil {
		checks = &statusChecks{}
	}
	signedOut, unanswered = map[string]bool{}, map[string]bool{}
	wait := time.After(authBudget)
	waiting := false
	var asked []profile.Profile
	dones := []<-chan struct{}{}
	for _, q := range ps {
		if q.Account != "" && q.Account != except {
			asked = append(asked, q)
			dones = append(dones, checks.ask(q))
		}
	}
	for _, done := range dones {
		if !waiting {
			select {
			case <-done:
			case <-wait:
				waiting = true
			}
		}
	}
	for _, q := range asked {
		if a, known := checks.answer(q); !known {
			unanswered[q.Account] = true
		} else if a.out {
			signedOut[q.Account] = true
		}
	}
	return signedOut, unanswered
}

// checkedDestination is where a stopped session goes, counting only logins whose check answered. A login of its harness that has not answered
// still counts, so the session waits for it, and only with none left does the last resort, which has no way back, come into it.
func (l launcher) checkedDestination(usable []profile.Profile, e core.Exhaustion, cwd string, hung map[string]bool) (target profile.Profile, unanswered string, err error) {
	var asking []profile.Profile
	for _, p := range usable {
		if _, asked := hung[p.Account]; !asked && p.Harness == e.Harness {
			asking = append(asking, p)
		}
	}
	first := func(pool []profile.Profile, e core.Exhaustion) (profile.Profile, error) {
		pool = slices.Clone(pool)
		for {
			p, err := failoverTarget(pool, e, cwd)
			if err != nil {
				return p, err
			}
			if _, asked := hung[p.Account]; !asked {
				_, un := l.loginChecks([]profile.Profile{p}, "")
				hung[p.Account] = un[p.Account]
			}
			if !hung[p.Account] {
				return p, nil
			}
			unanswered = cmp.Or(unanswered, p.ID)
			pool = slices.DeleteFunc(pool, func(q profile.Profile) bool { return q.Account == p.Account })
		}
	}
	_, un := l.loginChecks(asking, "")
	for _, p := range asking {
		hung[p.Account] = un[p.Account]
	}
	if target, err = first(usable, e); err == nil {
		return target, "", nil
	}
	if unanswered != "" {
		return profile.Profile{}, unanswered, nil
	}
	if target, err = first(usable, core.Exhaustion{Harness: core.Other(e.Harness), Account: e.Account}); err == nil {
		return target, "", nil
	}
	if unanswered != "" {
		return profile.Profile{}, unanswered, nil
	}
	return profile.Profile{}, "", err
}

// barredText says how the login a session stopped on stands.
func barredText(b loginBar, session string) string {
	when := "resets at a time not yet known"
	switch {
	case b.Known:
		when = "resets " + quotaReset(b.Until)
	case !b.Until.IsZero():
		when = "tries this login again " + quotaReset(b.Until)
	}
	return fmt.Sprintf("no usable login, %s; ym switch %s --profile <id> moves it", when, core.Short(session))
}

// atStopReason is what a row shows while a session's screen holds it at its limit and it cannot move.
func atStopReason(dir string, a failoverAttempt, harness string) string {
	if a.State.Reason != errNoUsablePool.Error() {
		return "stopped at its limit: " + a.State.Reason
	}
	b, _ := readBar(barPath(dir, profile.Profile{Harness: harness, ID: profile.ID(a.State.Profile), Account: a.State.Account}))
	return "stopped at its limit, " + barredText(b, a.State.Session)
}
