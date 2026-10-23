package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

type failoverAttempt struct {
	State core.FailoverState   `json:"state"`
	Pools map[string]time.Time `json:"attempted_pools"`
	Event string               `json:"event"`
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
func trueExhaustion(e core.Exhaustion, windows []core.Limit, now time.Time) (time.Time, bool) {
	if e.Recovered || e.Account == "" || e.Seen.IsZero() {
		return time.Time{}, false
	}
	if e.Harness == "codex" && e.Reason != "usage_limit_exceeded" || e.Harness == "claude" && e.Reason != "subscription_rate_limit" {
		return time.Time{}, false
	}
	var reset time.Time
	for _, w := range windows {
		if w.Harness != e.Harness || w.Account != e.Account || w.Used < 100 || !w.ResetsAt.After(now) || w.Seen.Before(e.Seen.Add(-time.Minute)) {
			continue
		}
		if e.Harness == "codex" && !strings.HasPrefix(w.Name, "codex ") {
			continue
		}
		if e.Harness == "claude" && w.Name != "5h" && w.Name != "7d" {
			continue
		}
		if w.ResetsAt.After(reset) {
			reset = w.ResetsAt
		}
	}
	return reset, !reset.IsZero()
}
func failoverTarget(ps []profile.Profile, e core.Exhaustion, limits []core.Limit, pools map[string]time.Time, now time.Time, cwds ...string) (profile.Profile, error) {
	exhausted := map[string]bool{e.Account: true}
	for account, until := range pools {
		if until.After(now) {
			exhausted[account] = true
		}
	}
	for _, l := range limits {
		if l.Used >= 100 && l.ResetsAt.After(now) && l.Account != "" {
			exhausted[l.Account] = true
		}
	}
	cwd := ""
	if len(cwds) > 0 {
		cwd = cwds[0]
	}
	for _, p := range ps {
		if p.Harness == e.Harness && p.Account != "" && !exhausted[p.Account] && p.SubscriptionSafe(cwd) {
			return p, nil
		}
	}
	return profile.Profile{}, fmt.Errorf("no different verified usable subscription pool")
}
func (l launcher) Failover(b core.Board) error {
	dir := filepath.Dir(l.records.Dir)
	ps, err := profile.Load(dir)
	if err != nil {
		return err
	}
	for _, e := range b.Fleet.Exhaustions {
		reset, actual := trueExhaustion(e, b.Fleet.Limits, b.At)
		if !actual {
			continue
		}
		rec, ok := l.records.Find(e.Session)
		if !ok || (!rec.AttachedAt.IsZero() && e.Seen.Before(rec.AttachedAt)) || profile.ID(rec.Profile) != profile.ID(e.Profile) {
			continue
		}
		selected, err := profile.Find(ps, e.Harness, e.Profile)
		if err != nil || selected.Account != e.Account || !selected.SubscriptionSafe(rec.Worktree.Path) {
			continue
		}
		if s, alive := b.Session(e.Session); alive && !s.Settled() {
			continue
		}
		a, err := loadFailover(dir, e.Harness, e.Session)
		if err != nil {
			return err
		}
		if a.Event == e.Event && (a.State.Status == "blocked" || a.State.Status == "switched") {
			continue
		}
		a.Event = e.Event
		a.Pools[e.Account] = reset
		a.State = core.FailoverState{Session: e.Session, Profile: e.Profile, Account: e.Account, Status: "switching", Reason: e.Reason, Reset: reset}
		target, chooseErr := failoverTarget(ps, e, b.Fleet.Limits, a.Pools, b.At, rec.Worktree.Path)
		if chooseErr != nil {
			a.State.Status = "blocked"
			a.State.Reason = chooseErr.Error()
			if err = saveFailover(dir, e.Harness, a); err != nil {
				return err
			}
			continue
		}
		a.Pools[target.Account] = reset
		if err = saveFailover(dir, e.Harness, a); err != nil {
			return err
		}
		next := l
		next.resumeNote = fmt.Sprintf("The prior turn stopped because the %s subscription exhausted its usage window. ym moved this same conversation from profile %s to profile %s. Inspect recorded tool results and filesystem effects before continuing the unfinished work. Do not blindly replay the previous prompt or repeat a tool action whose effects are already recorded.", e.Harness, e.Profile, target.ID)
		_, switchErr := next.SwitchProfile(e.Session, target.ID)
		if switchErr != nil {
			a.State.Status = "blocked"
			a.State.Reason = switchErr.Error()
		} else {
			a.State.Status = "switched"
			a.State.Profile = target.ID
			a.State.Account = target.Account
		}
		if err = saveFailover(dir, e.Harness, a); err != nil {
			return err
		}
	}
	return nil
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
