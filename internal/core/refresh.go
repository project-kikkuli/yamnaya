package core

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ContextCap is the context, in tokens, past which ym restarts an idle manager or chief fresh from its brief, until the owner sets
// another with ym goal context-cap.
const ContextCap = 150_000

// CacheLife is how long the model's prompt cache outlives a session's last reply; resuming after it reads the whole transcript cold.
const CacheLife = time.Hour

const (
	refreshAgents = 8
	refreshPRs    = 10
	refreshEvents = 40
)

const contextSchema = `
CREATE TABLE IF NOT EXISTS ym_contexts (
	manager TEXT PRIMARY KEY,
	session TEXT NOT NULL,
	tokens INTEGER NOT NULL,
	at TEXT NOT NULL,
	asked TEXT
);
CREATE TABLE IF NOT EXISTS ym_context_cap (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	tokens INTEGER NOT NULL
);
`

// ManagerContext is the context a manager's or chief's session read on its latest reply, as ym last saw it, and when ym asked that
// session for a fresh brief before restarting it.
type ManagerContext struct {
	Session string
	Tokens  int64
	At      time.Time
	Asked   time.Time
}

// ContextCap is the context past which ym restarts an idle manager or chief; zero means never.
func (s *Store) ContextCap() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT tokens FROM ym_context_cap WHERE id = 1`).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return ContextCap, nil
	}
	return n, err
}

// SetContextCap sets the context past which ym restarts an idle manager or chief; zero turns restarts off.
func (s *Store) SetContextCap(tokens int64) error {
	if tokens < 0 {
		return errors.New("a context cap cannot be negative")
	}
	_, err := s.db.Exec(`INSERT INTO ym_context_cap (id, tokens) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET tokens = excluded.tokens`, tokens)
	return err
}

// Contexts maps each manager and chief ym has seen reply to its session's latest context.
func (s *Store) Contexts() (map[string]ManagerContext, error) {
	rows, err := s.db.Query(`SELECT manager, session, tokens, at, asked FROM ym_contexts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ManagerContext{}
	for rows.Next() {
		var id string
		var mc ManagerContext
		var at, asked sql.NullString
		if err := rows.Scan(&id, &mc.Session, &mc.Tokens, &at, &asked); err != nil {
			return nil, err
		}
		mc.At, mc.Asked = parseTime(at), parseTime(asked)
		out[id] = mc
	}
	return out, rows.Err()
}

func (s *Store) noteContext(manager string, mc ManagerContext) error {
	_, err := s.db.Exec(`INSERT INTO ym_contexts (manager, session, tokens, at) VALUES (?, ?, ?, ?)
		ON CONFLICT (manager) DO UPDATE SET asked = CASE WHEN session = excluded.session THEN asked END,
		session = excluded.session, tokens = excluded.tokens, at = excluded.at`, manager, mc.Session, mc.Tokens, stamp(mc.At))
	return err
}

func (s *Store) noteBriefAsk(manager string, at time.Time) error {
	_, err := s.db.Exec(`UPDATE ym_contexts SET asked = ? WHERE manager = ?`, stamp(at), manager)
	return err
}

// refreshMove is what a wake due to a manager over its context cap does.
type refreshMove int

const (
	// wakeAsUsual hands the wake to the session it has.
	wakeAsUsual refreshMove = iota
	// holdWake keeps the wake until the session ends its turn.
	holdWake
	// askBrief has the session record a fresh brief first and keeps the wake.
	askBrief
	// restartFresh retires the session and hands the wake to a fresh one.
	restartFresh
)

// refreshStep decides a due wake of a session that read tokens of context on its latest reply, past limit or not: idle is whether it
// ended its turn, stale whether its brief predates what happened, asked when ym asked it for a fresh brief, replied when it last
// replied. An ask it has not answered within BriefGrace no longer holds it.
func refreshStep(tokens, limit int64, idle, stale bool, asked, replied, now time.Time) refreshMove {
	switch {
	case limit <= 0 || tokens < limit:
		return wakeAsUsual
	case !idle:
		return holdWake
	case stale && asked.IsZero():
		return askBrief
	case !asked.IsZero() && !replied.After(asked) && now.Sub(asked) < BriefGrace:
		return holdWake
	}
	return restartFresh
}

