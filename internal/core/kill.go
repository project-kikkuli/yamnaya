package core

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Toll is what a kill ends and removes: sessions, the worktrees ym made for them, and the work in those worktrees no remote holds,
// which the kill keeps for ym revive. Elsewhere names hosts whose work was not weighed.
type Toll struct {
	Sessions, Worktrees, Commits, Files int
	Elsewhere                           []string
}

func (t Toll) Add(o Toll) Toll {
	return Toll{t.Sessions + o.Sessions, t.Worktrees + o.Worktrees, t.Commits + o.Commits, t.Files + o.Files, append(t.Elsewhere, o.Elsewhere...)}
}

func (t Toll) String() string {
	parts := []string{count(t.Sessions, "session")}
	if t.Worktrees > 0 {
		parts = append(parts, count(t.Worktrees, "worktree"))
	}
	if w := t.Work(); w != "" {
		parts = append(parts, w)
	}
	if len(t.Elsewhere) > 0 {
		parts = append(parts, "work on "+strings.Join(t.Elsewhere, ", ")+" not counted")
	}
	return strings.Join(parts, ", ")
}

// Work names the unpushed commits and uncommitted files, "" when there are none.
func (t Toll) Work() string {
	var parts []string
	if t.Commits > 0 {
		parts = append(parts, count(t.Commits, "unpushed commit"))
	}
	if t.Files > 0 {
		parts = append(parts, count(t.Files, "uncommitted file"))
	}
	return strings.Join(parts, ", ")
}

