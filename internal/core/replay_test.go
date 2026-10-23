package core

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	askedPayload = regexp.MustCompile(`^(.*?) asked (\S+) \(([^)]+)\): (.*)$`)
	finishedFrom = regexp.MustCompile(`^agent ([0-9a-f]{8})`)
	shaMention   = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
)

// TestReplayInbox replays a copy of a real ym.db, named by YM_REPLAY_DB, through the wakes ym sent before the inbox and the ones it
// sends now, and logs wakes and characters per goal. It never writes to the live database: pass a copy made with sqlite3 .backup.
func TestReplayInbox(t *testing.T) {
	path := os.Getenv("YM_REPLAY_DB")
	if path == "" {
		t.Skip("set YM_REPLAY_DB to a copy of ym.db to replay it")
	}
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	since, _ := time.Parse(time.RFC3339, "2026-10-22T00:00:00Z")
	items, err := st.Items(ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Item{}
	for _, it := range items {
		byKey[it.Key()] = it
	}
	var total [6]int
	t.Logf("%-6s %10s %10s %10s %10s %10s %10s %8s", "goal", "old wakes", "old chars", "old asks", "ask chars", "new wakes", "new chars", "dropped")
	for _, id := range []string{"goal1", "goal2", "goal3", "goal4", "goal5"} {
		g, err := st.Goal(id)
		if err != nil {
			t.Fatal(err)
		}
		all, err := st.GoalEvents(id)
		if err != nil {
			t.Fatal(err)
		}
		var events []GoalEvent
		for _, e := range all {
			if !e.Created.Before(since) {
				events = append(events, e)
			}
		}
		oldWakes, oldChars := replayOld(g, events)
		asks, askChars := 0, 0
		for _, e := range events {
			if m := askedPayload.FindStringSubmatch(e.Payload); e.Kind == EventAsked && m != nil && m[2] == g.ManagerID {
				asks++
				askChars += len(oldAskText(byKey[m[3]]))
			}
		}
		newWakes, newChars, dropped := replayNew(g, events, byKey)
		t.Logf("%-6s %10d %10d %10d %10d %10d %10d %8d", id, oldWakes, oldChars, asks, askChars, newWakes, newChars, dropped)
		for i, n := range []int{oldWakes, oldChars, asks, askChars, newWakes, newChars} {
			total[i] += n
		}
	}
	t.Logf("%-6s %10d %10d %10d %10d %10d %10d", "total", total[0], total[1], total[2], total[3], total[4], total[5])
}

// replayOld groups the events ym woke the manager with by when it did, and sizes each wake as the old wakeText wrote it.
func replayOld(g Goal, events []GoalEvent) (int, int) {
	groups := map[time.Time][]GoalEvent{}
	var order []time.Time
	for _, e := range events {
		if e.Delivered.IsZero() || e.Delivered.Equal(e.Created) {
			continue
		}
		if _, ok := groups[e.Delivered]; !ok {
			order = append(order, e.Delivered)
		}
		groups[e.Delivered] = append(groups[e.Delivered], e)
	}
	chars := 0
	for _, at := range order {
		chars += len("ym: " + oldWakeText(g, groups[at]))
	}
	return len(order), chars
}

// replayNew runs the events through today's policy: an ask to the manager wakes it, an agent's reply that adds nothing is
// idle-again, and wakes come a minute after the first urgent event or as a digest. A reply counts as having a new commit when
// it names a commit the agent never named before, since the database keeps no worktree heads.
func replayNew(g Goal, events []GoalEvent, items map[string]Item) (wakes, chars, dropped int) {
	type report struct {
		said  string
		at    time.Time
		shas  map[string]bool
		first bool
	}
	reports := map[string]*report{}
	var timeline []GoalEvent
	for _, e := range events {
		switch e.Kind {
		case EventAsked:
			m := askedPayload.FindStringSubmatch(e.Payload)
			if m == nil || m[2] != g.ManagerID {
				continue
			}
			e.Payload = fmt.Sprintf("%s ask from %s to %s: %s", m[3], m[1], m[2], m[4])
		case EventUpdate:
			continue
		case EventFinished:
			who := finishedFrom.FindStringSubmatch(e.Payload)
			_, said, _ := strings.Cut(e.Payload, "Its last message, verbatim: ")
			if who == nil {
				break
			}
			r, seen := reports[who[1]]
			if !seen {
				r = &report{shas: map[string]bool{}}
				reports[who[1]] = r
			}
			commit := false
			for _, sha := range shaMention.FindAllString(said, -1) {
				commit = commit || !r.shas[sha]
				r.shas[sha] = true
			}
			asked := false
			for _, it := range items {
				asked = asked || strings.HasPrefix(it.Session, who[1]) && it.CreatedAt.After(r.at) && !it.CreatedAt.After(e.Created)
			}
			fresh := !seen || commit || asked || saysNew(strings.TrimSpace(said), r.said)
			r.at = e.Created
			if fresh {
				r.said = strings.TrimSpace(said)
			} else {
				e.Kind = EventIdleAgain
				dropped++
			}
		}
		timeline = append(timeline, e)
	}
	pace := WakePace{WakeDebounce, DigestEvery}
	var pending []GoalEvent
	dueAt := func() (time.Time, bool) {
		for _, e := range pending {
			if wakeKinds[e.Kind] {
				return e.Created.Add(pace.Debounce), true
			}
		}
		for _, e := range pending {
			if e.Kind != EventIdleAgain {
				return e.Created.Add(pace.Digest), true
			}
		}
		return time.Time{}, false
	}
	deliver := func() {
		wakes++
		chars += len(wakeText(g, pending))
		pending = nil
	}
	end := time.Now()
	for _, e := range timeline {
		if at, ok := dueAt(); ok && !at.After(e.Created) {
			deliver()
		}
		pending = append(pending, e)
	}
	if at, ok := dueAt(); ok && at.Before(end) {
		deliver()
	}
	return wakes, chars, dropped
}

// oldWakeText is wakeText as it was before the inbox: every event's whole payload, then the standing instructions.
func oldWakeText(g Goal, events []GoalEvent) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s on goal %s %q, oldest first:", plural(len(events), "new event"), g.ID, shortName(g.Ask))
	stale := false
	for _, e := range events {
		sb.WriteString("\n- " + e.Kind + ": " + e.Payload)
		stale = stale || slices.Contains(briefKinds, e.Kind)
	}
	if stale {
		sb.WriteString("\nYour brief predates these: once you have acted, record a fresh one with ym goal brief.")
	}
	sb.WriteString("\nAct on what needs you. When the goal moves, tell the owner in one line with ym goal update \"<line>\". ym wakes you on the next event, or at a time you set with ym goal wake <when> <why>.")
	return sb.String()
}

