package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

// unknownResetBar is how long a stop with no known reset bars its login: the shortest window either harness has. After it the login gets
// one attempt, and a new stop bars it again, so no login is hammered and none is lost for good.
const unknownResetBar = 5 * time.Hour

// loginBar is all ym knows about a login's quota beyond its rows, the one answer to whether it can be used. It is per login because quota
// is: whichever session or row learns something tells every other. Serve and CLI commands both write it, under barLock.
type loginBar struct {
	Harness string    `json:"harness"`
	Profile string    `json:"profile"`
	Account string    `json:"account,omitempty"`
	Stopped time.Time `json:"stopped"`
	Until   time.Time `json:"until"`
	Known   bool      `json:"reset_known,omitempty"`
	// Held means a session stopped here during or just after an earlier bar, so rows reading well are no proof: only Until lifts this.
	Held bool `json:"held,omitempty"`
	// Item is the login's one owner item, Said the reset it states.
	Item string    `json:"item,omitempty"`
	Said time.Time `json:"said,omitzero"`
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

// barredUntil is when the login can next be used, zero when it can be now. Every path that picks, resumes or moves onto a login asks this.
func barredUntil(dir string, p profile.Profile, limits []core.Limit, now time.Time) time.Time {
	if reset := exhaustedUntil(limits, p.Harness, p.ID, p.Account, now); !reset.IsZero() {
		return reset
	}
	b, err := readBar(barPath(dir, p))
	if err != nil || !b.Until.After(now) || !b.Held && showsUsage(limits, p, b.Stopped, now) {
		return time.Time{}
	}
	return b.Until
}

// recordStop is a session stopping on the login at at. A reset the stop states is kept; with none the bar is the shortest window, unless a
// known reset still stands. A stop soon after a bar means whatever let the session back on was wrong.
func recordStop(dir string, p profile.Profile, at, reset time.Time) error {
	return updateBar(dir, p, func(b *loginBar) error {
		if !at.After(b.Stopped) {
			return nil
		}
		b.Held = b.Until.Add(unknownResetBar).After(at)
		b.Stopped = at
		switch {
		case !reset.IsZero():
			b.Until, b.Known = reset, true
		case !b.Known || !b.Until.After(at):
			b.Until, b.Known = at.Add(unknownResetBar), false
		}
		return nil
	})
}

// learnReset is a row showing the login used up until reset, which outranks what any stop guessed. The latest known reset stands, as the
// login is out until every used-up window has passed.
func learnReset(dir string, p profile.Profile, reset, seen time.Time) error {
	return updateBar(dir, p, func(b *loginBar) error {
		if b.Known && !reset.After(b.Until) {
			return nil
		}
		b.Until, b.Known = reset, true
		if b.Stopped.IsZero() {
			b.Stopped = seen
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

// noteLogin keeps the login's one owner item true while it is barred: filed once, its reset restated when a better one is learned.
func (l launcher) noteLogin(dir string, ps []profile.Profile, p profile.Profile, session string, limits []core.Limit, now time.Time) error {
	if l.core == nil {
		return nil
	}
	said := func(b loginBar) time.Time {
		if b.Known {
			return b.Until
		}
		return time.Time{}
	}
	if reset := exhaustedUntil(limits, p.Harness, p.ID, p.Account, now); !reset.IsZero() {
		if err := learnReset(dir, p, reset, now); err != nil {
			return err
		}
	}
	current, err := readBar(barPath(dir, p))
	if err != nil {
		return err
	}
	if current.Item != "" && current.Said.Equal(said(current)) {
		return nil
	}
	usable := l.signedIn(usableLogins(dir, failoverAttempt{}, ps, limits, now), p)
	return updateBar(dir, p, func(b *loginBar) error {
		reset := said(*b)
		if b.Item != "" && b.Said.Equal(reset) {
			return nil
		}
		it := quotaItem(ps, usable, core.Exhaustion{Harness: p.Harness, Profile: p.ID, Account: p.Account, Session: session}, reset, limits, now)
		if !b.Known && !b.Until.IsZero() {
			it.Tried += "; ym tries this login again " + quotaReset(b.Until)
		}
		if b.Item != "" {
			if err := l.core.Store.RewordItem(b.Item, it.Question, it.Rec, it.Options); err != nil {
				return err
			}
			b.Said = reset
			return nil
		}
		added, err := l.core.AddOwnerItem(it)
		if err != nil {
			return err
		}
		b.Item, b.Said = added.Key(), reset
		return nil
	})
}

// rowDisproved is a turn on the login recovering after its used-up rows were read, which shows they are stale.
func rowDisproved(p profile.Profile, b core.Board) bool {
	var seen time.Time
	for _, w := range b.Fleet.Limits {
		if loginWindow(w, p.Harness, p.ID, p.Account, b.At) && w.Seen.After(seen) {
			seen = w.Seen
		}
	}
	return !seen.IsZero() && slices.ContainsFunc(b.Fleet.Exhaustions, func(e core.Exhaustion) bool {
		return e.Recovered && e.Account == p.Account && e.Seen.After(seen)
	})
}

// settleLogins learns what rows say about each login, gives one with a session on it its item, and closes the item of one usable again:
// on a row's word, or once its bar has passed with no stop coming straight back.
func (l launcher) settleLogins(dir string, ps []profile.Profile, b core.Board, all []launch.Record) error {
	var errs []error
	for _, p := range ps {
		if rowDisproved(p, b) {
			continue
		}
		if reset := exhaustedUntil(b.Fleet.Limits, p.Harness, p.ID, p.Account, b.At); !reset.IsZero() {
			errs = append(errs, learnReset(dir, p, reset, b.At))
		}
		record, err := readBar(barPath(dir, p))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		barred := !barredUntil(dir, p, b.Fleet.Limits, b.At).IsZero()
		if session := l.sessionOn(p, b, all); barred && session != "" {
			errs = append(errs, l.noteLogin(dir, ps, p, session, b.Fleet.Limits, b.At))
		}
		if !barred && record.Item != "" && (b.At.After(record.Until.Add(selfContinueGrace)) || showsUsage(b.Fleet.Limits, p, record.Stopped, b.At)) {
			errs = append(errs, l.closeLoginItem(dir, p))
		}
	}
	return errors.Join(errs...)
}

// closeLoginItem retires the login's item now it is usable, without telling the session that happened to file it.
func (l launcher) closeLoginItem(dir string, p profile.Profile) error {
	if l.core == nil {
		return nil
	}
	return updateBar(dir, p, func(b *loginBar) error {
		item := b.Item
		b.Item, b.Said = "", time.Time{}
		it, err := l.core.Store.Item(item)
		if err != nil || it.State != core.ItemOpen && it.State != core.ItemSnoozed {
			return nil
		}
		return l.core.Store.RetireItem(item, "this login has usage again")
	})
}

// authBudget is how long filing an item waits on native status checks, as one can hang for its whole timeout inside serve's cycle.
const authBudget = 2 * time.Second

// signedIn drops the logins an item would otherwise offer that their native status says are signed out, so its options are true. A check
// that does not answer in time is no evidence either way: the login stays on offer as not yet checked.
func (l launcher) signedIn(ps []profile.Profile, p profile.Profile) []profile.Profile {
	results := make(chan struct {
		account string
		out     bool
	}, len(ps))
	asked := 0
	for _, q := range ps {
		if q.Account == "" || q.Account == p.Account {
			continue
		}
		asked++
		go func(q profile.Profile) {
			results <- struct {
				account string
				out     bool
			}{q.Account, switchAuthentication(q, "") != nil}
		}(q)
	}
	wait := time.After(authBudget)
	for range asked {
		select {
		case r := <-results:
			if r.out {
				ps = slices.DeleteFunc(slices.Clone(ps), func(q profile.Profile) bool { return q.Account == r.account })
			}
		case <-wait:
			return ps
		}
	}
	return ps
}

// authHangs reports a login whose native status check does not answer in time, which would hold serve's cycle on the switch that runs it.
// A pass asks each login once.
func authHangs(p profile.Profile, hung map[string]bool) bool {
	if known, asked := hung[p.Account]; asked {
		return known
	}
	done := make(chan struct{})
	go func() {
		switchAuthentication(p, "")
		close(done)
	}()
	select {
	case <-done:
		hung[p.Account] = false
	case <-time.After(authBudget):
		hung[p.Account] = true
	}
	return hung[p.Account]
}

// atStopReason is what a row shows while a session's screen holds it at its limit and it cannot move.
func atStopReason(dir string, a failoverAttempt, harness string) string {
	if a.State.Reason != errNoUsablePool.Error() {
		return "stopped at its limit: " + a.State.Reason
	}
	b, _ := readBar(barPath(dir, profile.Profile{Harness: harness, ID: profile.ID(a.State.Profile), Account: a.State.Account}))
	when := "resets at a time not yet known"
	switch {
	case b.Known:
		when = "resets " + quotaReset(b.Until)
	case !b.Until.IsZero():
		when = "tries this login again " + quotaReset(b.Until)
	}
	return fmt.Sprintf("stopped at its limit, no usable login, %s; ym switch %s --profile <id> moves it", when, core.Short(a.State.Session))
}
