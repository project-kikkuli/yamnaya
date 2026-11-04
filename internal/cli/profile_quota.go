package cli

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
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

// quotaWindow is one limit window of a login and the owner item for it.
type quotaWindow struct {
	Reset time.Time `json:"reset"`
	Item  string    `json:"item"`
	Filed time.Time `json:"filed"`
}

type quotaNotice struct {
	Harness string        `json:"harness"`
	Profile string        `json:"profile,omitempty"`
	Account string        `json:"account"`
	Windows []quotaWindow `json:"windows"`
}

// quotaItemKeep bounds an item whose reset never became known: no window outlasts a week.
const quotaItemKeep = 7 * 24 * time.Hour

// quotaNoticePaths are where one login's notice lives: under its profile and, once known, its account, so a login whose account turns
// up later, or two names for one account, keep one item.
func quotaNoticePaths(dir, harness, id, account string) []string {
	name := func(login string) string {
		sum := sha256.Sum256([]byte(harness + "\x00" + login))
		return filepath.Join(dir, "quota-items", harness+"-"+hex.EncodeToString(sum[:12])+".json")
	}
	paths := []string{name("profile:" + profile.ID(id))}
	if account != "" {
		paths = append(paths, name(account))
	}
	return paths
}

// sameQuotaWindow allows for providers reporting one window's reset a second or so apart; any further apart is another window, whose
// item must state its own reset.
func sameQuotaWindow(a, b time.Time) bool {
	return !a.IsZero() && !b.IsZero() && a.Sub(b).Abs() < time.Minute
}

func quotaReset(reset time.Time) string {
	if reset.IsZero() {
		return "at a time not yet known"
	}
	return reset.UTC().Format(time.RFC3339)
}

// loadQuotaNotice is the login's notice, under whichever name it was filed.
func loadQuotaNotice(paths []string) (quotaNotice, error) {
	var n quotaNotice
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return n, err
		}
		return n, json.Unmarshal(raw, &n)
	}
	return n, nil
}

func saveQuotaNotice(paths []string, n quotaNotice) error {
	if len(n.Windows) == 0 {
		for _, path := range paths {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	}
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err = profile.Write(path, b); err != nil {
			return err
		}
	}
	return nil
}

// fileQuotaItem tells the owner once per login and reset window that the login ran out, with how its work can continue. A stop whose reset
// is not known yet joins whichever window is open, and that window takes the reset once a row shows it.
func (l launcher) fileQuotaItem(dir string, ps []profile.Profile, e core.Exhaustion, reset time.Time, limits []core.Limit, now time.Time) error {
	if l.core == nil {
		return nil
	}
	paths := quotaNoticePaths(dir, e.Harness, e.Profile, e.Account)
	n, err := loadQuotaNotice(paths)
	if err != nil {
		return err
	}
	if len(n.Windows) > 0 && reset.IsZero() {
		return nil
	}
	if slices.ContainsFunc(n.Windows, func(w quotaWindow) bool { return sameQuotaWindow(w.Reset, reset) }) {
		return nil
	}
	learned := slices.IndexFunc(n.Windows, func(w quotaWindow) bool { return w.Reset.IsZero() })
	if learned >= 0 {
		n.Windows[learned].Reset = reset
	} else {
		added, err := l.core.AddOwnerItem(quotaItem(l.signedIn(ps, e, limits, now), e, reset, limits, now))
		if err != nil {
			return err
		}
		n.Windows = append(n.Windows, quotaWindow{Reset: reset, Item: added.Key(), Filed: now})
	}
	n.Harness, n.Profile, n.Account = e.Harness, e.Profile, cmp.Or(e.Account, n.Account)
	return saveQuotaNotice(paths, n)
}

