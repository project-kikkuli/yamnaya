package cli

import (
	"cmp"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func quotaReset(reset time.Time) string {
	if reset.IsZero() {
		return "at a time not yet known"
	}
	return reset.UTC().Format(time.RFC3339)
}

func quotaItem(ps, usable []profile.Profile, unanswered map[string]bool, e core.Exhaustion, reset, retry time.Time, held error) core.Item {
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
	back := reset
	if back.IsZero() {
		back = retry
	}
	when := back.UTC().Format("Jan 2 15:04 UTC")
	state := "resets " + when
	if reset.IsZero() {
		state = "did not say when it resets, so ym tries it again " + when
	}
	it := core.Item{Kind: core.KindDecision, Session: e.Session, Harness: e.Harness,
		Question: fmt.Sprintf("%s login %s hit its subscription limit and %s; how should its work continue?", e.Harness, login, state),
		Evidence: []string{"ym usage", "account " + e.Account},
		Tried:    "ym resumes nothing onto " + login + " before " + when + " and moves quota-stopped sessions to a usable login of the same harness when there is one"}
	wait := core.Option{Label: "wait until " + when, Effect: "sessions on " + login + " stay stopped and ym resumes them after that"}
	add := core.Option{Label: "add a login", Effect: "ym profiles add " + e.Harness + " <id> && ym profiles login " + e.Harness + " <id>; ym's next pass moves quota-stopped sessions to it"}
	other := core.Other(e.Harness)
	port := core.Option{}
	if p, err := failoverTarget(usable, core.Exhaustion{Harness: other, Account: e.Account}); err == nil && held == nil {
		port = core.Option{Label: "move to " + other + "/" + p.ID, Effect: "with no " + e.Harness + " login left, ym ports each session to " + other + " login " + p.ID + " at its turn end, under a new session id, and keeps its manager and goal links"}
	}
	target, err := failoverTarget(usable, e)
	it.DoLine = "Add a login or wait"
	if err != nil {
		it.Options = []core.Option{wait, add}
		it.Rec = "add a login if the work cannot wait until " + when + "; no other usable " + e.Harness + " login is configured on this host"
		if held != nil {
			it.Rec = "wait: " + held.Error()
		}
		if port.Label != "" {
			it.Options = []core.Option{port, wait, add}
			it.DoLine = "Move harness or wait"
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
	why := "it is a different login with usage left"
	if unanswered[target.Account] {
		why = fmt.Sprintf("it is a different login, though its status check has not answered yet (ym profiles login %s %s if that lasts)", target.Harness, target.ID)
	}
	it.DoLine = "Switch login or wait"
	it.Rec = "switch to " + target.ID + ": " + why + ", so work continues now instead of waiting until " + when
	return it
}

func (l launcher) limits() []core.Limit {
	if l.core != nil && l.core.Meter != nil {
		return l.core.Meter.Fleet(time.Now()).Limits
	}
	return profileLimits(filepath.Dir(l.records.Dir))
}

// wall reports whether rec's login is barred and, if so, until when and where the conversation can go instead; target is empty when nothing
// is usable.
func (l launcher) wall(rec launch.Record) (until time.Time, target profile.Profile, ps []profile.Profile, source profile.Profile, err error) {
	dir := filepath.Dir(l.records.Dir)
	if ps, err = profile.Load(dir); err != nil {
		return
	}
	source = profile.Profile{Harness: rec.Harness, ID: profile.ID(rec.Profile), Account: rec.Account}
	if p, findErr := profile.Find(ps, rec.Harness, rec.Profile); findErr == nil {
		source = p
		source.Account = cmp.Or(p.Account, rec.Account)
	}
	now, limits := l.now(), l.limits()
	if until = barredUntil(dir, source, limits, now); until.IsZero() {
		return
	}
	a, err := loadFailover(dir, rec.Harness, rec.Session)
	if err != nil {
		return
	}
	target, _ = l.destination(dir, a, ps, core.Exhaustion{Harness: rec.Harness, Profile: source.ID, Account: source.Account}, rec.Worktree.Path, limits, now)
	return
}

// refusal is the error for a resume onto rec's used-up login, or nil when it has usage left or another login can take the session.
func (l launcher) refusal(rec launch.Record) error {
	reset, target, ps, source, err := l.wall(rec)
	if err != nil || reset.IsZero() || target.ID != "" {
		return err
	}
	return l.quotaWait(rec, ps, source, reset)
}

// quotaWait is the refusal of a resume onto rec's login, with why it cannot move instead.
func (l launcher) quotaWait(rec launch.Record, ps []profile.Profile, source profile.Profile, reset time.Time) *core.QuotaWait {
	dir := filepath.Dir(l.records.Dir)
	a, _ := loadFailover(dir, rec.Harness, rec.Session)
	_, why := l.destination(dir, a, ps, core.Exhaustion{Harness: rec.Harness, Profile: source.ID, Account: source.Account}, rec.Worktree.Path, l.limits(), l.now())
	q := &core.QuotaWait{Harness: rec.Harness, Login: profile.ID(rec.Profile), Key: rec.Key, Until: reset}
	if why != nil && !errors.Is(why, errNoUsablePool) {
		q.Held = why.Error()
		a.State = core.FailoverState{Session: rec.Session, Profile: profile.ID(rec.Profile), Account: source.Account, Status: "blocked", Reason: q.Held}
		saveFailover(dir, rec.Harness, a)
	}
	return q
}

// noteWall makes sure the owner has the barred login's one item, whichever path found it.
func (l launcher) noteWall(rec launch.Record, ps []profile.Profile, source profile.Profile) error {
	session := rec.Session
	if session == "" {
		session = rec.Key
	}
	dir, limits, now := filepath.Dir(l.records.Dir), l.limits(), l.now()
	return errors.Join(learnRows(dir, source, limits, now), l.noteLogin(dir, ps, source, session, limits, now))
}

// loginOut is when dest, the login a queued switch waits to land on, comes back, zero while it can be used.
func (l launcher) loginOut(dest profile.Profile) time.Time {
	return barredUntil(filepath.Dir(l.records.Dir), dest, l.limits(), l.now())
}

func (l launcher) now() time.Time {
	if l.core != nil && l.core.Now != nil {
		return l.core.Now()
	}
	return time.Now()
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
	reset, target, ps, source, err := l.wall(rec)
	if err != nil {
		return true, "", err
	}
	if reset.IsZero() {
		return false, "", nil
	}
	account := source.Account
	notice := l.noteWall(rec, ps, source)
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
		if pending, loadErr := loadMigration(dir, rec.Harness, rec.Session); errors.Is(err, profile.ErrOperationBusy) || errors.Is(err, errStatusUnanswered) || loadErr != nil || pending.Version != 0 && pending.State != "running" {
			return true, "", err
		}
		failure = err
		a.failed(target.Account, l.now().Add(core.QuotaRecheck), err)
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
	return true, "", errors.Join(l.quotaWait(rec, ps, source, reset), notice)
}

// freshLogin picks the login a session with no conversation to carry starts on: its own unless that is used up, else another of its
// harness. A new start under another harness would lose what links to this session, so it waits for the reset instead.
func (l launcher) freshLogin(rec launch.Record) (launch.Record, error) {
	if l.anyway {
		return rec, nil
	}
	reset, _, ps, source, err := l.wall(rec)
	if err != nil || reset.IsZero() {
		return rec, err
	}
	notice := l.noteWall(rec, ps, source)
	a, err := loadFailover(filepath.Dir(l.records.Dir), rec.Harness, rec.Session)
	if err != nil {
		return rec, err
	}
	now, limits := l.now(), l.limits()
	target, err := failoverTarget(usableLogins(filepath.Dir(l.records.Dir), a, ps, limits, now), core.Exhaustion{Harness: rec.Harness, Account: source.Account}, rec.Worktree.Path)
	if err != nil {
		return rec, errors.Join(l.quotaWait(rec, ps, source, reset), notice)
	}
	setRecordProfile(&rec, target)
	return rec, l.records.Save(rec)
}
