package usage

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

const claudeLimitsFile = "claude-limits.json"

// statusWindow is one window of Claude Code's statusline rate_limits; resets_at is epoch seconds or RFC 3339.
type statusWindow struct {
	Used   *float64        `json:"used_percentage"`
	Resets json.RawMessage `json:"resets_at"`
}

type claudeLimits struct {
	Seen     int64         `json:"seen"`
	FiveHour *statusWindow `json:"five_hour,omitempty"`
	SevenDay *statusWindow `json:"seven_day,omitempty"`
}

// RecordClaudeLimits saves the rate limits in one statusline JSON document to stateDir. Input without them changes nothing.
func RecordClaudeLimits(stateDir string, in io.Reader, now time.Time) error {
	var doc struct {
		RateLimits *claudeLimits `json:"rate_limits"`
	}
	if err := json.NewDecoder(in).Decode(&doc); err != nil {
		return err
	}
	l := doc.RateLimits
	if l == nil || (l.FiveHour == nil && l.SevenDay == nil) {
		return nil
	}
	l.Seen = now.Unix()
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	return writeFile(filepath.Join(stateDir, claudeLimitsFile), b)
}

func readClaudeLimits(stateDir string) []core.Limit {
	b, err := os.ReadFile(filepath.Join(stateDir, claudeLimitsFile))
	if err != nil {
		return nil
	}
	var l claudeLimits
	if json.Unmarshal(b, &l) != nil {
		return nil
	}
	var out []core.Limit
	for _, w := range []struct {
		name string
		span time.Duration
		w    *statusWindow
	}{{"5h", 5 * time.Hour, l.FiveHour}, {"7d", 7 * 24 * time.Hour, l.SevenDay}} {
		if w.w == nil || w.w.Used == nil {
			continue
		}
		resets, err := parseResets(w.w.Resets)
		if err != nil {
			continue
		}
		out = append(out, core.Limit{Harness: claudeName, Name: w.name, Used: *w.w.Used, Window: w.span, ResetsAt: resets, Seen: time.Unix(l.Seen, 0)})
	}
	return out
}

func parseResets(raw json.RawMessage) (time.Time, error) {
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		if n > 1e12 {
			return time.UnixMilli(int64(n)), nil
		}
		return time.Unix(int64(n), 0), nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return time.Time{}, err
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Unix(int64(v), 0), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New("unreadable resets_at")
}

// current drops windows that have already reset.
func current(ls []core.Limit, now time.Time) []core.Limit {
	var out []core.Limit
	for _, l := range ls {
		if l.ResetsAt.After(now) {
			out = append(out, l)
		}
	}
	return out
}
