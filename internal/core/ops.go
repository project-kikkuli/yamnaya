package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

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

// Calling uses Codex's thread environment when ancestry is missing or disagrees; Claude's environment needs an unreadable table.
func (c *Core) Calling(pid int, getenv func(string) string) (Caller, error) {
	cl, err := c.Identify(pid)
	if id := getenv("CODEX_THREAD_ID"); id != "" {
		if err == nil && cl.Harness == "codex" && cl.Session == id {
			return cl, nil
		}
		return Caller{Harness: "codex", Session: id}, nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		if env, ok := CallerFromEnv(getenv); ok {
			return env, nil
		}
	}
	return cl, err
}

// CallerFromEnv is the full session id a harness names in its shells' environment.
func CallerFromEnv(getenv func(string) string) (Caller, bool) {
	for _, v := range []struct{ harness, key string }{{"codex", "CODEX_THREAD_ID"}, {"claude", "CLAUDE_CODE_SESSION_ID"}} {
		if id := getenv(v.key); id != "" {
			return Caller{Harness: v.harness, Session: id}, true
		}
	}
	return Caller{}, false
}

// CallerOf resolves known full ids, displayed shorthands and unambiguous prefixes, else keeps ref itself.
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
	for _, l := range b.Launched {
		keys = append(keys, l.Session)
	}
	for _, p := range b.Projects {
		for _, d := range p.Dormant {
			keys = append(keys, d.Session)
		}
	}
	for session := range b.Org.Sessions {
		keys = append(keys, session)
	}
	for old := range b.moved {
		keys = append(keys, old)
	}
	id, err := SessionRef(ref, keys)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return Caller{}, err
		}
		id = ref
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
	// Remote is the host the session runs on when that is not this one.
	Remote string
}

