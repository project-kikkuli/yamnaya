package core

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const paceSchema = `
CREATE TABLE IF NOT EXISTS ym_pr_pace (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	left_share REAL NOT NULL,
	reset_at TEXT NOT NULL,
	failed TEXT NOT NULL DEFAULT '',
	at TEXT NOT NULL
);
`

// PRPace is how the last RefreshPRs read pull requests: the share of GitHub's budget left and when it resets, and why its first
// failed batch failed.
type PRPace struct {
	Left   float64
	Reset  time.Time
	Failed string
	At     time.Time
}

// paceFresh is how long a recorded failure stays on the board without a newer cycle confirming it.
const paceFresh = 10 * time.Minute

// Note says how pull request reads are slowed, stopped or failing at now, "" while they run at full pace.
func (p PRPace) Note(now time.Time) string {
	if p.At.IsZero() {
		return ""
	}
	if p.Failed != "" && now.Sub(p.At) < paceFresh {
		return "pull request reads failing: " + p.Failed
	}
	if !now.Before(p.Reset) || p.Left >= budgetFull {
		return ""
	}
	budget := fmt.Sprintf("GitHub budget %d%% left until %s", int(math.Round(p.Left*100)), p.Reset.Local().Format("15:04"))
	goal, board := prStale(p.Left, true), prStale(p.Left, false)
	if goal == 0 {
		return "pull request reads stopped: " + budget
	}
	if board == goal {
		return fmt.Sprintf("pull requests read every %s: %s", minutes(board), budget)
	}
	return fmt.Sprintf("pull requests read every %s, goals' every %s: %s", minutes(board), minutes(goal), budget)
}

func minutes(d time.Duration) string {
	return fmt.Sprintf("%dm", int(d.Round(time.Minute)/time.Minute))
}

func (s *Store) SavePRPace(p PRPace) error {
	_, err := s.db.Exec(`INSERT INTO ym_pr_pace (id, left_share, reset_at, failed, at) VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET left_share = excluded.left_share, reset_at = excluded.reset_at, failed = excluded.failed, at = excluded.at`,
		p.Left, p.Reset.UTC().Format(time.RFC3339Nano), p.Failed, p.At.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) PRPace() (PRPace, error) {
	var p PRPace
	var reset, at string
	err := s.db.QueryRow(`SELECT left_share, reset_at, failed, at FROM ym_pr_pace WHERE id = 1`).Scan(&p.Left, &reset, &p.Failed, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return PRPace{}, nil
	}
	if err != nil {
		return PRPace{}, err
	}
	p.Reset, _ = time.Parse(time.RFC3339Nano, reset)
	p.At, _ = time.Parse(time.RFC3339Nano, at)
	p.Failed = strings.TrimSpace(p.Failed)
	return p, nil
}