// resumeCold reports whether resuming a session whose last reply read tokens of context at at would read a large transcript with
// its cache gone, so a fresh session from the brief costs less.
func resumeCold(tokens, limit int64, at, now time.Time) bool {
	return limit > 0 && tokens >= limit && !at.IsZero() && now.Sub(at) >= CacheLife
}

// trackContexts records the context each running manager and chief on this host read on its latest reply.
func (c *Core) trackContexts(b Board, o Org) error {
	seen, err := c.Store.Contexts()
	if err != nil {
		return err
	}
	var errs []error
	for _, m := range o.Ordered() {
		s, ok := b.Session(m.Session)
		if m.Host != "" || !ok || s.Context == 0 {
			continue
		}
		now := ManagerContext{Session: m.Session, Tokens: s.Context, At: s.ContextAt}
		if was := seen[m.ID]; was.Session == now.Session && was.Tokens == now.Tokens && was.At.Equal(now.At) {
			continue
		}
		errs = append(errs, c.Store.noteContext(m.ID, now))
	}
	return errors.Join(errs...)
}

// refreshSwollen restarts, fresh from their briefs, the idle managers and chiefs past the context cap that have a wake due, and
// returns the ones whose wake ym keeps: restarted, asked for a brief first, or mid-turn.
func (c *Core) refreshSwollen(b Board, o Org) (map[string]bool, error) {
	limit, err := c.Store.ContextCap()
	if err != nil || limit <= 0 {
		return nil, err
	}
	held := map[string]bool{}
	var fresh map[string]Freshness
	var timers []Timer
	var asks map[string]ManagerContext
	var errs []error
	off, err := c.Store.offTargets()
	if err != nil {
		return held, err
	}
	now := c.now()
	for _, m := range o.Ordered() {
		s, ok := b.Session(m.Session)
		if m.Host != "" || !o.Active(m) || !ok || s.Context < limit || managerOff(off, o, b.parents, m) {
			continue
		}
		if fresh == nil {
			if fresh, err = c.Freshness(o); err != nil {
				return held, err
			}
			if timers, err = c.Store.Timers(); err != nil {
				return held, err
			}
			if asks, err = c.Store.Contexts(); err != nil {
				return held, err
			}
		}
		due, err := c.wakeWaits(o, m, timers)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !due {
			continue
		}
		f := fresh[m.ID]
		behind := f.Behind
		if f.Brief == nil {
			behind = m.LaunchedAt
		}
		asked := timeZero
		if mc := asks[m.ID]; mc.Session == m.Session && !mc.Asked.Before(behind) {
			asked = mc.Asked
		}
		idle := s.Status == Idle && s.Prompt == nil
		switch refreshStep(s.Context, limit, idle, f.Stale(), asked, s.ResultAt, now) {
		case wakeAsUsual:
			continue
		case askBrief:
			text := fmt.Sprintf("ym: your context is at %s tokens, past the %s ym restarts you at, and a wake is waiting. Record a fresh brief now (%s) with ym goal brief --assessment \"…\" --changed \"…\" --uncertainty \"…\" --next \"…\" --waiting-on \"…\", putting in it everything a fresh session of yours needs, then end your turn: ym restarts you from it with the wake.",
				Kilo(s.Context), Kilo(limit), f.reason())
			if err := c.tell(b, m.Session, text, false); err != nil {
				errs = append(errs, err)
				continue
			}
			errs = append(errs, c.Store.noteBriefAsk(m.ID, now), c.Store.noteBriefNudge(m.ID, behind))
		case restartFresh:
			if _, err := c.refresh(b, o, m, s, s.Context, false); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", m.ID, err))
			}
		}
		held[m.ID] = true
	}
	return held, errors.Join(errs...)
}

