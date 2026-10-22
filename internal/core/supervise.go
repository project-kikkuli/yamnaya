package core

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

var timeZero time.Time

const (
	// StartGrace is how long a launched session may take to show up on the board before ym counts it dead.
	StartGrace = 2 * time.Minute
	// QuietAfter is how long a goal may go without anything moving before its manager is nudged, and between nudges.
	QuietAfter = 30 * time.Minute
	// Nudges is how many nudges a quiet manager gets before its goal shows as stuck in the owner's queue.
	Nudges = 2
	// maxBackoff caps the wait between relaunches of a manager that keeps failing.
	maxBackoff = time.Hour
)

const quietEvidence = "quiet: "

// backoff is the wait after the nth launch in a row that did not stay up.
func backoff(n int) time.Duration {
	d := time.Minute
	for i := 1; i < n && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}

// Alive reports whether m's current session runs: on the board, or in a pane of ym's server the board has not read yet.
func (b Board) Alive(m Manager) bool {
	if m.Session == "" {
		return false
	}
	if _, ok := b.Session(m.Session); ok {
		return true
	}
	for _, l := range b.Launched {
		if l.Session == m.Session && l.State == "running" {
			return true
		}
	}
	return false
}

// Supervise keeps every active goal's manager and every chief running and moving: a dead one is relaunched with backoff, a quiet
// one is nudged, and one still stuck after its nudges, or one that cannot be launched, puts one stuck row in the owner's queue.
func (c *Core) Supervise(b Board) error {
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	var errs []error
	for _, m := range o.Ordered() {
		if s, ok := b.Session(c.Resolve(m.Session)); ok && s.Key() != m.Session {
			m.Session = s.Key()
			if err := c.Store.SaveManager(m); err != nil {
				return err
			}
			o.Managers[m.ID] = m
			o.Sessions[m.Session] = m.ID
		}
	}
	for _, m := range o.Ordered() {
		g, hasGoal := o.Goals[m.GoalID]
		if hasGoal && (g.State == GoalDone || g.State == GoalDropped) {
			continue
		}
		if err := c.supervise(b, o, m, g, hasGoal); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.ID, err))
		}
	}
	if o, err = c.LoadOrg(); err != nil {
		return errors.Join(append(errs, err)...)
	}
	if err := c.reroute(b, o); err != nil {
		errs = append(errs, err)
	}
	if err := c.noticeMerges(b, o); err != nil {
		errs = append(errs, err)
	}
	if err := c.verify(b, o); err != nil {
		errs = append(errs, err)
	}
	if err := c.noticeExits(b, o); err != nil {
		errs = append(errs, err)
	}
	if err := c.noticeHung(b, o); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (c *Core) supervise(b Board, o Org, m Manager, g Goal, hasGoal bool) error {
	now := c.now()
	alive := b.Alive(m)
	starting := !m.LaunchedAt.IsZero() && now.Sub(m.LaunchedAt) < StartGrace && m.LastError == ""
	s, _ := b.Session(m.Session)
	hung := alive && !starting && s.Hung(now, c.Woke)
	switch {
	case hung:
		if now.Before(m.NextAttempt) {
			break
		}
		m.Failures++
		m.NextAttempt = now.Add(backoff(m.Failures))
		if _, err := c.Kill(s); err != nil {
			m.LastError = "hung since " + s.lastActive().Local().Format(time.DateTime) + "; " + err.Error()
			if err := c.Store.SaveManager(m); err != nil {
				return err
			}
			break
		}
		m = c.launch(b, o, m)
	case alive && !starting:
		if m.Failures > 0 || !m.NextAttempt.IsZero() {
			m.Failures, m.NextAttempt = 0, timeZero
			if err := c.Store.SaveManager(m); err != nil {
				return err
			}
		}
	case !alive && !starting && !now.Before(m.NextAttempt):
		m.Failures++
		m.NextAttempt = now.Add(backoff(m.Failures))
		m = c.launch(b, o, m)
	}
	if !hasGoal {
		return nil
	}
	if m.Stuck() {
		evidence := fmt.Sprintf("manager %s did not stay up after %d launches in a row", m.ID, m.Failures)
		if m.LastError != "" {
			evidence += "; last launch failed: " + m.LastError
		}
		if g.State != GoalStuck || g.Evidence != evidence {
			was := g.State
			g.State, g.Evidence, g.AckedAt = GoalStuck, evidence, timeZero
			return c.saveStuck(g, was)
		}
		return nil
	}
	if g.State == GoalStuck && !strings.HasPrefix(g.Evidence, quietEvidence) && !strings.HasPrefix(g.Evidence, verifyEvidence) {
		g.State, g.Evidence = GoalActive, ""
		if err := c.Store.SaveGoal(g); err != nil {
			return err
		}
	}
	if !alive || hung || g.State == GoalVerifying {
		return nil
	}
	return c.watchQuiet(b, o, m, g)
}