func count(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// Kill ends s for good: its agent and, for a session ym launched, its pane and worktree, after keeping in ym's state on the
// session's host what ym revive needs to bring it back.
func (c *Core) Kill(s Live) (string, error) { return c.end(s, "") }

func (c *Core) end(s Live, goal string) (string, error) {
	if s.Remote != "" {
		msg, err := c.RunOrRelay(s.Remote, "kill", "--yes", s.Key())
		if errors.Is(err, ErrGone) {
			return fmt.Sprintf("%s %s on %s already ended", s.Harness, Short(s.Key()), s.Remote), nil
		}
		return msg, err
	}
	msg, err := c.Stop(s)
	if err != nil || c.Launches == nil {
		return msg, err
	}
	ended, err := c.Launches.End(s.Key(), goal)
	if errors.Is(err, ErrNotFound) {
		return msg, nil
	}
	return ended, err
}

// Weigh is the Toll of killing every session in ss.
func (c *Core) Weigh(ss []Live) Toll {
	var t Toll
	far := map[string]bool{}
	for _, s := range ss {
		t.Sessions++
		if s.Remote != "" {
			if !far[s.Remote] {
				far[s.Remote] = true
				t.Elsewhere = append(t.Elsewhere, s.Remote)
			}
			continue
		}
		if c.Launches != nil {
			if w, err := c.Launches.Weigh(s.Key()); err == nil {
				t = t.Add(w)
			}
		}
	}
	return t
}

// OrgRef reports whether ref names a goal, manager or chief on this host, by id or name.
func (c *Core) OrgRef(ref string) bool {
	o, err := c.LoadOrg()
	if err != nil {
		return false
	}
	_, err = o.lookup(ref)
	return err == nil
}

// KillTargets are the sessions killing the goal or chief ref ends, live or exited: a chief's own, or a goal's manager's, its
// agents' and its running verifier's.
func (c *Core) KillTargets(b Board, ref string) (Manager, []Live, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return Manager{}, nil, err
	}
	m, err := o.lookup(ref)
	if err != nil {
		return m, nil, err
	}
	seen := map[string]bool{}
	var out []Live
	add := func(s Live) {
		if k := s.Key(); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	byLaunch := func(session string) (Live, bool) {
		if s, ok := b.Session(session); ok {
			return s, true
		}
		for _, l := range b.Launched {
			if l.Session == session && l.Landed.IsZero() {
				return Live{Session: Session{ID: Dormant{Key: l.Key, Session: l.Session}.ID(), Harness: l.Harness}}, true
			}
		}
		return Live{}, false
	}
	if s, ok := byLaunch(m.Session); ok {
		add(s)
	}
	if _, isGoal := o.Goals[m.GoalID]; !isGoal {
		return m, out, nil
	}
	lin, err := c.Store.Lineage()
	if err != nil {
		return m, out, err
	}
	under := func(session, parent string) bool {
		seen := map[string]bool{}
		for at := session; at != "" && !seen[at]; {
			seen[at] = true
			if o.Sessions[at] == m.ID {
				return true
			}
			next, ok := lin.Parent[at]
			if !ok && at == session {
				next = parent
			}
			at = next
		}
		return false
	}
	for _, s := range b.Sessions {
		if under(s.Key(), s.Parent) {
			add(s)
		}
	}
	for _, l := range b.Launched {
		if under(l.Session, l.Parent) && l.Landed.IsZero() {
			if s, ok := byLaunch(l.Session); ok {
				add(s)
			}
		}
	}
	vs, err := c.Store.Verifications(m.GoalID)
	if err != nil {
		return m, out, err
	}
	for _, v := range vs {
		if !v.Ended && v.Session != "" {
			if s, ok := byLaunch(v.Session); ok {
				add(s)
			}
		}
	}
	return m, out, nil
}

// KillGoal kills the goal ref names, its manager, every agent working for it and its verifier, and shows it killed until it
// leaves the goal tree as a done goal does; on a chief it retires the chief alone, its reports reporting one level up.
func (c *Core) KillGoal(b Board, ref string) (string, error) {
	if host, rest, ok := c.remoteHost(ref); ok {
		return c.RunOrRelay(host, "kill", "--yes", rest)
	}
	m, targets, err := c.KillTargets(b, ref)
	if err != nil {
		return "", err
	}
	o, err := c.LoadOrg()
	if err != nil {
		return "", err
	}
	g, isGoal := o.Goals[m.GoalID]
	if isGoal {
		g.State, g.DoneAt, g.AckedAt = GoalKilled, c.now(), timeZero
		if err := c.Store.SaveGoal(g); err != nil {
			return "", err
		}
	}
	if !isGoal {
		m.Retired = true
		if err := c.Store.SaveManager(m); err != nil {
			return "", err
		}
	}
	stopped := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, s := range targets {
		wg.Go(func() { _, stopped[i] = c.Stop(s) })
	}
	wg.Wait()
	var errs []error
	for i, s := range targets {
		if stopped[i] != nil {
			errs = append(errs, stopped[i])
			continue
		}
		if c.Launches == nil {
			continue
		}
		if _, err := c.Launches.End(s.Key(), m.ID); err != nil && !errors.Is(err, ErrNotFound) {
			errs = append(errs, err)
		}
	}
	if isGoal {
		vs, err := c.Store.Verifications(g.ID)
		errs = append(errs, err)
		for _, v := range vs {
			if !v.Ended {
				v.Ended = true
				errs = append(errs, c.Store.SaveVerification(v))
			}
		}
	}
	what, id := "retired chief "+m.ID, m.ID
	if isGoal {
		what, id = "killed "+g.ID, g.ID
	}
	return fmt.Sprintf("%s: %s; ym revive %s brings it back", what, count(len(targets), "session"), id), errors.Join(errs...)
}

// Stop sends SIGTERM to each process running s and waits for them to end. Its terminal, worktree and transcript stay.
func (c *Core) Stop(s Live) (string, error) {
	if s.Remote != "" {
		return c.RunRemote(s.Remote, "kill", "--yes", "--stop", s.Key())
	}
	signal := c.Signal
	if signal == nil {
		signal = syscall.Kill
	}
	grace := c.KillGrace
	if grace == 0 {
		grace = 3 * time.Second
	}
	var pids []int
	if s.PID != 0 {
		pids = []int{s.PID}
	}
	if len(s.Copies) > 0 {
		pids = nil
		for _, cp := range s.Copies {
			pids = append(pids, cp.PID)
		}
	}
	var sent []int
	for _, pid := range pids {
		alive, same := c.Runs(pid, s.Harness, s.ID)
		if !alive {
			continue
		}
		if !same {
			return "", fmt.Errorf("pid %d no longer runs %s %s", pid, s.Harness, Short(s.Key()))
		}
		if err := signal(pid, syscall.SIGTERM); err != nil {
			return "", fmt.Errorf("pid %d: %w", pid, err)
		}
		sent = append(sent, pid)
	}
	if len(sent) == 0 {
		if err := c.StopSession(s.Key()); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s %s already ended", s.Harness, Short(s.Key())), nil
	}
	var left []string
	for end := time.Now().Add(grace); ; time.Sleep(50 * time.Millisecond) {
		left = left[:0]
		for _, pid := range sent {
			if alive, same := c.Runs(pid, s.Harness, s.ID); alive && same {
				left = append(left, fmt.Sprint(pid))
			}
		}
		if len(left) == 0 || !time.Now().Before(end) {
			break
		}
	}
	if len(left) > 0 {
		return "", fmt.Errorf("%s %s still running (pid %s) after SIGTERM", s.Harness, Short(s.Key()), strings.Join(left, ", "))
	}
	if err := c.StopSession(s.Key()); err != nil {
		return "", fmt.Errorf("killed %s %s; recording exit: %w", s.Harness, Short(s.Key()), err)
	}
	return fmt.Sprintf("killed %s %s", s.Harness, Short(s.Key())), nil
}

// Label names a session for a confirmation, such as "claude 06456f12 (Allergies rollout)".
func (s Live) Label() string {
	label := s.Harness + " " + Short(s.Key())
	if s.Title != "" {
		label += " (" + s.Title + ")"
	}
	return label
}

// ReopenGoal undoes a kill's hold on the goal or chief ref names: a goal is active again, a chief no longer retired, so ym
// supervises its manager once more.
func (c *Core) ReopenGoal(ref string) (Manager, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return Manager{}, err
	}
	m, err := o.lookup(ref)
	if err != nil {
		return m, err
	}
	if g, ok := o.Goals[m.GoalID]; ok {
		if g.State != GoalKilled {
			return m, fmt.Errorf("%s is %s, not killed", g.ID, g.State)
		}
		g.State, g.DoneAt, g.AckedAt, g.LastMovedAt = GoalActive, timeZero, timeZero, c.now()
		if err := c.Store.SaveGoal(g); err != nil {
			return m, err
		}
	}
	m.Retired, m.Failures, m.NextAttempt, m.LastError = false, 0, timeZero, ""
	return m, c.Store.SaveManager(m)
}
