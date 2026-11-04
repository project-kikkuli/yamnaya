package core

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// wakeKinds are the kinds of event that wake a manager or chief: something needs it now. GoalEvent.wakes judges one event; an agent's
// reply with news wakes after ReplyDebounce, and every other event waits in its inbox for the next wake, or for the digest.
var wakeKinds = map[string]bool{
	EventAsked: true, EventStuck: true, EventAgentStopped: true, EventAgentHung: true, EventAgentAdrift: true, EventAgentSwollen: true,
	EventAgentOnboarding: true, EventResumeFailed: true, EventCIRed: true, EventConflict: true, EventReview: true, EventSteered: true, EventFailedVerify: true,
	EventTimer: true, EventNudge: true, EventRelayFailed: true,
}

// wakes reports whether the event needs its reader within a debounce: a wake kind, or the terminal report a manager must accept. A plain
// approval and a review count with nothing unresolved need nobody.
func (e GoalEvent) wakes() bool {
	if e.terminalReport() {
		return true
	}
	return wakeKinds[e.Kind] && !e.quietReview()
}

func (e GoalEvent) terminalReport() bool {
	return e.Kind == EventFinished && strings.HasPrefix(e.Key, assignmentReportKey)
}

func (e GoalEvent) quietReview() bool {
	return e.Kind == EventReview && (strings.HasSuffix(e.Payload, " review: approved") || strings.HasSuffix(e.Payload, ", 0 unresolved"))
}

// agentReply reports whether the event is an agent's idle reply with news, which wakes once after ReplyDebounce.
func (e GoalEvent) agentReply() bool {
	return e.Kind == EventFinished && !e.terminalReport()
}

// wholeKinds are the events a wake shows whole: a failure's cause and a relayed command's outcome are lost when cut.
var wholeKinds = map[string]bool{EventResumeFailed: true, EventRelayed: true, EventRelayFailed: true}

const (
	// WakeDebounce is how long ym waits after the first event that wakes a manager, so a burst reaches it as one message.
	WakeDebounce = time.Minute
	// ReplyDebounce is how long an agent's reply with news waits for followers before it wakes its manager.
	ReplyDebounce = 10 * time.Minute
	// DigestEvery is how long events that wake nobody wait before ym hands them over as one digest.
	DigestEvery = 30 * time.Minute
	// lineMax caps an event's text in a wake; ym goal event prints it whole.
	lineMax = 200
)

// WakePace is how long a wake waits for followers, how long an agent's reply waits for them, and how long events that wake nobody
// wait for a digest.
type WakePace struct {
	Debounce, Reply, Digest time.Duration
}

// wakeDue reports whether events, oldest first, are due now: Debounce after the first that wakes, else Reply after the first agent's
// reply with news, or once the oldest of the rest has waited Digest. Agents' replies that add nothing are never due on their own.
func (c *Core) wakeDue(events []GoalEvent) bool {
	return c.pace().due(events, c.now())
}

func (p WakePace) due(events []GoalEvent, now time.Time) bool {
	at, ok := p.dueAt(events)
	return ok && !now.Before(at)
}

// dueAt is when events, oldest first, are due, and whether they ever are.
func (p WakePace) dueAt(events []GoalEvent) (time.Time, bool) {
	var oldest, reply time.Time
	for _, e := range events {
		if e.wakes() {
			return e.Created.Add(p.Debounce), true
		}
		if e.agentReply() && reply.IsZero() {
			reply = e.Created
		}
		if e.Kind != EventIdleAgain && oldest.IsZero() {
			oldest = e.Created
		}
	}
	switch {
	case oldest.IsZero():
		return oldest, false
	case !reply.IsZero() && reply.Add(p.Reply).Before(oldest.Add(p.Digest)):
		return reply.Add(p.Reply), true
	}
	return oldest.Add(p.Digest), true
}

// urgent reports whether any of events will wake its manager by itself before a digest would.
func urgent(events []GoalEvent) bool {
	for _, e := range events {
		if e.wakes() || e.agentReply() {
			return true
		}
	}
	return false
}

// inboxText is a wake: one line per event, those that need the reader first, then how many agents' replies added nothing. host
// names this host to a reader on another one, "" otherwise; label names each event's goal for a chief, nil for a manager.
func inboxText(head string, events []GoalEvent, host string, label func(GoalEvent) string) string {
	var now, later []string
	quiet := 0
	for _, e := range events {
		line := eventLine(e, host)
		if label != nil {
			line = label(e) + " " + line
			if !wholeKinds[e.Kind] {
				line = fitLine(e, line)
			}
		}
		switch {
		case e.Kind == EventIdleAgain:
			quiet++
		case e.wakes() || e.agentReply():
			now = append(now, line)
		default:
			later = append(later, line)
		}
	}
	var sb strings.Builder
	sb.WriteString("ym: " + head)
	switch {
	case len(now) > 0:
		fmt.Fprintf(&sb, ", %d for you", len(now))
		if len(later) > 0 {
			fmt.Fprintf(&sb, ", %d more", len(later))
		}
	case len(later) > 0:
		fmt.Fprintf(&sb, ", digest of %d", len(later))
	}
	sb.WriteString(":")
	for _, l := range append(now, later...) {
		sb.WriteString("\n- " + l)
	}
	if quiet > 0 {
		fmt.Fprintf(&sb, "\n(%s with nothing new left out)", plural(quiet, "agent reply"))
	}
	return sb.String()
}

// eventLine is an event in one line: its number, kind and the start of its text.
func eventLine(e GoalEvent, host string) string {
	id := strconv.FormatInt(e.Seq, 10)
	if host != "" {
		id = RemoteKey(host, id)
	}
	text := strings.Replace(oneLine(e.Payload), " is idle. Its last message, verbatim: ", ": ", 1)
	line := "#" + id + " " + e.Kind + ": " + text
	if wholeKinds[e.Kind] {
		return line
	}
	return fitLine(e, line)
}

// fitLine cuts an event's line to the cap; an ask keeps the key at its end that answers it.
func fitLine(e GoalEvent, line string) string {
	if i := strings.LastIndex(line, " "); e.Kind == EventAsked && i >= 0 {
		return clip(line[:i], lineMax-2-utf8.RuneCountInString(line[i:])) + line[i:]
	}
	return clip(line, lineMax-2)
}

// GoalEvent is the event numbered seq.
func (s *Store) GoalEvent(seq int64) (GoalEvent, error) {
	events, err := scanEvents(s.db.Query(`SELECT `+eventCols+` FROM ym_goal_events WHERE seq = ?`, seq))
	if err != nil {
		return GoalEvent{}, err
	}
	if len(events) == 0 {
		return GoalEvent{}, fmt.Errorf("no event #%d", seq)
	}
	return events[0], nil
}

// lastReport is what ym last noted of an agent's idle replies: its worktree's HEAD and when, and the last reply it handed on.
type lastReport struct {
	Head, Said string
	At         time.Time
}

func (s *Store) lastReport(session string) (lastReport, bool, error) {
	var r lastReport
	var at sql.NullString
	err := s.db.QueryRow(`SELECT head, said, at FROM ym_agent_reports WHERE session = ?`, session).Scan(&r.Head, &r.Said, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	r.At = parseTime(at)
	return r, err == nil, err
}

func (s *Store) noteReport(session string, r lastReport) error {
	_, err := s.db.Exec(`INSERT INTO ym_agent_reports (session, head, said, at) VALUES (?, ?, ?, ?)
		ON CONFLICT (session) DO UPDATE SET head = excluded.head, said = excluded.said, at = excluded.at`, session, r.Head, r.Said, stamp(r.At))
	return err
}
