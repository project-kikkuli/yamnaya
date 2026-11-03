package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

type quotaNotice struct {
	Harness string    `json:"harness"`
	Account string    `json:"account"`
	Reset   time.Time `json:"reset"`
	Item    string    `json:"item"`
}

func quotaNoticePath(dir, harness, account string) string {
	sum := sha256.Sum256([]byte(harness + "\x00" + account))
	return filepath.Join(dir, "quota-items", harness+"-"+hex.EncodeToString(sum[:12])+".json")
}

// sameQuotaWindow allows for providers reporting one window's reset a second or so apart; a new window starts hours later.
func sameQuotaWindow(a, b time.Time) bool {
	return a.Sub(b).Abs() < time.Hour
}

// fileQuotaItem tells the owner once per login and reset window that the login ran out, with how its work can continue.
func (l launcher) fileQuotaItem(dir string, ps []profile.Profile, e core.Exhaustion, reset time.Time, limits []core.Limit, now time.Time) error {
	if l.core == nil {
		return nil
	}
	path := quotaNoticePath(dir, e.Harness, e.Account)
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var filed quotaNotice
	if err == nil {
		if err = json.Unmarshal(raw, &filed); err != nil {
			return err
		}
		if sameQuotaWindow(filed.Reset, reset) {
			return nil
		}
	}
	added, err := l.core.AddOwnerItem(quotaItem(ps, e, reset, limits, now))
	if err != nil {
		return err
	}
	b, err := json.Marshal(quotaNotice{Harness: e.Harness, Account: e.Account, Reset: reset, Item: added.Key()})
	if err != nil {
		return err
	}
	return profile.Write(path, b)
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
		{Label: "switch to " + target.ID, Effect: "ym moves quota-stopped sessions to profile " + target.ID + " by itself; ym profiles default " + e.Harness + " " + target.ID + " makes new launches use it too"},
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
	target, _ = failoverDestination(ps, core.Exhaustion{Harness: rec.Harness, Account: account}, limits, a.Pools, now, rec.Worktree.Path)
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
	if target.ID == "" {
		return true, "", errors.Join(&core.QuotaWait{Harness: rec.Harness, Login: profile.ID(rec.Profile), Key: rec.Key, Until: reset}, notice)
	}
	dir := filepath.Dir(l.records.Dir)
	a, err := loadFailover(dir, rec.Harness, rec.Session)
	if err != nil {
		return true, "", err
	}
	if a.State.Status == "blocked" {
		a.State.Target = target.ID
		if err = saveFailover(dir, rec.Harness, a); err != nil {
			return true, "", err
		}
	}
	out, err := l.failoverSwitch(rec, target, note, fmt.Sprintf("the %s login %s is out of its subscription limit until %s", rec.Harness, profile.ID(rec.Profile), reset.UTC().Format(time.RFC3339)))
	return true, out.Message, err
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
	target, err := failoverTarget(ps, core.Exhaustion{Harness: rec.Harness, Account: account}, l.limits(), a.Pools, time.Now(), rec.Worktree.Path)
	if err != nil {
		return rec, errors.Join(&core.QuotaWait{Harness: rec.Harness, Login: profile.ID(rec.Profile), Key: rec.Key, Until: reset}, notice)
	}
	setRecordProfile(&rec, target)
	return rec, l.records.Save(rec)
}