// wakeWaits reports whether a wake is due to m: its goal's or subtree's events, or a timer of its own.
func (c *Core) wakeWaits(o Org, m Manager, timers []Timer) (bool, error) {
	now := c.now()
	for _, t := range timers {
		if t.Manager == m.ID && !t.Due.After(now) {
			return true, nil
		}
	}
	var events []GoalEvent
	var err error
	if g, ok := o.Goals[m.GoalID]; ok {
		events, err = c.Store.undelivered(g.ID)
	} else if m.Role == RoleChief {
		events, err = c.chiefEvents(o, m)
	}
	return err == nil && c.wakeDue(events), err
}

// bringBack brings a manager or chief that is down back: fresh from its brief when resuming would read a large transcript cold,
// else in its own conversation as relaunch does.
func (c *Core) bringBack(b Board, o Org, m Manager) Manager {
	limit, err := c.Store.ContextCap()
	if err != nil {
		return c.relaunch(b, o, m)
	}
	seen, err := c.Store.Contexts()
	if mc := seen[m.ID]; err == nil && m.Session != "" && mc.Session == m.Session && resumeCold(mc.Tokens, limit, mc.At, c.now()) {
		m, _ = c.refresh(b, o, m, Live{}, mc.Tokens, true)
		return m
	}
	return c.relaunch(b, o, m)
}

// refresh retires m's session, s when it still runs, and starts a fresh one from m's prompt, brief, plan, state and the events since
// its brief, then records the restart on m's goal, or in a chief's own feed.
func (c *Core) refresh(b Board, o Org, m Manager, s Live, tokens int64, cold bool) (Manager, error) {
	if s.Key() != "" {
		if _, err := c.Kill(s); err != nil {
			return m, err
		}
	}
	was := m.Session
	m = c.begin(b, o, m, false, &restart{tokens: tokens, cold: cold})
	if m.LastError != "" {
		return m, errors.New(m.LastError)
	}
	what := Kilo(tokens)
	if cold {
		what += " cold"
	}
	now := c.now()
	goal := m.ID
	if g, ok := o.Goals[m.GoalID]; ok {
		goal = g.ID
	}
	e, _, err := c.Store.AddGoalEvent(GoalEvent{Goal: goal, Kind: EventRefreshed, Payload: fmt.Sprintf("refreshed: %s -> fresh (session %s retired, %s started)", what, Short(was), Short(m.Session)),
		Key: "refreshed:" + was, Created: now, Delivered: now})
	if err == nil && goal == m.ID {
		err = c.Store.markHeard(m.ID, []GoalEvent{e}, now)
	}
	return m, err
}

// restart is why ym starts a manager's or chief's session fresh while it could have resumed: the context its last session read.
type restart struct {
	tokens int64
	cold   bool
}

