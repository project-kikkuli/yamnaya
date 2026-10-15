package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// Caller is the session whose process tree contains pid.
type Caller struct {
	Harness string
	Session string
	PID     int
}

// Identify walks pid's ancestry to the nearest harness process and asks that harness for its session.
func (c *Core) Identify(pid int) (Caller, error) {
	procs, err := c.Procs()
	if err != nil {
		return Caller{}, err
	}
	for _, p := range proc.Index(procs).Ancestors(pid) {
		for _, h := range c.Harnesses {
			if !h.Owns(p) {
				continue
			}
			id := h.SessionID(p)
			if id == "" {
				return Caller{}, fmt.Errorf("found %s process %d but could not read its session id", h.Name(), p.PID)
			}
			return Caller{Harness: h.Name(), Session: id, PID: p.PID}, nil
		}
	}
	return Caller{}, fmt.Errorf("no claude or codex process above pid %d: %w", pid, ErrNotFound)
}

// Calling is the session above pid, or the one its harness names in the environment when the process table cannot be read.
func (c *Core) Calling(pid int, getenv func(string) string) (Caller, error) {
	cl, err := c.Identify(pid)
	if err != nil && !errors.Is(err, ErrNotFound) {
		if env, ok := CallerFromEnv(getenv); ok {
			return env, nil
		}
	}
	return cl, err
}

// CallerFromEnv is the session a harness names in its shells' environment, for when the process table cannot be read, as in Codex's sandbox.
func CallerFromEnv(getenv func(string) string) (Caller, bool) {
	for _, v := range []struct{ harness, key string }{{"codex", "CODEX_THREAD_ID"}, {"claude", "CLAUDE_CODE_SESSION_ID"}} {
		if id := getenv(v.key); id != "" {
			return Caller{Harness: v.harness, Session: id}, true
		}
	}
	return Caller{}, false
}

// CallerOf is the session ref names: a live session's full id or an unambiguous prefix of at least six characters, else ref itself.
// A session that continued under another id stands for the one it continued in.
func (c *Core) CallerOf(ref string) (Caller, error) {
	b, err := c.Board()
	if err != nil {
		return Caller{}, err
	}
	keys := make([]string, 0, len(b.Sessions)+len(b.moved))
	for _, s := range b.Sessions {
		keys = append(keys, s.Key())
	}
	for old := range b.moved {
		keys = append(keys, old)
	}
	id := ref
	var hits []string
	for _, k := range keys {
		if k == ref {
			hits = []string{k}
			break
		}
		if len(ref) >= 6 && strings.HasPrefix(k, ref) && !contains(hits, k) {
			hits = append(hits, k)
		}
	}
	switch len(hits) {
	case 0:
	case 1:
		id = hits[0]
	default:
		return Caller{}, fmt.Errorf("session %q is ambiguous: %s and %s", ref, hits[0], hits[1])
	}
	id = c.Resolve(id)
	if s, ok := b.Session(id); ok {
		return Caller{Harness: s.Harness, Session: s.Key(), PID: s.PID}, nil
	}
	return Caller{Session: id}, nil
}

// Target is one place a session can be opened.
type Target struct {
	Session Session
	Handle  *Handle
	App     string
	Resume  []string
}

func (t Target) String() string {
	if t.Handle != nil {
		return fmt.Sprintf("%s %s (pid %d)", t.Handle.Host, t.Handle.Label, t.Session.PID)
	}
	if t.Session.PID != 0 {
		return fmt.Sprintf("%s %s (pid %d)", t.App, strings.TrimPrefix(t.Session.TTY, "/dev/"), t.Session.PID)
	}
	return strings.Join(t.Resume, " ")
}

// Advice is what to tell the owner when ym cannot jump to the target itself.
func (t Target) Advice() string {
	resume := strings.Join(t.Resume, " ")
	switch {
	case t.Handle != nil:
		return fmt.Sprintf("runs in %s %s (pid %d); ym jumps there only when it runs inside that tmux", t.Handle.Host, t.Handle.Label, t.Session.PID)
	case t.Session.PID == 0:
		return "not running; resume with: " + resume
	}
	where := "pid " + strconv.Itoa(t.Session.PID)
	if t.Session.TTY != "" {
		where += " on " + strings.TrimPrefix(t.Session.TTY, "/dev/")
	}
	if t.App != "" {
		where += " in " + t.App
	}
	return "runs as " + where + "; switch to it there. To move it, quit it first, then: " + resume
}

// Targets lists every live copy of a session with its host; a session with no live process yields its resume command.
func (c *Core) Targets(b Board, key, harnessName string) []Target {
	var out []Target
	for _, s := range b.Sessions {
		if s.Key() != key {
			continue
		}
		for _, cp := range copiesOf(s) {
			t := Target{Session: cp, App: s.App}
			if h, host := c.locate(cp.TTY); host != nil {
				t.Handle = &h
			} else {
				t.Resume = c.resume(cp.Harness, cp.ID)
			}
			out = append(out, t)
		}
		return out
	}
	if r := c.resume(harnessName, key); r != nil {
		out = append(out, Target{Session: Session{Harness: harnessName, ID: key}, Resume: r})
	}
	return out
}

func copiesOf(s Live) []Session {
	if len(s.Copies) > 0 {
		return s.Copies
	}
	return []Session{s.Session}
}

func (c *Core) resume(harnessName, id string) []string {
	if id == "" || strings.HasPrefix(id, "pid:") {
		return nil
	}
	for _, h := range c.Harnesses {
		if h.Name() == harnessName {
			return h.Resume(id)
		}
	}
	return nil
}

