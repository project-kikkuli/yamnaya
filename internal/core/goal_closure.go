package core

import (
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

const closedPrefix, closedPR = "closed as ", "open pull request: "

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

// goalClosures maps each goal to its standing closure: a goal-closed event no reopening or new work has followed.
func (s *Store) goalClosures() (map[string]GoalClosure, error) {
	return remembered(s, "goal closures", cloneMap[string, GoalClosure], func() (map[string]GoalClosure, error) {
		events, err := scanEvents(s.db.Query(`SELECT `+eventCols+` FROM ym_goal_events e WHERE kind = ? AND NOT EXISTS
			(SELECT 1 FROM ym_goal_events later WHERE later.goal = e.goal AND later.seq > e.seq AND later.kind IN (?, ?, ?))`,
			EventGoalClosed, EventGoalClosed, EventGoalReopened, EventWorkResumed))
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
