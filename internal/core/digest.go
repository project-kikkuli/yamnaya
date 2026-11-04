package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const digestSchema = `
CREATE TABLE IF NOT EXISTS ym_digests (
	manager TEXT PRIMARY KEY,
	action TEXT NOT NULL DEFAULT '',
	sent TEXT NOT NULL DEFAULT '',
	quiet TEXT NOT NULL DEFAULT '',
	told_at TEXT,
	summed_at TEXT
) WITHOUT ROWID;
`

// digestHead starts every chief's digest; a pending message with it takes new content instead of a second message.
const digestHead = "ym: changes under you"

// ChiefBatch is how long what needs a chief from below it waits for followers, so one message carries a burst across its goals.
const ChiefBatch = 15 * time.Minute

// deathGrace is how long a stopped agent has to come back before its chief hears it died.
const deathGrace = DigestEvery

// ownAgents labels a chief's own agents in its digest.
const ownAgents = "your agents"

// GoalDigest is one goal's line in a digest: what needs the chief, and how many of each kind of event do not.
type GoalDigest struct {
	Actions []string       `json:"actions,omitempty"`
	Counts  map[string]int `json:"counts,omitempty"`
}

// Rollup maps a goal's label to its line.
type Rollup map[string]GoalDigest

func (r Rollup) merged(more Rollup) Rollup {
	out := Rollup{}
	for _, from := range []Rollup{r, more} {
		for label, d := range from {
			got := out[label]
			for _, a := range d.Actions {
				if !slices.Contains(got.Actions, a) {
					got.Actions = append(got.Actions, a)
				}
			}
			for kind, n := range d.Counts {
				if got.Counts == nil {
					got.Counts = map[string]int{}
				}
				got.Counts[kind] += n
			}
			out[label] = got
		}
	}
	return out
}

// countNouns names a kind in a count where its name does not read as a noun.
var countNouns = map[string]string{
	EventFinished: "agent report", EventWorkResumed: "work resume", EventResumed: "agent resume", EventSteered: "steer", EventAdopted: "adoption",
	EventVerifying: "verify run", EventCIGreen: "green CI run", EventCIRed: "red CI run", EventPROpened: "opened PR", EventMerged: "merged PR",
	EventClosed: "closed PR", EventAgentStopped: "agent stop", EventAgentHung: "hung agent", EventAgentAdrift: "adrift agent", EventAgentSwollen: "swollen agent", EventAgentOnboarding: "onboarding agent", EventAgentLimited: "agent at its limit",
	EventStuck: "stuck repeat", EventFailedVerify: "failed verify repeat",
}

func countText(kind string, n int) string {
	noun, ok := countNouns[kind]
	if !ok {
		noun = kind
	}
	return plural(n, noun)
}