// oldAskText is askText as it was before the inbox, without the asking agent's title, which the database does not keep.
func oldAskText(it Item) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "ym: agent %s asks you (%s %s, %s): %s", Short(it.Session), it.Key(), it.ID, it.Kind, oneLine(it.Question))
	if it.Rec != "" {
		fmt.Fprintf(&sb, " Recommended: %s.", oneLine(it.Rec))
	}
	for _, opt := range it.Options {
		fmt.Fprintf(&sb, " Option %s: %s.", opt.Label, oneLine(opt.Effect))
	}
	if it.Assumption != "" {
		fmt.Fprintf(&sb, " Context: %s", it.Assumption)
	}
	if it.Tried != "" {
		fmt.Fprintf(&sb, " Tried: %s.", oneLine(it.Tried))
	}
	if len(it.Evidence) > 0 {
		fmt.Fprintf(&sb, " Evidence: %s.", strings.Join(it.Evidence, ", "))
	}
	for _, pr := range it.Dossier.PRs {
		fmt.Fprintf(&sb, " PR: %s.", pr)
	}
	fmt.Fprintf(&sb, " You own this: answer with ym item answer '%s' <answer> (ym item go '%s' to say go ahead, ym item dismiss '%s' <reason>)."+
		" Escalate with your own ym item add only if it is beyond your authority.", it.Key(), it.Key(), it.Key())
	return sb.String()
}
