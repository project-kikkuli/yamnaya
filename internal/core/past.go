package core

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

// PastTextCap is the most characters of a past session's prompts its filter text holds.
const PastTextCap = 4000

// Past is a conversation a harness's own history lists, whoever started it.
type Past struct {
	Harness, Session, Cwd, Title string
	Last                         time.Time
	// Prompts are what the owner typed into it, oldest first.
	Prompts []string
	// Transcript is the conversation's file on disk, empty when it is gone.
	Transcript string
}

// Text is the session's prompts joined, at most PastTextCap characters.
func (p Past) Text() string {
	s := []rune(strings.Join(p.Prompts, " ¦ "))
	return string(s[:min(len(s), PastTextCap)])
}

// PastDirs are where Claude Code and Codex keep the history ReadPast reads: Claude's config dir, Codex's home and the directory
// of Codex's state database.
type PastDirs struct{ Claude, Codex, CodexState string }

// ReadPast reads every Claude Code and Codex conversation the harnesses' history files list, newest first.
func ReadPast(d PastDirs) ([]Past, error) {
	cl, err := readClaudePast(d.Claude)
	if err != nil {
		return nil, err
	}
	cx, err := readCodexPast(d.Codex, d.CodexState)
	if err != nil {
		return nil, err
	}
	out := append(cl, cx...)
	slices.SortStableFunc(out, func(a, b Past) int { return b.Last.Compare(a.Last) })
	return out, nil
}

var claudeDirChars = regexp.MustCompile(`[^A-Za-z0-9]`)

func readClaudePast(dir string) ([]Past, error) {
	var out []Past
	at := map[string]int{}
	err := eachJSONLine(filepath.Join(dir, "history.jsonl"), func(line []byte) {
		var h struct {
			Display   string `json:"display"`
			Timestamp int64  `json:"timestamp"`
			Project   string `json:"project"`
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(line, &h) != nil || h.SessionID == "" {
			return
		}
		when := time.UnixMilli(h.Timestamp)
		i, ok := at[h.SessionID]
		if !ok {
			i = len(out)
			at[h.SessionID] = i
			out = append(out, Past{Harness: "claude", Session: h.SessionID, Cwd: h.Project, Last: when})
		}
		p := &out[i]
		p.Prompts = append(p.Prompts, oneLine(h.Display))
		if when.After(p.Last) {
			p.Last = when
		}
	})
	for i := range out {
		path := filepath.Join(dir, "projects", claudeDirChars.ReplaceAllString(out[i].Cwd, "-"), out[i].Session+".jsonl")
		if _, err := os.Stat(path); err == nil {
			out[i].Transcript = path
		}
	}
	return out, err
}

func readCodexPast(home, state string) ([]Past, error) {
	matches, _ := filepath.Glob(filepath.Join(state, "state_*.sqlite"))
	if len(matches) == 0 {
		return nil, nil
	}
	slices.Sort(matches)
	prompts := map[string][]string{}
	err := eachJSONLine(filepath.Join(home, "history.jsonl"), func(line []byte) {
		var h struct {
			SessionID string `json:"session_id"`
			Text      string `json:"text"`
		}
		if json.Unmarshal(line, &h) == nil && h.SessionID != "" {
			prompts[h.SessionID] = append(prompts[h.SessionID], oneLine(h.Text))
		}
	})
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+matches[len(matches)-1]+"?mode=ro&"+url.Values{"_pragma": {"busy_timeout(2000)"}}.Encode())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, cwd, rollout_path, coalesce(updated_at_ms, updated_at*1000), first_user_message, title FROM threads
		WHERE coalesce(thread_source, '') != 'subagent' AND archived = 0`)
	if err != nil {
		return nil, fmt.Errorf("read Codex threads: %w", err)
	}
	defer rows.Close()
	var out []Past
	for rows.Next() {
		var p Past
		var rollout, first string
		var last int64
		if err := rows.Scan(&p.Session, &p.Cwd, &rollout, &last, &first, &p.Title); err != nil {
			return nil, err
		}
		p.Harness, p.Last, p.Prompts = "codex", time.UnixMilli(last), prompts[p.Session]
		if len(p.Prompts) == 0 && first != "" {
			p.Prompts = []string{oneLine(first)}
		}
		if _, err := os.Stat(rollout); err == nil && rollout != "" {
			p.Transcript = rollout
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func eachJSONLine(path string, f func([]byte)) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		f(sc.Bytes())
	}
	return sc.Err()
}

// Unlaunched drops from ps the sessions b already shows: those running and those ym launched, so ym revive keeps bringing those back.
func Unlaunched(ps []Past, b Board) []Past {
	known := map[string]bool{}
	for _, l := range b.Launched {
		known[l.Session] = true
	}
	for _, s := range b.Sessions {
		known[s.ID] = true
	}
	for _, p := range b.Projects {
		for _, d := range p.Dormant {
			known[d.Session] = true
		}
	}
	return slices.DeleteFunc(slices.Clone(ps), func(p Past) bool { return known[p.Session] })
}

// FilterPast keeps the sessions holding every space-separated word of query in their prompts or directory, as exact substrings;
// a word with no upper case letter matches any case.
func FilterPast(ps []Past, query string) []Past {
	words := strings.Fields(query)
	if len(words) == 0 {
		return ps
	}
	var out []Past
	for _, p := range ps {
		text := p.Text() + " " + p.Cwd
		lower := strings.ToLower(text)
		if !slices.ContainsFunc(words, func(w string) bool {
			if strings.IndexFunc(w, unicode.IsUpper) < 0 {
				return !strings.Contains(lower, w)
			}
			return !strings.Contains(text, w)
		}) {
			out = append(out, p)
		}
	}
	return out
}

// ResumePast resumes p in its own directory and conversation on ym's server, where the board shows it as launched.
func (c *Core) ResumePast(p Past) (string, error) {
	if p.Transcript == "" {
		return "", fmt.Errorf("%s session %s has no transcript on disk to resume", p.Harness, Short(p.Session))
	}
	if c.Launches == nil {
		return "", errors.New("ym's launcher is unavailable")
	}
	if p.Cwd == "" {
		return "", fmt.Errorf("%s session %s names no directory to resume it in", p.Harness, Short(p.Session))
	}
	return c.Launches.Relaunch(Live{Session: Session{ID: p.Session, Harness: p.Harness, Cwd: p.Cwd}}, p.Harness, p.Session)
}
