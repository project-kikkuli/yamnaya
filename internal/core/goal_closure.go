package core

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// GoalClosure is what the newest ym goal close of a goal recorded, read back from its goal-closed event.
type GoalClosure struct {
	Disposition string
	Reason      string
	Actor       string
	PRs         []string `json:",omitempty"`
	At          time.Time
}

const closedPrefix, closedPR, closedKey = "closed as ", "open pull request: ", "closed:"

// closureKey carries who closed a goal, by id, so the name the event's line shows can be for people alone.
func closureKey(author string, at time.Time, n int64) string {
	return fmt.Sprintf("%s%s:%d:%d", closedKey, author, at.UnixNano(), n)
}

// closerOf is the id of whoever closed the goal in e, Owner for the owner. A close older than closureKey names its chief by id in its line.
func closerOf(e GoalEvent) string {
	if rest, ok := strings.CutPrefix(e.Key, closedKey); ok {
		id, _, _ := strings.Cut(rest, ":")
		return id
	}
	if id, ok := strings.CutPrefix(parseClosure(e).Actor, "chief "); ok {
		return id
	}
	return Owner
}

// closureText is a goal-closed event's line; author is the closer as people read it.
func closureText(disposition, author, why string, prs []string) string {
	by := "the owner"
	if author != Owner {
		by = "chief " + author
	}
	text := fmt.Sprintf("%s%s by %s: %s", closedPrefix, disposition, by, why)
	for _, pr := range prs {
		text += "\n" + closedPR + pr
	}
	return text
}

func parseClosure(e GoalEvent) GoalClosure {
	first, rest, _ := strings.Cut(e.Payload, "\n")
	disposition, said, _ := strings.Cut(strings.TrimPrefix(first, closedPrefix), " by ")
	actor, why, _ := strings.Cut(said, ": ")
	out := GoalClosure{Disposition: disposition, Actor: actor, Reason: why, At: e.Created}
	for line := range strings.Lines(rest) {
		if pr, ok := strings.CutPrefix(strings.TrimSpace(line), closedPR); ok {
			out.PRs = append(out.PRs, pr)
		}
	}
	return out
}

// SettleClosures rewrites each close stored with its chief's id in its line to show the chief's name, and moves the id to the event's key.
func (c *Core) SettleClosures() (int, error) {
	events, err := scanEvents(c.Store.db.Query(`SELECT `+eventCols+` FROM ym_goal_events WHERE kind = ? AND key = ''`, EventGoalClosed))
	if err != nil {
		return 0, err
	}
	settled := 0
	var errs []error
	for _, e := range events {
		author := closerOf(e)
		if author == Owner {
			continue
		}
		payload := strings.Replace(e.Payload, " by chief "+author+": ", " by chief "+c.RefName(author)+": ", 1)
		_, err := c.Store.db.Exec(`UPDATE ym_goal_events SET payload = ?, key = ? WHERE seq = ?`, payload, closureKey(author, e.Created, e.Seq), e.Seq)
		if errs = append(errs, err); err == nil {
			settled++
		}
	}
	c.Store.invalidate()
	return settled, errors.Join(errs...)
}

// standingEvents lists the goal events of kind no reopening or new work has followed, nor a newer one of kind.
func (s *Store) standingEvents(kind string) ([]GoalEvent, error) {
	return scanEvents(s.db.Query(`SELECT `+eventCols+` FROM ym_goal_events e WHERE kind = ? AND NOT EXISTS
		(SELECT 1 FROM ym_goal_events later WHERE later.goal = e.goal AND later.seq > e.seq AND later.kind IN (?, ?, ?))`,
		kind, kind, EventGoalReopened, EventWorkResumed))
}

// goalClosures maps each goal to its standing closure.
func (s *Store) goalClosures() (map[string]GoalClosure, error) {
	return remembered(s, "goal closures", cloneMap[string, GoalClosure], func() (map[string]GoalClosure, error) {
		events, err := s.standingEvents(EventGoalClosed)
		if err != nil {
			return nil, err
		}
		out := map[string]GoalClosure{}
		for _, e := range events {
			out[e.Goal] = parseClosure(e)
		}
		return out, nil
	})
}

// goalsArchived are the goals whose goal-archived event stands.
func (s *Store) goalsArchived() (map[string]bool, error) {
	return remembered(s, "goals archived", cloneMap[string, bool], func() (map[string]bool, error) {
		events, err := s.standingEvents(EventGoalArchived)
		if err != nil {
			return nil, err
		}
		out := map[string]bool{}
		for _, e := range events {
			out[e.Goal] = true
		}
		return out, nil
	})
}