// signedIn drops the logins an item would otherwise offer that their own native status says are signed out, so its options are true.
func (l launcher) signedIn(ps []profile.Profile, e core.Exhaustion, limits []core.Limit, now time.Time) []profile.Profile {
	for _, harness := range []string{e.Harness, core.Other(e.Harness)} {
		for {
			target, err := failoverTarget(ps, core.Exhaustion{Harness: harness, Account: e.Account}, limits, nil, now)
			if err != nil || switchAuthentication(target, "") == nil {
				break
			}
			ps = slices.DeleteFunc(slices.Clone(ps), func(p profile.Profile) bool { return p.Harness == target.Harness && p.ID == target.ID })
		}
	}
	return ps
}

// loginInUse reports whether any session, live or that ym would bring back, is on the login.
func (l launcher) loginInUse(harness, id, account string, b core.Board, all []launch.Record) bool {
	on := func(h, p, a string) bool {
		return h == harness && (account != "" && a == account || profile.ID(p) == profile.ID(id))
	}
	return slices.ContainsFunc(b.Sessions, func(s core.Live) bool { return s.Remote == "" && on(s.Harness, s.Profile, s.Account) }) ||
		slices.ContainsFunc(all, func(r launch.Record) bool {
			return !r.IsLanded() && r.Killed.IsZero() && on(r.Harness, r.Profile, r.Account)
		})
}

// closeQuotaItems retires each owner item once its question has answered itself: its window passed, rows newer than it show the login has
// usage, it has waited as long as any window lasts, or no session is left on the login.
func (l launcher) closeQuotaItems(dir string, b core.Board) error {
	if l.core == nil {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(dir, "quota-items", "*.json"))
	if len(files) == 0 {
		return nil
	}
	all, err := l.records.All()
	if err != nil {
		return err
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		var n quotaNotice
		if err = json.Unmarshal(raw, &n); err != nil {
			return err
		}
		needed := l.loginInUse(n.Harness, n.Profile, n.Account, b, all)
		login := profile.Profile{Harness: n.Harness, ID: profile.ID(n.Profile), Account: n.Account}
		var kept []quotaWindow
		for _, w := range n.Windows {
			reason := ""
			switch {
			case !w.Reset.IsZero() && b.At.After(w.Reset):
				reason = "the limit has reset"
			case showsUsage(b.Fleet.Limits, login, w.Filed, b.At):
				reason = "this login has usage again"
			case b.At.Sub(w.Filed) > quotaItemKeep:
				reason = "this login's limit has had time to pass"
			}
			lapsed := reason != ""
			if !lapsed && !needed {
				reason = "no session is left on this login"
			}
			if reason == "" {
				kept = append(kept, w)
				continue
			}
			it, err := l.core.Store.Item(w.Item)
			open := err == nil && it.State == core.ItemOpen
			if open {
				if err = l.core.Store.RetireItem(w.Item, reason); err != nil {
					return err
				}
			} else if !lapsed {
				kept = append(kept, w)
			}
		}
		if err = saveQuotaNotice(quotaNoticePaths(dir, n.Harness, n.Profile, n.Account), quotaNotice{Harness: n.Harness, Profile: n.Profile, Account: n.Account, Windows: kept}); err != nil {
			return err
		}
	}
	return nil
}