// restartText is what a fresh session of m reads after its launch prompt: why it was restarted, its brief, where its goal stands,
// what waited for it and the events since its brief.
func (c *Core) restartText(b Board, o Org, m Manager, r restart, waiting []Message, missed []GoalEvent) string {
	var sb strings.Builder
	why := "past the " + Kilo(c.capOrDefault()) + " ym restarts managers and chiefs at"
	if r.cold {
		why += ", and its prompt cache had expired"
	}
	fmt.Fprintf(&sb, "ym restarted you fresh: your previous session %s had read %s tokens of context, %s, so ym retired it and started this one from what it knew. Your brief, your plan, where the goal stands and everything since your brief follow; pick up where the brief leaves off. Do not resume or read the old session.",
		Short(m.Session), Kilo(r.tokens), why)
	briefs, _ := c.Store.LatestBriefs()
	since := timeZero
	if br, ok := briefs[m.ID]; ok {
		since = br.CreatedAt
		fmt.Fprintf(&sb, "\n\nYour brief v%d, written %s (also at %s):", br.Version, br.CreatedAt.Local().Format(time.DateTime), m.BriefPath)
		for _, s := range br.Sections() {
			fmt.Fprintf(&sb, "\n%s: %s", s[0], s[1])
		}
	} else {
		sb.WriteString("\n\nYou have written no brief yet; write one as soon as you have your bearings.")
	}
	c.goalState(&sb, b, o, m)
	if len(waiting) > 0 {
		sb.WriteString("\n\nMessages that waited for you, oldest first:")
		for _, msg := range waiting {
			sb.WriteString("\n- " + oneLine(msg.Text))
		}
	}
	events := c.sinceBrief(o, m, since, missed)
	if len(events) > 0 {
		head := ""
		if len(events) > refreshEvents {
			head = fmt.Sprintf(" (the latest %d of %d; ym goal feed shows the rest)", refreshEvents, len(events))
			events = events[len(events)-refreshEvents:]
		}
		sb.WriteString("\n\nSince your brief" + head + ", " + wakeOrChief(o, m, events))
	}
	return sb.String()
}

func (c *Core) capOrDefault() int64 {
	if n, err := c.Store.ContextCap(); err == nil {
		return n
	}
	return ContextCap
}

// sinceBrief is what a fresh session of m has not seen: its goal's events after since and every one never handed to it, oldest
// first, or for a chief the changes under it not yet handed to it.
func (c *Core) sinceBrief(o Org, m Manager, since time.Time, missed []GoalEvent) []GoalEvent {
	g, ok := o.Goals[m.GoalID]
	if !ok {
		return missed
	}
	all, err := c.Store.GoalEvents(g.ID)
	if err != nil {
		return missed
	}
	var out []GoalEvent
	for _, e := range all {
		if e.Kind != EventRefreshed && (e.Created.After(since) || e.Delivered.IsZero()) {
			out = append(out, e)
		}
	}
	return out
}

// goalState writes where m's work stands now: its agents, as the board shows them, and its pull requests with their CI.
func (c *Core) goalState(sb *strings.Builder, b Board, o Org, m Manager) {
	_, _, sessions := c.work(b, o, m.Session)
	statuses, _ := c.Store.PRStatuses()
	var prs []PRNote
	seen := map[string]bool{}
	add := func(pr PR) {
		if seen[pr.URL] || len(prs) == refreshPRs {
			return
		}
		seen[pr.URL] = true
		if pr.Status == nil {
			if st, ok := statuses[pr.URL]; ok {
				pr.Status = &st
			}
		}
		prs = append(prs, pr.Note())
	}
	sb.WriteString("\n\nYour agents now:")
	if len(sessions) == 0 {
		sb.WriteString(" none running.")
	}
	for i, s := range sessions {
		for _, pr := range s.AllPRs() {
			add(pr)
		}
		if i >= refreshAgents {
			continue
		}
		what := s.Doing.String()
		if s.Status != Busy && s.Result != "" {
			what = s.Result
		}
		fmt.Fprintf(sb, "\n- %s (%s): %s", titleFor(s), Short(s.Key()), s.Status.String())
		if what = clip(oneLine(what), 160); what != "" {
			sb.WriteString(", " + what)
		}
	}
	if len(sessions) > refreshAgents {
		fmt.Fprintf(sb, "\n- and %d more; ym sessions lists them", len(sessions)-refreshAgents)
	}
	if tracked, err := c.Store.GoalPRs(); err == nil {
		for _, url := range tracked[m.GoalID] {
			add(PR{URL: url})
		}
	}
	if len(prs) == 0 {
		return
	}
	sb.WriteString("\n\nPull requests:")
	for _, p := range prs {
		sb.WriteString("\n- " + p.String())
	}
}

// Kilo is a token count in a few characters, such as 262k or 1.2M.
func Kilo(n int64) string {
	switch {
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%dk", (n+500)/1000)
	}
	return fmt.Sprint(n)
}