// ErrNoHost means the target has no terminal ym can focus; show its resume command instead.
var ErrNoHost = errors.New("no terminal host")

// Focus brings the target's terminal forward, and the terminal showing that one when it is nested.
func (c *Core) Focus(t Target) error {
	if t.Handle == nil {
		return ErrNoHost
	}
	host := c.host(t.Handle.Host)
	if host == nil {
		return ErrNoHost
	}
	outer, err := host.Focus(*t.Handle)
	if err != nil || outer == "" {
		return err
	}
	if h, oh := c.locate(outer); oh != nil && oh.Name() != host.Name() {
		_, err = oh.Focus(h)
	}
	return err
}

// AddItem files an item against a session, in the session's scope unless it names one.
func (c *Core) AddItem(it Item) (Item, error) {
	if it.Scope == "" {
		b, _ := c.Board()
		it.Scope = c.ScopeOf(b, it.Session)
	}
	return c.Store.AddItem(it)
}

// Delivery says how an answer reached, or will reach, its agent.
type Delivery int

const (
	Queued Delivery = iota
	Typed
	// Sent went straight to the session's own inbox, which reads it after the current turn.
	Sent
	// Held reached the session's inbox, which shows it to the owner before the agent reads it.
	Held
)

func (d Delivery) String() string {
	return [...]string{"queued for the agent's next turn", "typed into the agent's terminal", "sent to the agent's session inbox", "held in the agent's session for your approval"}[d]
}

// Answer records the answer and hands it to the source agent, wherever its conversation runs now: through the session's own inbox,
// else typed in when it sits idle at its prompt in a host that accepts input.
func (c *Core) Answer(b Board, id, text string) (Item, Delivery, error) {
	it, err := c.Store.Transition(id, ItemAnswered, text)
	if err != nil {
		return it, Queued, err
	}
	d := c.deliver(b, it, fmt.Sprintf("re %s: %s", it.Spoken(), oneLine(text)))
	return it, d, nil
}

// Dismiss closes the item for good and tells the agent the way Answer does.
func (c *Core) Dismiss(b Board, id, reason string) (Item, Delivery, error) {
	it, err := c.Store.Transition(id, ItemDismissed, reason)
	if err != nil {
		return it, Queued, err
	}
	d := c.deliver(b, it, fmt.Sprintf("re %s: dismissed: %s", it.Spoken(), oneLine(reason)))
	return it, d, nil
}

func (c *Core) Snooze(id string) (Item, error) { return c.Store.Transition(id, ItemSnoozed, "") }

func (c *Core) Reopen(id string) (Item, error) { return c.Store.Transition(id, ItemOpen, "") }

func (c *Core) deliver(b Board, it Item, text string) Delivery {
	s, ok := b.Session(c.Resolve(it.Session))
	if !ok || len(s.Copies) > 1 {
		return Queued
	}
	if claimed, err := c.Store.ClaimDelivery(it.Seq); err != nil || !claimed {
		return Queued
	}
	switch err := c.message(s.Session, text, true); {
	case err == nil:
		return Sent
	case errors.Is(err, ErrPendingApproval) && c.Store.MarkHeld(it.Seq) == nil:
		return Held
	}
	if s.Status == Idle && s.Handle != nil {
		host, hn := c.host(s.Handle.Host), c.harness(s.Harness)
		if host != nil && hn != nil && Type(host, *s.Handle, hn, s.PID, text) == nil {
			return Typed
		}
	}
	c.Store.Undeliver(it.Seq)
	return Queued
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Inbox returns the answered and dismissed items the session has not received, including those filed under sessions it continues, and marks them received.
func (c *Core) Inbox(session string) ([]Item, error) {
	items, err := c.Store.Items(ItemFilter{Undelivered: true})
	if err != nil {
		return nil, err
	}
	mine := c.carries(session)
	out := []Item{}
	for _, it := range items {
		if !mine(it.Session) {
			continue
		}
		claimed, err := c.Store.ClaimDelivery(it.Seq)
		if err != nil {
			return nil, err
		}
		if claimed {
			out = append(out, it)
		}
	}
	return out, nil
}

// TakeMessages hands the session its pending messages, including those sent to sessions it continues, and marks them delivered by the inbox.
func (c *Core) TakeMessages(session string) ([]Message, error) {
	pending, err := c.Store.PendingMessages()
	if err != nil {
		return nil, err
	}
	mine := c.carries(session)
	var out []Message
	for _, m := range pending {
		if !mine(m.Session) {
			continue
		}
		ok, err := c.Store.Claim(m.Seq, MsgInbox)
		if err != nil {
			return out, err
		}
		if ok {
			m.State = MsgInbox
			out = append(out, m)
		}
	}
	return out, nil
}

// ProjectKey resolves a project by key, name, or any of its session ids.
func (b Board) ProjectKey(ref string) (string, error) {
	var hits []string
	for _, p := range b.Projects {
		match := p.Key == ref || strings.EqualFold(p.Name, ref)
		for _, s := range p.Sessions {
			match = match || s.Key() == ref || (len(ref) >= 6 && strings.HasPrefix(s.Key(), ref))
		}
		if match {
			hits = append(hits, p.Key)
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("project %q: %w", ref, ErrNotFound)
	case 1:
		return hits[0], nil
	}
	return "", fmt.Errorf("project %q is ambiguous: %s", ref, strings.Join(hits, ", "))
}