// watchQuiet moves the goal's clock on anything that moved and nudges, then flags, a manager whose goal stopped moving.
func (c *Core) watchQuiet(b Board, o Org, m Manager, g Goal) error {
	now := c.now()
	if moved := c.lastMoved(b, o, m, g); moved.Sub(g.LastMovedAt) >= time.Minute || (moved.After(g.LastMovedAt) && (g.State == GoalStuck || m.Nudges > 0)) {
		g.LastMovedAt = moved
		if g.State == GoalStuck && strings.HasPrefix(g.Evidence, quietEvidence) {
			g.State, g.Evidence = GoalActive, ""
		}
		if err := c.Store.SaveGoal(g); err != nil {
			return err
		}
		if m.Nudges > 0 {
			m.Nudges, m.NudgedAt = 0, timeZero
			if err := c.Store.SaveManager(m); err != nil {
				return err
			}
		}
		return nil
	}
	since := g.LastMovedAt
	if m.NudgedAt.After(since) {
		since = m.NudgedAt
	}
	if now.Sub(since) < QuietAfter {
		return nil
	}
	if m.Nudges < Nudges {
		text := fmt.Sprintf("ym: nothing has moved on goal %s since %s. Check your agents and PRs, update your brief at %s, and keep driving the goal to done; if it is blocked on something beyond your authority, escalate with ym item add.",
			g.ID, g.LastMovedAt.Local().Format(time.Kitchen), m.BriefPath)
		if err := c.tell(b, m.Session, text, false); err != nil {
			return err
		}
		m.Nudges, m.NudgedAt = m.Nudges+1, now
		return c.Store.SaveManager(m)
	}
	if g.State == GoalStuck {
		return nil
	}
	was := g.State
	g.State, g.AckedAt = GoalStuck, timeZero
	g.Evidence = fmt.Sprintf("%snothing moved since %s; nudged %d times, last at %s", quietEvidence,
		g.LastMovedAt.Local().Format(time.DateTime), m.Nudges, m.NudgedAt.Local().Format(time.DateTime))
	if s, ok := b.Session(m.Session); ok && s.Doing.Tool != "" {
		g.Evidence += "; last doing " + s.Doing.String()
	}
	return c.saveStuck(g, was)
}

// saveStuck saves a goal that got stuck and, when it was not stuck before, records the stuck event the owner is notified of.
func (c *Core) saveStuck(g Goal, was string) error {
	if err := c.Store.SaveGoal(g); err != nil || was == GoalStuck {
		return err
	}
	_, _, err := c.RecordGoalEvent(g.ID, EventStuck, g.Evidence, "")
	return err
}

// lastMoved is the latest sign of life in m's goal: its session or its agents' turns, its brief, or now while any of them works or
// it waits on the owner.
func (c *Core) lastMoved(b Board, o Org, m Manager, g Goal) time.Time {
	now := c.now()
	var latest time.Time
	seen := func(t time.Time) {
		if t.After(latest) && !t.After(now) {
			latest = t
		}
	}
	for _, s := range b.Sessions {
		if owner, ok := o.Managed(s.Key(), b.parents); !ok || owner.ID != m.ID {
			continue
		}
		if (s.Status == Busy && !s.Hung(now, c.Woke)) || s.Status == Waiting {
			return now
		}
		seen(s.lastActive())
	}
	if info, err := os.Stat(m.BriefPath); err == nil {
		seen(info.ModTime())
	}
	if items, err := c.Store.Items(ItemFilter{States: []string{ItemOpen}}); err == nil {
		for _, it := range items {
			if it.Recipient == Owner && o.Sessions[it.Session] == m.ID {
				return now
			}
		}
	}
	return latest
}

// noticeMerges tells a manager once per head that a green pull request of its agents may be merged in a repository it is cleared for.
func (c *Core) noticeMerges(b Board, o Org) error {
	statuses, err := c.Store.PRStatuses()
	if err != nil {
		return err
	}
	for _, s := range b.Sessions {
		m, ok := o.Managed(s.Key(), b.parents)
		if !ok {
			continue
		}
		for _, pr := range s.AllPRs() {
			st, ok := statuses[pr.URL]
			if !ok || !st.ReadyToMerge() || !st.OwnedPR() {
				continue
			}
			if cleared, err := c.Store.CanMerge(prRepoName(pr)); err != nil || !cleared {
				continue
			}
			fresh, err := c.Store.NoteMerge(pr.URL, st.Merge.OID, m.ID)
			if err != nil || !fresh {
				continue
			}
			text := fmt.Sprintf("ym: %s is green, approved or unreviewed, with no open threads and a clean merge state. You are cleared to merge in %s: merge it when it is what the goal needs.", pr.URL, prRepoName(pr))
			if err := c.tell(b, m.Session, text, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// prRepoName is the bare repository name a pull request belongs to, as ym_repo_authority keys it.
func prRepoName(pr PR) string {
	repo := pr.Repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		repo = repo[i+1:]
	}
	return strings.ToLower(repo)
}