func quotaItem(ps []profile.Profile, e core.Exhaustion, reset time.Time, limits []core.Limit, now time.Time) core.Item {
	var names []string
	for _, p := range ps {
		if p.Harness == e.Harness && p.Account == e.Account {
			names = append(names, p.ID)
		}
	}
	if len(names) == 0 {
		names = []string{profile.ID(e.Profile)}
	}
	login := strings.Join(names, ", ")
	when := reset.UTC().Format("Jan 2 15:04 UTC")
	if reset.IsZero() {
		when = "at a time not yet known"
	}
	it := core.Item{Kind: core.KindDecision, Session: e.Session, Harness: e.Harness,
		Question: fmt.Sprintf("%s login %s hit its subscription limit and resets %s; how should its work continue?", e.Harness, login, when),
		Evidence: []string{"ym usage", "account " + e.Account},
		Tried:    "ym resumes nothing onto " + login + " before " + when + " and moves quota-stopped sessions to a usable login of the same harness when there is one"}
	wait := core.Option{Label: "wait until " + when, Effect: "sessions on " + login + " stay stopped and ym resumes them after the reset"}
	add := core.Option{Label: "add a login", Effect: "ym profiles add " + e.Harness + " <id> && ym profiles login " + e.Harness + " <id>; ym's next pass moves quota-stopped sessions to it"}
	other := core.Other(e.Harness)
	port := core.Option{}
	if p, err := failoverTarget(ps, core.Exhaustion{Harness: other, Account: e.Account}, limits, nil, now); err == nil {
		port = core.Option{Label: "move to " + other + "/" + p.ID, Effect: "with no " + e.Harness + " login left, ym ports each session to " + other + " login " + p.ID + " at its turn end, under a new session id, and keeps its manager and goal links"}
	}
	target, err := failoverTarget(ps, e, limits, nil, now)
	if err != nil {
		it.Options = []core.Option{wait, add}
		it.Rec = "add a login if the work cannot wait until " + when + "; no other usable " + e.Harness + " login is configured on this host"
		if port.Label != "" {
			it.Options = []core.Option{port, wait, add}
			it.Rec = port.Label + ": no other " + e.Harness + " login has usage, so work continues now instead of waiting until " + when
		}
		return it
	}
	it.Options = []core.Option{
		{Label: "switch to " + target.ID, Effect: "ym moves each session to profile " + target.ID + " once it stops on this limit; ym switch <session> --profile " + target.ID + " moves one now; ym profiles default " + e.Harness + " " + target.ID + " makes new launches use it too"},
		wait, add,
	}
	if port.Label != "" {
		it.Options = append(it.Options, port)
	}
	it.Rec = "switch to " + target.ID + ": it is a different login with usage left, so work continues now instead of waiting until " + when
	return it
}

func (l launcher) limits() []core.Limit {
	if l.core != nil && l.core.Meter != nil {
		return l.core.Meter.Fleet(time.Now()).Limits
	}
	return profileLimits(filepath.Dir(l.records.Dir))
}

// wall reports whether rec's login is used up and, if so, its reset and where the conversation can go instead; target is empty when
// nothing is usable.
func (l launcher) wall(rec launch.Record) (reset time.Time, target profile.Profile, ps []profile.Profile, account string, err error) {
	dir := filepath.Dir(l.records.Dir)
	if ps, err = profile.Load(dir); err != nil {
		return
	}
	account = rec.Account
	if p, findErr := profile.Find(ps, rec.Harness, rec.Profile); findErr == nil && p.Account != "" {
		account = p.Account
	}
	now, limits := time.Now(), l.limits()
	if reset = exhaustedUntil(limits, rec.Harness, rec.Profile, account, now); reset.IsZero() {
		return
	}
	a, err := loadFailover(dir, rec.Harness, rec.Session)
	if err != nil {
		return
	}
	target, _ = failoverDestination(a.candidates(ps, limits, now), core.Exhaustion{Harness: rec.Harness, Account: account}, limits, nil, now, rec.Worktree.Path)
	return
}

// refusal is the error for a resume onto rec's used-up login, or nil when it has usage left or another login can take the session.
func (l launcher) refusal(rec launch.Record) error {
	reset, target, _, _, err := l.wall(rec)
	if err != nil || reset.IsZero() || target.ID != "" {
		return err
	}
	return &core.QuotaWait{Harness: rec.Harness, Login: profile.ID(rec.Profile), Key: rec.Key, Until: reset}
}

// noteWall makes sure the owner has the used-up login's one item, whichever path found it.
func (l launcher) noteWall(rec launch.Record, ps []profile.Profile, account string, reset time.Time) error {
	source := rec.Session
	if source == "" {
		source = rec.Key
	}
	e := core.Exhaustion{Harness: rec.Harness, Profile: rec.Profile, Account: account, Session: source}
	return l.fileQuotaItem(filepath.Dir(l.records.Dir), ps, e, reset, l.limits(), time.Now())
}