// lines is the digest's body: one line per goal, those that need the chief first.
func (r Rollup) lines() []string {
	labels := make([]string, 0, len(r))
	for label := range r {
		labels = append(labels, label)
	}
	slices.SortFunc(labels, func(a, b string) int {
		if acts := len(r[a].Actions) > 0; acts != (len(r[b].Actions) > 0) {
			if acts {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		d := r[label]
		parts := slices.Clone(d.Actions)
		kinds := make([]string, 0, len(d.Counts))
		for kind := range d.Counts {
			kinds = append(kinds, kind)
		}
		slices.Sort(kinds)
		for _, kind := range kinds {
			parts = append(parts, countText(kind, d.Counts[kind]))
		}
		out = append(out, label+": "+strings.Join(parts, ", "))
	}
	return out
}

// text is the digest as one message.
func (r Rollup) text() string {
	lines := r.lines()
	if len(lines) == 0 {
		return digestHead + ": nothing new."
	}
	var sb strings.Builder
	sb.WriteString(digestHead + ":\n" + strings.Join(lines, "\n"))
	if strings.Contains(sb.String(), "#") {
		sb.WriteString("\nym goal event <n> prints one; ym digest prints what has not reached you.")
	}
	return sb.String()
}

var prNumber = regexp.MustCompile(`https?://[^/\s]+/([^/\s]+/[^/\s]+)/pull/(\d+)`)

// actionText names an event that needs a chief, by the number ym goal event prints it with; a ready pull request by its repository and
// its own number instead, so a new head of the same one stays one action.
func actionText(e GoalEvent, host string) string {
	id := strconv.FormatInt(e.Seq, 10)
	if host != "" {
		id = RemoteKey(host, id)
	}
	switch e.Kind {
	case EventPRReady:
		if ms := prNumber.FindAllStringSubmatch(e.Payload, -1); ms != nil {
			m := ms[len(ms)-1]
			return m[1] + " PR " + m[2] + " ready"
		}
		return "PR ready #" + id
	case EventFailedVerify:
		return "failed verify #" + id
	case EventResumeFailed, EventAgentStopped:
		return "agent died #" + id
	case EventRelayFailed, EventRelayed, EventGoalClosed:
		return eventLine(e, host)
	case EventFinished:
		return "agent finished #" + id
	case EventCIRed:
		return "CI red #" + id
	case EventAsked:
		return "ask #" + id
	}
	return e.Kind + " #" + id
}

// chiefSort is a chief's pending events split by whether each needs it now. Events still settling are in neither: they wait to be judged.
type chiefSort struct {
	action, quiet []GoalEvent
}

// sortChief judges events for chief: what needs its action, and what it may read when it likes.
func (c *Core) sortChief(chief string, events []GoalEvent) (chiefSort, error) {
	history := map[string][]GoalEvent{}
	past := func(goal string) ([]GoalEvent, error) {
		if h, ok := history[goal]; ok {
			return h, nil
		}
		h, err := c.Store.GoalEvents(goal)
		history[goal] = h
		return h, err
	}
	var out chiefSort
	for _, e := range events {
		switch e.Kind {
		case EventIdleAgain:
			out.quiet = append(out.quiet, e)
			continue
		case EventDone, EventVerified, EventPRReady, EventResumeFailed, EventRelayFailed, EventRelayed:
			out.action = append(out.action, e)
			continue
		case EventGoalClosed:
			if closerOf(e) == chief {
				out.quiet = append(out.quiet, e)
			} else {
				out.action = append(out.action, e)
			}
			continue
		case EventStuck, EventFailedVerify, EventAgentStopped:
		default:
			if e.Goal == chief && (wakeKinds[e.Kind] || e.Kind == EventFinished) || managerAdrift(e) {
				out.action = append(out.action, e)
			} else {
				out.quiet = append(out.quiet, e)
			}
			continue
		}
		h, err := past(e.Goal)
		if err != nil {
			return out, err
		}
		switch e.Kind {
		case EventStuck:
			if stuckBefore(h, e) {
				out.quiet = append(out.quiet, e)
			} else {
				out.action = append(out.action, e)
			}
		case EventFailedVerify:
			if strings.Contains(e.Payload, " withdrawn: ") || failedBefore(h, e) {
				out.quiet = append(out.quiet, e)
			} else {
				out.action = append(out.action, e)
			}
		case EventAgentStopped:
			switch {
			case strings.Contains(e.Payload, "exited with status 0") || cameBack(h, e):
				out.quiet = append(out.quiet, e)
			case c.now().Before(e.Created.Add(deathGrace)):
			default:
				out.action = append(out.action, e)
			}
		}
	}
	return out, nil
}

// managerAdrift reports whether what is going nowhere is the goal's manager, which only a chief can step in on.
func managerAdrift(e GoalEvent) bool {
	return (e.Kind == EventAgentAdrift || e.Kind == EventAgentSwollen) && strings.HasPrefix(e.Payload, "manager ")
}

// movedKinds are the events that show a goal moved, so that it getting stuck again is news.
var movedKinds = map[string]bool{
	EventUpdate: true, EventFinished: true, EventPROpened: true, EventPRReady: true, EventCIGreen: true, EventMerged: true, EventVerifying: true, EventAdopted: true,
}

// stuckBefore reports whether the goal already got stuck before e and has not moved since.
func stuckBefore(history []GoalEvent, e GoalEvent) bool {
	stuck := false
	for _, h := range history {
		if h.Seq >= e.Seq {
			break
		}
		switch {
		case h.Kind == EventStuck:
			stuck = true
		case movedKinds[h.Kind]:
			stuck = false
		}
	}
	return stuck
}

var attemptWord = regexp.MustCompile(`^attempt \d+ `)

// failedFindings is a failed verification's findings, without its attempt or what it tells the manager to do.
func failedFindings(payload string) string {
	if _, list, ok := strings.Cut(payload, "\n  - "); ok {
		return strings.TrimSuffix(list, failedVerifyEnd)
	}
	return attemptWord.ReplaceAllString(payload, "")
}

// failedBefore reports whether an earlier failed verification of the goal had e's findings.
func failedBefore(history []GoalEvent, e GoalEvent) bool {
	cause := failedFindings(e.Payload)
	for _, h := range history {
		if h.Seq >= e.Seq {
			break
		}
		if h.Kind == EventFailedVerify && failedFindings(h.Payload) == cause {
			return true
		}
	}
	return false
}

// cameBack reports whether the agent e says stopped did anything after it.
func cameBack(history []GoalEvent, e GoalEvent) bool {
	fields := strings.Fields(e.Payload)
	if len(fields) < 2 {
		return false
	}
	agent := "agent " + fields[1] + " "
	for _, h := range history {
		if h.Seq > e.Seq && h.Kind != EventResumeFailed && strings.HasPrefix(h.Payload, agent) {
			return true
		}
	}
	return false
}

func (c *Core) pace() WakePace {
	if c.Pace != nil {
		return *c.Pace
	}
	return WakePace{WakeDebounce, DigestEvery}
}

// actionDue reports whether any of chief's events that need it has waited long enough: what its own agents did a debounce, and what
// happened below it long enough for its reports' news to arrive together, since they keep working meanwhile.
func (c *Core) actionDue(s chiefSort, chief string) bool {
	now, batch := c.now(), ChiefBatch
	if c.Pace != nil {
		batch = c.Pace.Debounce
	}
	for _, e := range s.action {
		wait := batch
		if e.Goal == chief {
			wait = c.pace().Debounce
		}
		if !now.Before(e.Created.Add(wait)) {
			return true
		}
	}
	return false
}

// quietDue reports whether the oldest event that wakes nobody has waited long enough to roll up.
func (s chiefSort) quietDue(p WakePace, now time.Time) bool {
	return len(s.quiet) > 0 && !now.Before(s.quiet[0].Created.Add(p.Digest))
}

// chiefLabel names each event's goal for a chief, here or, with host, on another host.
func chiefLabel(o Org, host string) func(GoalEvent) string {
	return func(e GoalEvent) string {
		g, ok := o.Goals[e.Goal]
		if !ok {
			return ownAgents
		}
		if host != "" {
			return RemoteKey(host, g.Shown())
		}
		return g.Ref()
	}
}

func actionRollup(events []GoalEvent, label func(GoalEvent) string, host string) Rollup {
	out := Rollup{}
	for _, e := range events {
		d := out[label(e)]
		if a := actionText(e, host); !slices.Contains(d.Actions, a) {
			d.Actions = append(d.Actions, a)
		}
		out[label(e)] = d
	}
	return out
}

func quietRollup(events []GoalEvent, label func(GoalEvent) string) Rollup {
	out := Rollup{}
	for _, e := range events {
		if e.Kind == EventIdleAgain {
			continue
		}
		d := out[label(e)]
		if d.Counts == nil {
			d.Counts = map[string]int{}
		}
		d.Counts[e.Kind]++
		out[label(e)] = d
	}
	return out
}

// digestRow is what a chief's digest holds: Action waits to be handed over and wakes it, Sent is in a message not yet taken, and
// Quiet never wakes it.
type digestRow struct {
	Action, Sent, Quiet Rollup
	ToldAt, SummedAt    time.Time
}

func rollupOf(s string) (Rollup, error) {
	r := Rollup{}
	if s == "" {
		return r, nil
	}
	return r, json.Unmarshal([]byte(s), &r)
}

func (s *Store) digest(manager string) (digestRow, error) {
	var action, sent, quiet string
	var told, summed sql.NullString
	var r digestRow
	err := s.db.QueryRow(`SELECT action, sent, quiet, told_at, summed_at FROM ym_digests WHERE manager = ?`, manager).Scan(&action, &sent, &quiet, &told, &summed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return r, err
	}
	r.ToldAt, r.SummedAt = parseTime(told), parseTime(summed)
	var errA, errS, errQ error
	r.Action, errA = rollupOf(action)
	r.Sent, errS = rollupOf(sent)
	r.Quiet, errQ = rollupOf(quiet)
	return r, errors.Join(errA, errS, errQ)
}

func (s *Store) saveDigest(manager string, r digestRow) error {
	encode := func(x Rollup) (string, error) {
		if len(x) == 0 {
			return "", nil
		}
		b, err := json.Marshal(x)
		return string(b), err
	}
	action, errA := encode(r.Action)
	sent, errS := encode(r.Sent)
	quiet, errQ := encode(r.Quiet)
	if err := errors.Join(errA, errS, errQ); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO ym_digests (manager, action, sent, quiet, told_at, summed_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (manager) DO UPDATE SET action = excluded.action, sent = excluded.sent, quiet = excluded.quiet, told_at = excluded.told_at, summed_at = excluded.summed_at`,
		manager, action, sent, quiet, stamp(r.ToldAt), stamp(r.SummedAt))
	return err
}

// addDigest merges more into what waits for manager.
func (s *Store) addDigest(manager string, action, quiet Rollup) error {
	r, err := s.digest(manager)
	if err != nil {
		return err
	}
	r.Action, r.Quiet = r.Action.merged(action), r.Quiet.merged(quiet)
	return s.saveDigest(manager, r)
}

// pendingDigest is the seq of the digest still waiting for session, 0 when none is.
func (s *Store) pendingDigest(session string) (int64, error) {
	var seq int64
	err := s.db.QueryRow(`SELECT seq FROM ym_messages WHERE session = ? AND state = ? AND text LIKE ? ORDER BY seq DESC LIMIT 1`, session, MsgPending, digestHead+"%").Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}

func (s *Store) rewriteMessage(seq int64, text string) error {
	_, err := s.db.Exec(`UPDATE ym_messages SET text = ? WHERE seq = ? AND state = ?`, text, seq, MsgPending)
	return err
}

// busyTold reports whether told already reached s in the turn it is taking, or s cannot take a message now. A hung session is taking
// no turn, and no event but this message reaches a hung manager.
func (c *Core) busyTold(s Live, told time.Time) bool {
	return s.Status == Waiting || s.Status == Onboarding || s.Prompt != nil || s.Status == Busy && !s.Hung(c.now(), c.Woke) && told.After(s.Since)
}

// flushDigest hands a chief what waits for it. New content goes into a digest still pending; a session mid-turn gets at most one,
// so anything later waits here for its next message, and only what needs the chief ever starts a message. Quiet content rides
// along at most every Digest.
func (c *Core) flushDigest(b Board, m Manager) error {
	r, err := c.Store.digest(m.ID)
	if err != nil || len(r.Action) == 0 {
		return err
	}
	s, ok := c.reach(b, m.Session)
	if !ok {
		return nil
	}
	seq, err := c.Store.pendingDigest(m.Session)
	if err != nil {
		return err
	}
	if seq != 0 {
		r.Sent, r.Action = r.Sent.merged(r.Action), Rollup{}
		if err := c.Store.rewriteMessage(seq, c.Store.Nicked(r.Sent.text())); err != nil {
			return err
		}
		return c.Store.saveDigest(m.ID, r)
	}
	if c.busyTold(s, r.ToldAt) {
		return nil
	}
	body := r.Action
	if now := c.now(); len(r.Quiet) > 0 && !now.Before(r.SummedAt.Add(c.pace().Digest)) {
		body, r.Quiet, r.SummedAt = body.merged(r.Quiet), Rollup{}, now
	}
	text := body.text()
	if err := c.tell(b, m.Session, text, false); err != nil {
		return err
	}
	r.Action, r.Sent, r.ToldAt = Rollup{}, Rollup{}, c.now()
	if seq, err = c.Store.pendingDigest(m.Session); err != nil {
		return err
	}
	if seq != 0 {
		r.Sent = body
	}
	return c.Store.saveDigest(m.ID, r)
}

// Digest is the calling manager's or chief's changes it has not been handed, one line per goal; a chief's are handed by this.
func (c *Core) Digest(session string) ([]string, error) {
	return c.chiefChanges(session, true)
}

// ChangesDue is Digest when the last summary is at least a digest period old, and nothing otherwise: ym item inbox, run during a
// turn, reads it, so these never start one.
func (c *Core) ChangesDue(session string) ([]string, error) {
	return c.chiefChanges(session, false)
}

func (c *Core) chiefChanges(session string, force bool) ([]string, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return nil, err
	}
	m, ok := o.ManagerOf(c.Resolve(session))
	if !ok {
		if force {
			return nil, fmt.Errorf("session %s is not a manager or chief; only they have digests", Short(session))
		}
		return nil, nil
	}
	if g, ok := o.Goals[m.GoalID]; ok {
		if !force {
			return nil, nil
		}
		events, err := c.Store.undelivered(g.ID)
		if err != nil {
			return nil, err
		}
		return quietRollup(events, func(GoalEvent) string { return g.Shown() }).lines(), nil
	}
	if m.Role != RoleChief {
		return nil, nil
	}
	r, err := c.Store.digest(m.ID)
	if err != nil {
		return nil, err
	}
	now := c.now()
	if !force && now.Before(r.SummedAt.Add(c.pace().Digest)) {
		return nil, nil
	}
	events, err := c.chiefEvents(o, m)
	if err != nil {
		return nil, err
	}
	split, err := c.sortChief(m.ID, events)
	if err != nil {
		return nil, err
	}
	r.Quiet = r.Quiet.merged(quietRollup(split.quiet, chiefLabel(o, "")))
	lines := r.Quiet.lines()
	if len(lines) == 0 && !force {
		if err := c.Store.markHeard(m.ID, split.quiet, now); err != nil {
			return nil, err
		}
		return nil, nil
	}
	r.Quiet, r.SummedAt = Rollup{}, now
	if err := c.Store.saveDigest(m.ID, r); err != nil {
		return nil, err
	}
	return lines, c.Store.markHeard(m.ID, split.quiet, now)
}
