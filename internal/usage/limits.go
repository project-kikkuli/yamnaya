package usage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/project-kikkuli/yamnaya/internal/profile"
	"io"
	"os"
	"path/filepath"
	"sort"
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
	Account  string        `json:"account,omitempty"`
	Profile  string        `json:"profile,omitempty"`
	Seen     int64         `json:"seen"`
	FiveHour *statusWindow `json:"five_hour,omitempty"`
	SevenDay *statusWindow `json:"seven_day,omitempty"`
}

// RecordClaudeLimits saves the rate limits in one statusline JSON document to stateDir. Input without them changes nothing.
func RecordClaudeLimits(stateDir string, in io.Reader, now time.Time) error {
	ps, err := profile.Load(stateDir)
	if err != nil {
		return err
	}
	selected := os.Getenv("YAMNAYA_PROFILE")
	root := os.Getenv("CLAUDE_CONFIG_DIR")
	if root != "" {
		if canonical, err := filepath.EvalSymlinks(root); err == nil {
			root = canonical
		}
		if selected == "" {
			for _, candidate := range ps {
				if candidate.Harness == claudeName && candidate.Root == root {
					selected = candidate.ID
					break
				}
			}
		}
	}
	p, err := profile.Find(ps, claudeName, selected)
	if err != nil {
		return err
	}
	if root != "" && root != p.Root {
		return fmt.Errorf("statusline root disagrees with selected profile")
	}
	if account, bound := os.LookupEnv("YAMNAYA_ACCOUNT"); bound {
		if account == p.Account {
			p.Account = account
		} else {
			p.Account = ""
		}
	}

	return RecordProfileLimits(stateDir, p, in, now)
}
func RecordProfileLimits(stateDir string, p profile.Profile, in io.Reader, now time.Time) error {
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
	l.Account = p.Account
	l.Profile = p.ID
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	return writeFile(claudeLimitPath(stateDir, p.ID), b)
}

func readClaudeLimits(stateDir string) []core.Limit {
	ps, err := profile.Load(stateDir)
	if err != nil {
		return nil
	}
	var out []core.Limit
	for _, p := range ps {
		if p.Harness == claudeName {
			out = append(out, readProfileLimits(stateDir, p)...)
		}
	}
	return out
}
func claudeLimitPath(dir, id string) string {
	if profile.ID(id) == "default" {
		return filepath.Join(dir, claudeLimitsFile)
	}
	return filepath.Join(dir, "claude-limits."+id+".json")
}
func readProfileLimits(stateDir string, p profile.Profile) []core.Limit {
	b, err := os.ReadFile(claudeLimitPath(stateDir, p.ID))
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
		out = append(out, core.Limit{Harness: claudeName, Profile: p.ID, ProfileLabel: p.Label, Account: l.Account, Name: w.name, Used: *w.w.Used, Window: w.span, ResetsAt: resets, Seen: time.Unix(l.Seen, 0)})
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
	newest := map[string]core.Limit{}
	for _, l := range ls {
		if l.ResetsAt.After(now) {
			if old, ok := newest[l.Key()]; !ok || old.Seen.Before(l.Seen) {
				newest[l.Key()] = l
			}
		}
	}
	var out []core.Limit
	for _, l := range newest {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

func KnownLimits(dir string, now time.Time) []core.Limit {
	out := readClaudeLimits(dir)
	path := filepath.Join(dir, "ym.db")
	if _, err := os.Stat(path); err != nil {
		return current(out, now)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return current(out, now)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT body FROM ym_usage_limits`)
	if err != nil {
		return current(out, now)
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		var lim core.Limit
		if rows.Scan(&body) == nil && json.Unmarshal([]byte(body), &lim) == nil {
			out = append(out, lim)
		}
	}
	return current(out, now)
}
