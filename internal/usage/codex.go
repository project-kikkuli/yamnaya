package usage

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/project-kikkuli/yamnaya/internal/nativefile"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// codexSources lists every rollout under sessions/, keyed to the thread id that ends its name.
func codexSources(home string) []source {
	var out []source
	for _, dir := range []string{"sessions", "archived_sessions"} {
		walk(filepath.Join(home, dir), func(path string, d fs.DirEntry) {
			name := d.Name()
			if !strings.HasPrefix(name, "rollout-") || !nativefile.IsTranscript(name) {
				return
			}
			id := rolloutConversation(path)
			if id == "" {
				id = strings.TrimSuffix(nativefile.PlainName(name), ".jsonl")
				if len(id) >= 36 {
					id = id[len(id)-36:]
				}
			}
			if len(id) < 36 {
				return
			}
			if s, ok := stat(path, id, true); ok {
				out = append(out, s)
			}
		})
	}
	return out
}

// codexEdges reads parent threads from the state database's spawn edges.
func codexEdges(home string) map[string]string {
	edges := map[string]string{}
	matches, _ := filepath.Glob(filepath.Join(home, "state_*.sqlite"))
	if len(matches) == 0 {
		return edges
	}
	sort.Strings(matches)
	db, err := sql.Open("sqlite", "file:"+matches[len(matches)-1]+"?mode=ro&"+url.Values{"_pragma": {"busy_timeout(2000)"}}.Encode())
	if err != nil {
		return edges
	}
	defer db.Close()
	rows, err := db.Query(`SELECT child_thread_id, parent_thread_id FROM thread_spawn_edges`)
	if err != nil {
		return edges
	}
	defer rows.Close()
	for rows.Next() {
		var c, p string
		if rows.Scan(&c, &p) == nil {
			edges[c] = p
		}
	}
	return edges
}

// codexTotals is a thread's cumulative usage; cached input is part of input.
type codexTotals struct {
	Input     int64 `json:"input_tokens"`
	Cached    int64 `json:"cached_input_tokens"`
	Output    int64 `json:"output_tokens"`
	Reasoning int64 `json:"reasoning_output_tokens"`
}

// codexCursor is what parsing a rollout must remember between reads.
type codexCursor struct {
	Model  string      `json:"model"`
	Last   codexTotals `json:"last"`
	Parent string      `json:"parent"`
}

type window struct {
	Used    float64 `json:"used_percent"`
	Minutes int64   `json:"window_minutes"`
	Resets  int64   `json:"resets_at"`
}

type codexLine struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   struct {
		Type   string          `json:"type"`
		Model  string          `json:"model"`
		Source json.RawMessage `json:"source"`
		Info   *struct {
			Total codexTotals `json:"total_token_usage"`
			Turn  codexTotals `json:"last_token_usage"`
		} `json:"info"`
		RateLimits *struct {
			ID        string  `json:"limit_id"`
			Primary   *window `json:"primary"`
			Secondary *window `json:"secondary"`
			Plan      string  `json:"plan_type"`
		} `json:"rate_limits"`
	} `json:"payload"`
}

type codexChunk struct {
	turns  []response
	limits map[string]core.Limit
}

var (
	tokenCount  = []byte(`"token_count"`)
	turnContext = []byte(`"type":"turn_context"`)
	sessionMeta = []byte(`"type":"session_meta"`)
)

func parseCodex(line []byte, cur *codexCursor, c *codexChunk) {
	sum := sha256.Sum256(bytes.TrimSpace(line))
	eventID := hex.EncodeToString(sum[:])
	head := line[:min(len(line), 120)]
	isMeta := bytes.Contains(head, sessionMeta)
	if !isMeta && !bytes.Contains(head, turnContext) && !bytes.Contains(line, tokenCount) {
		return
	}
	var r codexLine
	if json.Unmarshal(line, &r) != nil {
		return
	}
	switch {
	case r.Type == "session_meta":
		var src struct {
			Subagent struct {
				Spawn struct {
					Parent string `json:"parent_thread_id"`
				} `json:"thread_spawn"`
			} `json:"subagent"`
		}
		if json.Unmarshal(r.Payload.Source, &src) == nil && src.Subagent.Spawn.Parent != "" {
			cur.Parent = src.Subagent.Spawn.Parent
		}
	case r.Type == "turn_context":
		if r.Payload.Model != "" {
			cur.Model = r.Payload.Model
		}
	case r.Type == "event_msg" && r.Payload.Type == "token_count":
		if rl := r.Payload.RateLimits; rl != nil && rl.ID != "" {
			for _, w := range []*window{rl.Primary, rl.Secondary} {
				if w == nil || w.Minutes == 0 {
					continue
				}
				l := core.Limit{Harness: codexName, Name: rl.ID + " " + windowName(w.Minutes), Used: w.Used,
					Window: time.Duration(w.Minutes) * time.Minute, ResetsAt: time.Unix(w.Resets, 0), Plan: rl.Plan, Seen: r.Timestamp}
				if old, ok := c.limits[l.Name]; !ok || !old.Seen.After(l.Seen) {
					c.limits[l.Name] = l
				}
			}
		}
		info := r.Payload.Info
		if info == nil {
			return
		}
		d := info.Total
		if d.Input >= cur.Last.Input && d.Output >= cur.Last.Output && d.Cached >= cur.Last.Cached {
			d = codexTotals{Input: d.Input - cur.Last.Input, Cached: d.Cached - cur.Last.Cached, Output: d.Output - cur.Last.Output}
		}
		cur.Last = info.Total
		if d.Input == 0 && d.Output == 0 {
			return
		}
		c.turns = append(c.turns, response{ID: eventID,
			Model:  cur.Model,
			Day:    day(r.Timestamp),
			At:     r.Timestamp.Unix(),
			Class:  class{Tier: tierOf(info.Turn.Input)},
			Tokens: core.Tokens{Input: d.Input - d.Cached, CacheRead: d.Cached, Output: d.Output},
		})
	}
}

// windowName labels a limit window by its length, rounding Codex's 299 and 10079 minutes up.
func windowName(minutes int64) string {
	switch {
	case minutes%1440 >= 1439 || minutes%1440 == 0:
		return strconv.FormatInt((minutes+1)/1440, 10) + "d"
	case minutes%60 >= 59 || minutes%60 == 0:
		return strconv.FormatInt((minutes+1)/60, 10) + "h"
	}
	return strconv.FormatInt(minutes, 10) + "m"
}

func rolloutConversation(path string) string {
	f, err := nativefile.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil {
		return ""
	}
	var meta struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &meta) != nil || meta.Type != "session_meta" {
		return ""
	}
	return meta.Payload.ID
}