// loginOut is when dest, the login a queued switch waits to land on, comes back, zero while it has usage left.
func (l launcher) loginOut(dest profile.Profile) time.Time {
	return exhaustedUntil(l.limits(), dest.Harness, dest.ID, dest.Account, time.Now())
}

// offWall keeps ym from resuming rec onto a login whose subscription is used up: it moves the conversation to a usable login, of its
// harness first, with note as its next message, or refuses until the login resets. moved is false when rec's login has usage left,
// or when a pending switch already decides where it goes: restart applies that one, unless it waits on a used-up login itself.
func (l launcher) offWall(rec launch.Record, note string) (moved bool, msg string, err error) {
	if rec.Session == "" || l.operation == rec.Session || l.anyway {
		return false, "", nil
	}
	m, err := loadMigration(filepath.Dir(l.records.Dir), rec.Harness, rec.Session)
	if err != nil {
		return true, "", err
	}
	if m.Version != 0 && m.State != "running" && (m.State != "queued" || l.loginOut(m.Destination).IsZero()) {
		return false, "", nil
	}
	reset, target, ps, account, err := l.wall(rec)
	if err != nil {
		return true, "", err
	}
	if reset.IsZero() {
		return false, "", nil
	}
	notice := l.noteWall(rec, ps, account, reset)
	dir := filepath.Dir(l.records.Dir)
	var failure error
	for target.ID != "" {
		a, err := loadFailover(dir, rec.Harness, rec.Session)
		if err != nil {
			return true, "", err
		}
		a.State.Session, a.State.Profile, a.State.Account, a.State.Reset = rec.Session, profile.ID(rec.Profile), account, reset
		if a.State.Status == "blocked" || a.State.Status == "" {
			a.State.Status, a.State.Target = "blocked", target.ID
			if err = saveFailover(dir, rec.Harness, a); err != nil {
				return true, "", err
			}
		}
		out, err := l.failoverSwitch(rec, target, note, fmt.Sprintf("the %s login %s is out of its subscription limit until %s", rec.Harness, profile.ID(rec.Profile), quotaReset(reset)))
		if err == nil {
			return true, out.Message, nil
		}
		if pending, loadErr := loadMigration(dir, rec.Harness, rec.Session); errors.Is(err, profile.ErrOperationBusy) || loadErr != nil || pending.Version != 0 && pending.State != "running" {
			return true, "", err
		}
		failure = err
		a.failed(target.Account, time.Now().Add(core.QuotaRecheck))
		a.State.Reason = err.Error()
		if err = saveFailover(dir, rec.Harness, a); err != nil {
			return true, "", err
		}
		if _, target, _, _, err = l.wall(rec); err != nil {
			return true, "", err
		}
	}
	if failure != nil {
		return true, "", errors.Join(failure, notice)
	}
	return true, "", errors.Join(&core.QuotaWait{Harness: rec.Harness, Login: profile.ID(rec.Profile), Key: rec.Key, Until: reset}, notice)
}

// freshLogin picks the login a session with no conversation to carry starts on: its own unless that is used up, else another of its
// harness. A new start under another harness would lose what links to this session, so it waits for the reset instead.
func (l launcher) freshLogin(rec launch.Record) (launch.Record, error) {
	if l.anyway {
		return rec, nil
	}
	reset, _, ps, account, err := l.wall(rec)
	if err != nil || reset.IsZero() {
		return rec, err
	}
	notice := l.noteWall(rec, ps, account, reset)
	a, err := loadFailover(filepath.Dir(l.records.Dir), rec.Harness, rec.Session)
	if err != nil {
		return rec, err
	}
	now, limits := time.Now(), l.limits()
	target, err := failoverTarget(a.candidates(ps, limits, now), core.Exhaustion{Harness: rec.Harness, Account: account}, limits, nil, now, rec.Worktree.Path)
	if err != nil {
		return rec, errors.Join(&core.QuotaWait{Harness: rec.Harness, Login: profile.ID(rec.Profile), Key: rec.Key, Until: reset}, notice)
	}
	setRecordProfile(&rec, target)
	return rec, l.records.Save(rec)
}