func (t Target) String() string {
	if t.Remote != "" {
		return fmt.Sprintf("%s on %s (pid %d)", Short(t.Session.Key()), t.Remote, t.Session.PID)
	}
	if t.Handle != nil {
		return fmt.Sprintf("%s %s (pid %d)", t.Handle.Host, t.Handle.Label, t.Session.PID)
	}
	if t.Session.Background {
		return fmt.Sprintf("bg job (pid %d)", t.Session.PID)
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
	case t.Remote != "":
		return fmt.Sprintf("runs on %s; ym open %s attaches to it there over ssh", t.Remote, Short(t.Session.Key()))
	case t.Session.Background:
		return fmt.Sprintf("runs as a background job (pid %d) with no terminal ym can attach to; a answers and s sends reach it through its inbox", t.Session.PID)
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
		if s.Remote != "" {
			return []Target{{Session: s.Session, Remote: s.Remote}}
		}
		for _, cp := range copiesOf(s) {
			t := Target{Session: cp, App: s.App}
			if h, host := c.locate(cp.TTY); host != nil && !cp.Background {
				t.Handle = &h
			} else if !cp.Background {
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

// Delivery says how an answer reached, or will reach, its agent.
type Delivery int

const (
	Queued Delivery = iota
	Typed
	// Sent went straight to the session's own inbox, which reads it after the current turn.
	Sent
	// Held reached the session's inbox, which shows it to the owner before the agent reads it.
	Held
	SubmissionUncertain
)

func (d Delivery) String() string {
	return [...]string{"queued for the agent's next turn", "typed into the agent's terminal", "sent to the agent's session inbox", "held in the agent's session for your approval", "paste attempted, submission uncertain; not retyping"}[d]
}

// Answer records the answer and hands it to the source agent, wherever its conversation runs now: through the session's own inbox,
// else typed in when it sits idle at its prompt in a host that accepts input.
func (c *Core) Answer(b Board, id, text string) (Item, Delivery, error) {
	if it, d, ok, err := c.remoteItem(id, "answer", text); ok {
		return it, d, err
	}
	it, err := c.Store.Transition(id, ItemAnswered, text)
	if err != nil {
		return it, Queued, err
	}
	return it, c.deliver(b, it.Session, []Item{it}), c.ownerLooked(it)
}

// ownerLooked records the owner's look at the goal of an item the owner just settled.
func (c *Core) ownerLooked(it Item) error {
	if it.Recipient != Owner {
		return nil
	}
	return c.Looked(it.Dossier.Goal)
}

// Go answers a go item with GoAhead.
func (c *Core) Go(b Board, id string) (Item, Delivery, error) {
	if it, d, ok, err := c.remoteItem(id, "go"); ok {
		return it, d, err
	}
	it, err := c.Store.Item(id)
	if err != nil {
		return it, Queued, err
	}
	if it.Kind != KindGo {
		return it, Queued, fmt.Errorf("%s is a %s item, not a go item", it.ID, it.Kind)
	}
	return c.Answer(b, id, GoAhead)
}

// Dismiss closes the item for good and tells the agent the way Answer does.
func (c *Core) Dismiss(b Board, id, reason string) (Item, Delivery, error) {
	if it, d, ok, err := c.remoteItem(id, "dismiss", reason); ok {
		return it, d, err
	}
	it, err := c.Store.Transition(id, ItemDismissed, reason)
	if err != nil {
		return it, Queued, err
	}
	return it, c.deliver(b, it.Session, []Item{it}), c.ownerLooked(it)
}

// notice is how an answered or dismissed item reaches its agent.
func notice(it Item) string {
	if it.State == ItemDismissed {
		return fmt.Sprintf("re %s: dismissed: %s", it.Spoken(), oneLine(it.Reason))
	}
	return fmt.Sprintf("re %s: %s", it.Spoken(), oneLine(it.Answer))
}

// Redeliver hands the answered and dismissed items still queued to their agents once the session carrying them runs on the board,
// one notice per session, then types each idle session's recent pending messages; it returns how many items and messages arrived.
// Why one could not be delivered goes to c.Log each time the reason changes.
func (c *Core) Redeliver(b Board) (int, error) {
	items, err := c.Store.Items(ItemFilter{Undelivered: true})
	if err != nil {
		return 0, err
	}
	o, err := c.LoadOrg()
	if err != nil {
		return 0, err
	}
	bySession := map[string][]Item{}
	var order []string
	for _, it := range items {
		s, ok := b.Session(c.carrier(o, it.Session))
		if !ok {
			continue
		}
		if _, seen := bySession[s.Key()]; !seen {
			order = append(order, s.Key())
		}
		bySession[s.Key()] = append(bySession[s.Key()], it)
	}
	n := 0
	for _, key := range order {
		d, why := c.deliverWhy(b, key, bySession[key])
		var ids []string
		for _, it := range bySession[key] {
			ids = append(ids, it.Spoken())
		}
		c.noteUndelivered("answer to "+strings.Join(ids, ", ")+" for "+Short(key), why)
		if d != Queued {
			n += len(bySession[key])
		}
	}
	m, err := c.typePending(b, o)
	return n + m, err
}

// pendingFresh is how old a pending message may be for ym serve to type it in; older ones wait for ym item inbox.
const pendingFresh = time.Hour

// typePending types each idle session's pending messages from the last pendingFresh into its empty input box, all in one paste.
func (c *Core) typePending(b Board, o Org) (int, error) {
	msgs, err := c.Store.PendingMessages()
	if err != nil {
		return 0, err
	}
	now := c.now()
	bySession := map[string][]Message{}
	var order []string
	for _, m := range msgs {
		if now.Sub(m.CreatedAt) > pendingFresh {
			continue
		}
		s, ok := b.Session(c.carrier(o, m.Session))
		if !ok {
			continue
		}
		if _, seen := bySession[s.Key()]; !seen {
			order = append(order, s.Key())
		}
		bySession[s.Key()] = append(bySession[s.Key()], m)
	}
	n := 0
	for _, key := range order {
		s, _ := b.Session(key)
		why := c.typeMessages(s, bySession[key])
		c.noteUndelivered(fmt.Sprintf("%d messages for %s", len(bySession[key]), Short(key)), why)
		if why == "" {
			n += len(bySession[key])
		}
	}
	return n, nil
}

// typeMessages types msgs into s when it sits idle at an empty prompt, and says why not otherwise.
func (c *Core) typeMessages(s Live, msgs []Message) string {
	if s.Status != Idle {
		return ""
	}
	if s.Handle == nil {
		return "no terminal ym can type into"
	}
	host, hn := c.host(s.Handle.Host), c.harness(s.Harness)
	if host == nil || hn == nil {
		return "no terminal ym can type into"
	}
	if box, ok := readComposer(host, *s.Handle, hn, s.PID, true); !ok || strings.TrimSpace(box) != "" {
		return ""
	}
	var claimed []Message
	var texts []string
	for _, m := range msgs {
		if ok, err := c.Store.Claim(m.Seq, MsgUncertain); err == nil && ok {
			claimed = append(claimed, m)
			texts = append(texts, m.Text)
		}
	}
	if len(claimed) == 0 {
		return ""
	}
	if err := Type(host, *s.Handle, hn, s.PID, strings.Join(texts, "\n\n")); err != nil {
		if !errors.Is(err, ErrSubmissionUncertain) {
			for _, m := range claimed {
				c.Store.Unclaim(m.Seq)
			}
		}
		return "typing failed: " + err.Error()
	}
	for _, m := range claimed {
		if err := c.Store.FinishTyping(m.Seq, MsgTyped); err != nil {
			return err.Error()
		}
	}
	return ""
}

// noteUndelivered logs why what is queued stays undelivered whenever the reason changes; an empty why clears it.
func (c *Core) noteUndelivered(what, why string) {
	key := what
	c.undeliveredMu.Lock()
	defer c.undeliveredMu.Unlock()
	if c.undelivered == nil {
		c.undelivered = map[string]string{}
	}
	if c.undelivered[key] == why {
		return
	}
	if why == "" {
		delete(c.undelivered, key)
		return
	}
	c.undelivered[key] = why
	if c.Log != nil {
		c.Log(fmt.Sprintf("%s not delivered: %s", what, why))
	}
}

func (c *Core) Snooze(id string) (Item, error) {
	if it, _, ok, err := c.remoteItem(id, "snooze"); ok {
		it.State = ItemSnoozed
		return it, err
	}
	return c.Store.Transition(id, ItemSnoozed, "")
}

func (c *Core) Reopen(id string) (Item, error) {
	if it, _, ok, err := c.remoteItem(id, "reopen"); ok {
		it.State = ItemOpen
		return it, err
	}
	return c.Store.Transition(id, ItemOpen, "")
}

// deliver hands the items' notices to session as one message, claiming each first so no other delivery repeats them.
func (c *Core) deliver(b Board, session string, items []Item) Delivery {
	d, _ := c.deliverWhy(b, session, items)
	return d
}

// deliverWhy is deliver, with why the items stay queued when they do and the agent is not simply busy.
func (c *Core) deliverWhy(b Board, session string, items []Item) (Delivery, string) {
	if o, err := c.LoadOrg(); err == nil {
		session = c.carrier(o, session)
	}
	s, ok := b.Session(c.Resolve(session))
	if !ok {
		return Queued, ""
	}
	if len(s.Copies) > 1 {
		return Queued, "its conversation runs in several processes"
	}
	var claimed []Item
	var notices []string
	for _, it := range items {
		if ok, err := c.Store.ClaimDelivery(it.Seq); err == nil && ok {
			claimed = append(claimed, it)
			notices = append(notices, notice(it))
		}
	}
	if len(claimed) == 0 {
		return Queued, ""
	}
	text := strings.Join(notices, "; ")
	owner := true
	for _, it := range claimed {
		owner = owner && it.Recipient == Owner
	}
	err := c.message(s.Session, text, owner)
	switch {
	case err == nil:
		return Sent, ""
	case errors.Is(err, ErrPendingApproval):
		for _, it := range claimed {
			c.Store.MarkHeld(it.Seq)
		}
		return Held, ""
	}
	why := ""
	if s.Status == Idle {
		why = "no terminal ym can type into"
		if s.Handle != nil {
			host, hn := c.host(s.Handle.Host), c.harness(s.Harness)
			if host != nil && hn != nil {
				terr := Type(host, *s.Handle, hn, s.PID, text)
				if terr == nil {
					return Typed, ""
				}
				why = "typing failed: " + terr.Error()
				if errors.Is(terr, ErrSubmissionUncertain) {
					return SubmissionUncertain, why
				}
			}
		}
		why += "; its inbox: " + err.Error()
	}
	for _, it := range claimed {
		c.Store.Undeliver(it.Seq)
	}
	return Queued, why
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
			match = match || SessionMatches(s.Key(), ref)
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
