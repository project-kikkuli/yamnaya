package core

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Loop is one pass of the goals' loop: notice what happened, keep every manager and chief running, wake them with what happened,
// and tell the owner what finished or got stuck.
func (c *Core) Loop(b Board) error {
	var errs []error
	for _, step := range []func(Board) error{c.ForwardIdle, c.Notice, c.Supervise, c.Wake} {
		errs = append(errs, step(b))
	}
	return errors.Join(append(errs, c.NotifyOwner())...)
}

// RecordGoalEvent records an event of kind for the goal, now, unless key names one recorded already.
func (c *Core) RecordGoalEvent(goal, kind, payload, key string) (GoalEvent, bool, error) {
	return c.Store.AddGoalEvent(GoalEvent{Goal: goal, Kind: kind, Payload: payload, Key: key, Created: c.now()})
}

// Notice records what happened on every pull request a goal's agents or manager opened: opened, CI red or green on a head,
// new review threads or a review decision, merged or closed. A pull request stays watched after the session that opened it ends.
func (c *Core) Notice(b Board) error {
	statuses, err := c.Store.PRStatuses()
	if err != nil {
		return err
	}
	o := b.Org
	var errs []error
	seen := map[string]bool{}
	for _, s := range b.Sessions {
		m, ok := o.Managed(s.Key(), b.parents)
		if !ok {
			continue
		}
		g, ok := o.Goals[m.GoalID]
		if !ok {
			continue
		}
		who := "agent " + Short(s.Key())
		if s.Key() == m.Session {
			who = "manager " + m.ID
		} else if s.Title != "" {
			who += " (" + oneLine(s.Title) + ")"
		}
		for _, pr := range s.AllPRs() {
			if seen[g.ID+pr.URL] {
				continue
			}
			seen[g.ID+pr.URL] = true
			st, fetched := statuses[pr.URL]
			title := pr.URL
			if fetched && st.Title != "" {
				title = fmt.Sprintf("%q %s", st.Title, pr.URL)
			}
			for _, e := range prEvents(pr.URL, title, who, st, fetched) {
				if _, _, err := c.RecordGoalEvent(g.ID, e.Kind, e.Payload, g.ID+":"+e.Key); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	tracked, err := c.Store.GoalPRs()
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for goal, urls := range tracked {
		if g, ok := o.Goals[goal]; !ok || !o.Active(o.Managers[g.ManagerID]) {
			continue
		}
		for _, url := range urls {
			st, fetched := statuses[url]
			if seen[goal+url] || !fetched {
				continue
			}
			title := url
			if st.Title != "" {
				title = fmt.Sprintf("%q %s", st.Title, url)
			}
			for _, e := range prEvents(url, title, "", st, true)[1:] {
				if _, _, err := c.RecordGoalEvent(goal, e.Kind, e.Payload, goal+":"+e.Key); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}

// prEvents are the events a pull request's last fetched status stands for, each keyed so it happens once.
func prEvents(url, title, who string, st PRStatus, fetched bool) []GoalEvent {
	out := []GoalEvent{{Kind: EventPROpened, Key: "pr:" + url, Payload: who + " opened " + title}}
	if !fetched || st.State == PRGone {
		return out
	}
	head := st.Merge.OID
	if head == "" {
		head = st.Head + "#" + strconv.Itoa(st.Commits)
	}
	if st.State == "OPEN" && st.Pending == 0 {
		switch {
		case st.Failed > 0:
			out = append(out, GoalEvent{Kind: EventCIRed, Key: "red:" + url + "@" + head, Payload: fmt.Sprintf("CI failed on %s: %d failing, %d passing", title, st.Failed, st.Passed)})
		case st.Passed > 0:
			out = append(out, GoalEvent{Kind: EventCIGreen, Key: "green:" + url + "@" + head, Payload: fmt.Sprintf("CI passed on %s (%d checks)", title, st.Passed)})
		}
	}
	if st.Threads > 0 {
		out = append(out, GoalEvent{Kind: EventReview, Key: "threads:" + url + "#" + strconv.Itoa(st.Threads),
			Payload: fmt.Sprintf("%s has %d review threads, %d unresolved", title, st.Threads, st.Unresolved)})
	}
	if st.Review == "APPROVED" || st.Review == "CHANGES_REQUESTED" {
		out = append(out, GoalEvent{Kind: EventReview, Key: "review:" + url + "@" + head + ":" + st.Review,
			Payload: fmt.Sprintf("%s review: %s", title, strings.ToLower(strings.ReplaceAll(st.Review, "_", " ")))})
	}
	switch st.State {
	case "MERGED":
		out = append(out, GoalEvent{Kind: EventMerged, Key: "merged:" + url, Payload: "merged " + title})
	case "CLOSED":
		out = append(out, GoalEvent{Kind: EventClosed, Key: "closed:" + url, Payload: "closed without merging " + title})
	}
	return out
}

// noticeAgent records what happened to an agent, once per key, as an event on the goal of the manager over it, which wakes the
// manager and reaches its chiefs and the owner's feed; an agent of a chief is told to the chief directly.
func (c *Core) noticeAgent(b Board, o Org, session, kind, text, key string) error {
	m, managed := o.Managed(session, b.parents)
	if !managed {
		return nil
	}
	if g, ok := o.Goals[m.GoalID]; ok {
		_, _, err := c.RecordGoalEvent(g.ID, kind, text, key)
		return err
	}
	to := o.Recipient(session, b.parents)
	if to == Owner {
		return nil
	}
	var seen int
	if err := c.Store.db.QueryRow(`SELECT count(*) FROM ym_forwarded WHERE id = ?`, key).Scan(&seen); err != nil || seen > 0 {
		return err
	}
	if err := c.tell(b, o.Managers[to].Session, "ym: "+text, false); err != nil {
		return err
	}
	_, err := c.Store.db.Exec(`INSERT INTO ym_forwarded (id, at) VALUES (?, ?)`, key, now())
	return err
}

// Wake fires due timers, nudges a manager that ended its turn with nothing in flight, then hands every running manager its goal's
// new events and every running chief its subtree's, each as one message per cycle. Events wait for a manager that is down; its
// relaunch prompt carries them.
func (c *Core) Wake(b Board) error {
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	var errs []error
	if err := c.fireTimers(b, o); err != nil {
		errs = append(errs, err)
	}
	for _, m := range o.Ordered() {
		if !o.Active(m) {
			continue
		}
		if _, ok := b.Session(m.Session); !ok {
			continue
		}
		var err error
		if g, ok := o.Goals[m.GoalID]; ok {
			if err = c.nudgeIdle(b, o, m, g); err == nil {
				err = c.wakeManager(b, m, g)
			}
		} else if m.Role == RoleChief {
			err = c.wakeChief(b, o, m)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.ID, err))
		}
	}
	return errors.Join(errs...)
}

func (c *Core) wakeManager(b Board, m Manager, g Goal) error {
	events, err := c.Store.undelivered(g.ID)
	if err != nil || len(events) == 0 {
		return err
	}
	if err := c.tell(b, m.Session, "ym: "+wakeText(g, events), false); err != nil {
		return err
	}
	return c.Store.markDelivered(events, c.now())
}

func stalesBrief(events []GoalEvent) bool {
	for _, e := range events {
		if slices.Contains(briefKinds, e.Kind) {
			return true
		}
	}
	return false
}

// wakeText lists a goal's new events for its manager, oldest first.
func wakeText(g Goal, events []GoalEvent) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s on goal %s %q, oldest first:", plural(len(events), "new event"), g.ID, shortName(g.Ask))
	for _, e := range events {
		sb.WriteString("\n- " + e.Kind + ": " + e.Payload)
	}
	if stalesBrief(events) {
		sb.WriteString("\nYour brief predates these: once you have acted, record a fresh one with ym goal brief.")
	}
	sb.WriteString("\nAct on what needs you. When the goal moves, tell the owner in one line with ym goal update \"<line>\". ym wakes you on the next event, or at a time you set with ym goal wake <when> <why>.")
	return sb.String()
}

func (c *Core) chiefEvents(o Org, m Manager) ([]GoalEvent, error) {
	var goals []string
	for _, below := range o.Subtree(m.ID) {
		if g, ok := o.Goals[below.GoalID]; ok && below.ID != m.ID {
			goals = append(goals, g.ID)
		}
	}
	since, err := c.Store.firstRun(m.ID)
	if err != nil {
		return nil, err
	}
	return c.Store.chiefPending(m.ID, goals, since)
}

func (c *Core) wakeChief(b Board, o Org, m Manager) error {
	events, err := c.chiefEvents(o, m)
	if err != nil || len(events) == 0 {
		return err
	}
	if err := c.tell(b, m.Session, "ym: "+chiefText(o, events), false); err != nil {
		return err
	}
	return c.Store.markHeard(m.ID, events, c.now())
}

// chiefText lists the state changes under a chief, oldest first.
func chiefText(o Org, events []GoalEvent) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s under you, oldest first:", plural(len(events), "change"))
	for _, e := range events {
		g := o.Goals[e.Goal]
		fmt.Fprintf(&sb, "\n- %s (%s %q) %s: %s", g.ID, g.ManagerID, shortName(g.Ask), e.Kind, e.Payload)
	}
	sb.WriteString("\nYour reports keep working without you; step in only where a nudge or a decision unblocks one.")
	return sb.String()
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}

// fireTimers turns every due timer into a wake: an event on a manager's goal, or a message to a running chief. A timer of a goal
// that ended, or of a retired chief, fires silently.
func (c *Core) fireTimers(b Board, o Org) error {
	timers, err := c.Store.Timers()
	if err != nil {
		return err
	}
	now := c.now()
	for _, t := range timers {
		if t.Due.After(now) {
			break
		}
		why := fmt.Sprintf("the wake you set at %s for %s: %s", t.Created.Local().Format(time.Kitchen), t.Due.Local().Format(time.Kitchen), t.Why)
		m, ok := o.Managers[t.Manager]
		switch {
		case !ok || !o.Active(m):
		case t.Goal != "":
			if _, _, err := c.RecordGoalEvent(t.Goal, EventTimer, why, "timer:"+strconv.FormatInt(t.Seq, 10)); err != nil {
				return err
			}
		default:
			if _, running := b.Session(m.Session); !running {
				continue
			}
			if err := c.tell(b, m.Session, "ym: "+why, false); err != nil {
				return err
			}
		}
		if err := c.Store.fireTimer(t.Seq, now); err != nil {
			return err
		}
	}
	return nil
}

// SetWake has the calling manager or chief woken at due with why.
func (c *Core) SetWake(session string, due time.Time, why string) (Timer, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return Timer{}, err
	}
	m, ok := o.ManagerOf(c.Resolve(session))
	if !ok {
		return Timer{}, fmt.Errorf("session %s is not a manager or chief; only they set wakes", Short(session))
	}
	why = strings.TrimSpace(why)
	if why == "" {
		return Timer{}, errors.New("a wake needs a reason")
	}
	return c.Store.AddTimer(Timer{Manager: m.ID, Goal: m.GoalID, Due: due.UTC(), Why: why, Created: c.now()})
}

// ParseWhen reads a wake time: a duration from now such as 30m or 2h, a clock time such as 15:04 (the next one), or RFC 3339.
func ParseWhen(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return time.Time{}, errors.New("a wake must be in the future")
		}
		return now.Add(d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"15:04", "3:04pm", "3pm"} {
		if t, err := time.ParseInLocation(layout, strings.ToLower(s), now.Location()); err == nil {
			at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
			if !at.After(now) {
				at = at.AddDate(0, 0, 1)
			}
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot read %q as a time: use a duration like 30m, a clock time like 15:04, or RFC 3339", s)
}

// UpdateGoal records the calling manager's one-line news on its goal for its chiefs and the owner.
func (c *Core) UpdateGoal(session, line string) (GoalEvent, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return GoalEvent{}, err
	}
	g, err := o.goalOf(c.Resolve(session))
	if err != nil {
		return GoalEvent{}, err
	}
	line = oneLine(line)
	if line == "" {
		return GoalEvent{}, errors.New("an update needs a line")
	}
	now := c.now()
	e, _, err := c.Store.AddGoalEvent(GoalEvent{Goal: g.ID, Kind: EventUpdate, Payload: line, Created: now, Delivered: now})
	if err != nil {
		return e, err
	}
	g.LastMovedAt = now
	return e, c.Store.SaveGoal(g)
}

// nudgeIdle wakes a manager at once when it ended a turn with nothing in flight: no agent working, no pull request awaiting CI,
// no wake set, no ask waiting on someone, and no event since its last wake. It nudges once until something else happens on the
// goal; a manager that stays idle after that is the quiet watch's to nudge and flag.
func (c *Core) nudgeIdle(b Board, o Org, m Manager, g Goal) error {
	if g.State != GoalActive {
		return nil
	}
	s, ok := b.Session(m.Session)
	if !ok || s.Status != Idle || s.Prompt != nil || s.ResultAt.IsZero() {
		return nil
	}
	if pending, err := c.Store.undelivered(g.ID); err != nil || len(pending) > 0 {
		return err
	}
	if last, err := c.Store.lastDelivered(g.ID); err != nil || last.After(s.ResultAt) {
		return err
	}
	if kind, err := c.Store.lastKind(g.ID); err != nil || kind == EventNudge {
		return err
	}
	if c.inFlight(b, o, m) {
		return nil
	}
	_, _, err := c.RecordGoalEvent(g.ID, EventNudge, "you ended your turn with nothing in flight: no agent is working, no pull request awaits CI, no wake is set and nobody owes you an answer. "+
		"Start the next piece of work with ym new, set a wake with ym goal wake <when> <why> if you are waiting on something ym cannot see, or finish with ym goal done.",
		"nudge:"+m.Session+"@"+s.ResultAt.UTC().Format(time.RFC3339Nano))
	return err
}

// inFlight reports whether anything of m's is still moving on its own: an agent that is not hung, or a subagent of one, working or just launched, a pull request with CI
// still running, a wake set, or an open ask one of its sessions filed.
func (c *Core) inFlight(b Board, o Org, m Manager) bool {
	statuses, _ := c.Store.PRStatuses()
	mine := func(key string) bool { owner, ok := o.Managed(key, b.parents); return ok && owner.ID == m.ID }
	for _, s := range b.Sessions {
		if !mine(s.Key()) {
			continue
		}
		starting := s.Status == Unknown && s.PID > 0 && s.Result == ""
		if s.Key() != m.Session && (s.Status == Busy || s.Status == Waiting || starting) && !s.Hung(c.now(), c.Woke) {
			return true
		}
		for _, member := range s.Crew {
			if member.Status == Busy || member.Status == Waiting {
				return true
			}
		}
		for _, pr := range s.AllPRs() {
			st, ok := statuses[pr.URL]
			if !ok || (st.State == "OPEN" && st.Pending > 0) {
				return true
			}
		}
	}
	for _, l := range b.Launched {
		if _, shown := b.Session(l.Session); !shown && l.State == "running" && mine(l.Session) {
			return true
		}
	}
	if timers, err := c.Store.Timers(); err == nil {
		for _, t := range timers {
			if t.Manager == m.ID {
				return true
			}
		}
	}
	if items, err := c.Store.Items(ItemFilter{States: []string{ItemOpen}}); err == nil {
		for _, it := range items {
			if it.Recipient != m.ID && mine(c.Resolve(it.Session)) {
				return true
			}
		}
	}
	return false
}

// NotifyOwner raises a desktop notification, once, for every goal that finished or got stuck.
func (c *Core) NotifyOwner() error {
	if c.Notify == nil {
		return nil
	}
	events, err := c.Store.unnotified()
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range events {
		g, err := c.Store.Goal(e.Goal)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := c.Notify(fmt.Sprintf("ym: %s %s", g.ID, e.Kind), shortName(g.Ask)+": "+e.Payload); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := c.Store.markNotified(e.Seq, c.now()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
