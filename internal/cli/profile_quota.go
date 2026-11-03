package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func quotaNoticePath(dir, harness, account string, reset time.Time) string {
	sum := sha256.Sum256([]byte(harness + "\x00" + account + "\x00" + reset.UTC().Format(time.RFC3339)))
	return filepath.Join(dir, "quota-items", harness+"-"+hex.EncodeToString(sum[:12])+".json")
}

// fileQuotaItem tells the owner once per login and reset window that the login ran out, with how its work can continue.
func (l launcher) fileQuotaItem(dir string, ps []profile.Profile, e core.Exhaustion, reset time.Time, limits []core.Limit, now time.Time) error {
	if l.core == nil {
		return nil
	}
	path := quotaNoticePath(dir, e.Harness, e.Account, reset)
	if _, err := os.Stat(path); err == nil || !os.IsNotExist(err) {
		return err
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
	target, err := failoverTarget(ps, e, limits, nil, now)
	if err != nil {
		it.Options = []core.Option{wait, add}
		it.Rec = "add a login if the work cannot wait until " + when + "; no other usable " + e.Harness + " login is configured on this host"
		return it
	}
	it.Options = []core.Option{
		{Label: "switch to " + target.ID, Effect: "ym moves quota-stopped sessions to profile " + target.ID + " by itself; ym profiles default " + e.Harness + " " + target.ID + " makes new launches use it too"},
		wait, add,
	}
	it.Rec = "switch to " + target.ID + ": it is a different login with usage left, so work continues now instead of waiting until " + when
	return it
}

// exhaustedUntil is the reset of the login's latest used-up window, zero while it has usage left.
func exhaustedUntil(limits []core.Limit, harness, id, account string, now time.Time) time.Time {
	var reset time.Time
	for _, w := range limits {
		if w.Harness != harness || w.Used < 100 || !w.ResetsAt.After(now) {
			continue
		}
		if w.Account != "" && w.Account != account || w.Account == "" && profile.ID(w.Profile) != profile.ID(id) {
			continue
		}
		if w.ResetsAt.After(reset) {
			reset = w.ResetsAt
		}
	}
	return reset
}

func (l launcher) limits() []core.Limit {
	if l.core != nil && l.core.Meter != nil {
		return l.core.Meter.Fleet(time.Now()).Limits
	}
	return profileLimits(filepath.Dir(l.records.Dir))
}

// offWall keeps ym from resuming rec onto a login whose subscription is used up: it moves the conversation to a usable login of
// its harness with note as its next message, or refuses until the login resets. moved is false when rec's login has usage left, or
// when a pending switch already decides where it goes: restart applies that one.
func (l launcher) offWall(rec launch.Record, note string) (moved bool, msg string, err error) {
	if rec.Session == "" || l.operation == rec.Session {
		return false, "", nil
	}
	dir := filepath.Dir(l.records.Dir)
	ps, err := profile.Load(dir)
	if err != nil {
		return false, "", err
	}
	account := rec.Account
	if p, err := profile.Find(ps, rec.Harness, rec.Profile); err == nil && p.Account != "" {
		account = p.Account
	}
	now, limits := time.Now(), l.limits()
	reset := exhaustedUntil(limits, rec.Harness, rec.Profile, account, now)
	if reset.IsZero() {
		return false, "", nil
	}
	if m, err := loadMigration(dir, rec.Harness, rec.Session); err != nil {
		return true, "", err
	} else if m.Version != 0 && m.State != "running" {
		return false, "", nil
	}
	a, err := loadFailover(dir, rec.Harness, rec.Session)
	if err != nil {
		return true, "", err
	}
	target, err := failoverTarget(ps, core.Exhaustion{Harness: rec.Harness, Account: account}, limits, a.Pools, now, rec.Worktree.Path)
	if err != nil {
		return true, "", fmt.Errorf("%s login %s is out of its subscription limit until %s and no other usable login is configured; ym resumes nothing onto it before then, or ym switch %s --profile <id> moves it", rec.Harness, profile.ID(rec.Profile), reset.UTC().Format(time.RFC3339), rec.Key)
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
