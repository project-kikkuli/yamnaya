package core

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// wakeKinds are the events that wake a manager or chief: something needs it now. Every other event waits in its inbox for the next
// wake, or for the digest.
var wakeKinds = map[string]bool{
	EventAsked: true, EventStuck: true, EventAgentStopped: true, EventAgentHung: true, EventAgentAdrift: true, EventAgentSwollen: true,
	EventResumeFailed: true, EventCIRed: true, EventConflict: true, EventReview: true, EventSteered: true, EventFailedVerify: true,
	EventTimer: true, EventNudge: true, EventMachineThrottled: true,
}

const (
	// WakeDebounce is how long ym waits after the first event that wakes a manager, so a burst reaches it as one message.
	WakeDebounce = time.Minute
	// DigestEvery is how long events that wake nobody wait before ym hands them over as one digest.
	DigestEvery = 30 * time.Minute
	// lineMax caps an event's text in a wake; ym goal event prints it whole.
	lineMax = 200
)

// WakePace is how long a wake waits for followers, and how long events that wake nobody wait for a digest.
type WakePace struct {
	Debounce, Digest time.Duration
}

// wakeDue reports whether events, oldest first, are due now: Debounce after the first that wakes, or once the oldest of the rest
// has waited Digest. Agents' replies that add nothing are never due on their own.
func (c *Core) wakeDue(events []GoalEvent) bool {
	pace := WakePace{WakeDebounce, DigestEvery}
	if c.Pace != nil {
		pace = *c.Pace
	}
	return pace.due(events, c.now())
}

func (p WakePace) due(events []GoalEvent, now time.Time) bool {
	var oldest time.Time
	for _, e := range events {
		if wakeKinds[e.Kind] {
			return !now.Before(e.Created.Add(p.Debounce))
		}
		if e.Kind != EventIdleAgain && oldest.IsZero() {
			oldest = e.Created
		}
	}
	return !oldest.IsZero() && !now.Before(oldest.Add(p.Digest))
}

// urgent reports whether any of events wakes its manager.
func urgent(events []GoalEvent) bool {
	for _, e := range events {
		if wakeKinds[e.Kind] {
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
			line = clip(label(e)+" "+line, lineMax-2)
		}
		switch {
		case e.Kind == EventIdleAgain:
			quiet++
		case wakeKinds[e.Kind]:
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
	return clip("#"+id+" "+e.Kind+": "+text, lineMax-2)
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
