package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/nativefile"
	"os"
	"path/filepath"
	"strings"
)

// Transcript is one transcript file: a Claude Code session's own transcript or a Codex rollout.
type Transcript struct {
	Path    string
	Harness string
	Session string
	Size    int64
	MTime   int64
}

// RootTranscripts lists every Claude session transcript (not its subagents) and every Codex rollout.
func RootTranscripts(claudeDir, codexHome string) []Transcript {
	var out []Transcript
	for _, s := range claudeSources(claudeDir) {
		if s.Root {
			out = append(out, Transcript{Path: s.Path, Harness: claudeName, Session: s.Session, Size: s.Size, MTime: s.MTime})
		}
	}
	for _, s := range codexSources(codexHome) {
		out = append(out, Transcript{Path: s.Path, Harness: codexName, Session: s.Session, Size: s.Size, MTime: s.MTime})
	}
	return out
}

// ReadLines calls fn for each complete line after offset and returns the offset past the last one.
func ReadLines(path string, offset int64, fn func(line []byte)) (int64, error) {
	return readLines(path, offset, fn)
}

// Dirs are the Claude config dir and Codex home ym reads transcripts from.
func Dirs() (claudeDir, codexHome string) {
	m := New("")
	return m.ClaudeDir, m.CodexHome
}

// AllTranscripts lists every Claude session transcript, its subagents' included, and every Codex rollout.
func AllTranscripts(claudeDir, codexHome string) []Transcript {
	var out []Transcript
	for _, s := range claudeSources(claudeDir) {
		out = append(out, Transcript{Path: s.Path, Harness: claudeName, Session: s.Session, Size: s.Size, MTime: s.MTime})
	}
	for _, s := range codexSources(codexHome) {
		out = append(out, Transcript{Path: s.Path, Harness: codexName, Session: s.Session, Size: s.Size, MTime: s.MTime})
	}
	return out
}

func ImportSeed(path string, end int64) ([]byte, error) {
	var cursor codexCursor
	chunk := codexChunk{limits: map[string]core.Limit{}}
	var pos int64
	_, err := readLines(path, 0, func(line []byte) {
		pos += int64(len(line))
		if pos <= end {
			parseCodex(line, &cursor, &chunk)
		}
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(cursor)
}

func ImportSeedAt(root, path string, end int64) ([]byte, error) {
	cursor, err := rolloutSeed(root, path, end, map[string]bool{})
	if err != nil {
		return nil, err
	}
	return json.Marshal(cursor)
}
func rolloutSeed(root, path string, end int64, seen map[string]bool) (codexCursor, error) {
	var cursor codexCursor
	if seen[path] {
		return cursor, fmt.Errorf("cyclic usage lineage")
	}
	seen[path] = true
	meta, err := nativefile.Open(path)
	if err != nil {
		return cursor, err
	}
	line, err := bufio.NewReader(meta).ReadBytes('\n')
	meta.Close()
	if err != nil {
		return cursor, err
	}
	var doc struct {
		Payload struct {
			Base *struct {
				ID  string `json:"thread_id"`
				End int64  `json:"end_byte_offset"`
			} `json:"history_base"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &doc) == nil && doc.Payload.Base != nil {
		base := doc.Payload.Base
		var found string
		for _, dir := range []string{"sessions", "archived_sessions"} {
			filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && strings.HasSuffix(strings.TrimSuffix(nativefile.PlainName(path), ".jsonl"), base.ID) {
					found = path
				}
				return nil
			})
		}
		if found == "" {
			return cursor, fmt.Errorf("missing usage history base %s", base.ID)
		}
		cursor, err = rolloutSeed(root, found, base.End, seen)
		if err != nil {
			return cursor, err
		}
	}
	chunk := codexChunk{limits: map[string]core.Limit{}}
	var pos int64
	_, err = readLines(path, 0, func(line []byte) {
		pos += int64(len(line))
		if pos <= end {
			parseCodex(line, &cursor, &chunk)
		}
	})
	return cursor, err
}
