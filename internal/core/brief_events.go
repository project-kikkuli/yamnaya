package core

import (
	"database/sql"
	"strings"
)

// briefKinds are the goal events a manager's brief must not predate: what changes where the goal stands.
var briefKinds = []string{EventFinished, EventAgentStopped, EventAgentHung, EventPROpened, EventCIRed, EventCIGreen, EventReview, EventMerged, EventClosed, EventStuck,
	EventFailedVerify, EventAdopted}

// meaningfulEvents maps each manager to the newest event of its goal that a brief must not predate.
func (c *Core) meaningfulEvents() (map[string][]stamped, error) {
	args := make([]any, len(briefKinds))
	for i, k := range briefKinds {
		args[i] = k
	}
	rows, err := c.Store.db.Query(`SELECT g.manager_id, e.kind, e.payload, max(e.created) FROM ym_goal_events e
		JOIN ym_goals g ON e.goal = 'goal' || g.num WHERE e.kind IN (?`+strings.Repeat(", ?", len(briefKinds)-1)+`) GROUP BY e.goal`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]stamped{}
	for rows.Next() {
		var manager, kind, payload string
		var at sql.NullString
		if err := rows.Scan(&manager, &kind, &payload, &at); err != nil {
			return nil, err
		}
		what := kind
		if line, _, _ := strings.Cut(payload, "\n"); line != "" {
			what += ": " + clip(line, 80)
		}
		out[manager] = append(out[manager], stamped{At: parseTime(at), What: what})
	}
	return out, rows.Err()
}
