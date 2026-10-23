package cli

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/project-kikkuli/yamnaya/internal/nativefile"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
	"github.com/project-kikkuli/yamnaya/internal/usage"
)

type rolloutMeta struct {
	Type    string `json:"type"`
	Payload struct {
		ID      string `json:"id"`
		Rollout string `json:"rollout_id"`
		Base    *struct {
			ID      string `json:"thread_id"`
			End     int64  `json:"end_byte_offset"`
			Ordinal int64  `json:"end_ordinal_exclusive"`
		} `json:"history_base"`
	} `json:"payload"`
}

func readRolloutMeta(path string) (rolloutMeta, error) {
	var m rolloutMeta
	f, err := nativefile.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(line, &m)
	if err == nil && (m.Type != "session_meta" || m.Payload.ID == "") {
		err = fmt.Errorf("invalid native rollout metadata in %s", path)
	}
	return m, err
}
func nativeDB(root string) (*sql.DB, error) {
	paths, _ := filepath.Glob(filepath.Join(root, "state_*.sqlite"))
	if len(paths) == 0 {
		return nil, nil
	}
	sort.Strings(paths)
	return sql.Open("sqlite", "file:"+paths[len(paths)-1]+"?"+url.Values{"_pragma": {"busy_timeout(2000)"}}.Encode())
}
func selectedRollout(root, id string) (string, error) {
	db, err := nativeDB(root)
	if err != nil || db == nil {
		return "", err
	}
	defer db.Close()
	var path string
	err = db.QueryRow(`SELECT rollout_path FROM threads WHERE id=?`, id).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return path, err
}
func retargetRollout(root, id, expected, destination string) error {
	if expected == "" || expected == destination {
		return nil
	}
	db, err := nativeDB(root)
	if err != nil {
		return err
	}
	if db == nil {
		return fmt.Errorf("destination thread selection disappeared")
	}
	defer db.Close()
	res, err := db.Exec(`UPDATE threads SET rollout_path=? WHERE id=? AND rollout_path=?`, destination, id, expected)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("destination selected rollout changed during handoff; source remains recoverable")
	}
	return nil
}
func writerLock(root, id string) (func(), error) {
	dir := filepath.Join(root, "thread-writer-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	coordination, err := os.OpenFile(filepath.Join(dir, ".coordination.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(coordination.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		coordination.Close()
		return nil, fmt.Errorf("native thread publication is active: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, id+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err == nil {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		if f != nil {
			f.Close()
		}
		coordination.Close()
		return nil, fmt.Errorf("native conversation has a live writer: %w", err)
	}
	return func() { f.Close(); coordination.Close() }, nil
}
func rolloutID(path string, m rolloutMeta) string {
	if m.Payload.Rollout != "" {
		return m.Payload.Rollout
	}
	name := strings.TrimSuffix(nativefile.PlainName(filepath.Base(path)), ".jsonl")
	if i := strings.LastIndex(name, "_"); i >= 0 {
		return name[i+1:]
	}
	return m.Payload.ID
}
func artifactClosure(root, head string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	var visit func(string) error
	visit = func(path string) error {
		if seen[path] {
			return fmt.Errorf("cyclic rollout history at %s", path)
		}
		seen[path] = true
		rel, err := filepath.Rel(root, path)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return fmt.Errorf("rollout escapes selected profile")
		}
		m, err := readRolloutMeta(path)
		if err != nil {
			return err
		}
		if m.Payload.Base != nil {
			var base string
			for _, dir := range []string{"sessions", "archived_sessions"} {
				err = filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, e error) error {
					if e != nil {
						return nil
					}
					if d.IsDir() || !nativefile.IsTranscript(p) {
						return nil
					}
					if strings.HasSuffix(strings.TrimSuffix(nativefile.PlainName(p), ".jsonl"), m.Payload.Base.ID) {
						if base != "" && base != p {
							return fmt.Errorf("ambiguous base rollout %s", m.Payload.Base.ID)
						}
						base = p
					}
					return nil
				})
				if err != nil {
					return err
				}
			}
			if base == "" {
				return fmt.Errorf("missing history base rollout %s", m.Payload.Base.ID)
			}
			info, err := nativefile.Stat(base)
			if err != nil {
				return err
			}
			if info.Size() < m.Payload.Base.End {
				return fmt.Errorf("incomplete history base %s", base)
			}
			if err = visit(base); err != nil {
				return err
			}
		}
		out = append(out, path)
		return nil
	}
	err := visit(head)
	return out, err
}
func stageArtifact(state string, source profile.Profile, destination profile.Profile, path, conversation string) (string, error) {
	if err := containedArtifact(source.Root, path, true); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(source.Root, path)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("conversation artifact outside source profile")
	}
	dest := filepath.Join(destination.Root, rel)
	if err := containedArtifact(destination.Root, dest, false); err != nil {
		return "", err
	}
	data, err := nativefile.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return "", fmt.Errorf("conversation has an incomplete record")
	}
	old, err := nativefile.ReadFile(dest)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if !bytes.HasPrefix(data, old) {
		return "", fmt.Errorf("destination conversation diverged; source is unchanged")
	}
	if len(old) == len(data) {
		return dest, nil
	}
	hash, err := profile.HashRange(path, int64(len(old)), int64(len(data)))
	if err != nil {
		return "", err
	}
	ranges, err := profile.Imports(state, dest)
	if err != nil {
		return "", err
	}
	imported := profile.Import{Start: int64(len(old)), End: int64(len(data)), Hash: hash, SourceRoot: source.Root, SourceProfile: source.ID, SourceAccount: source.Account, SourcePath: path, Conversation: conversation}
	if source.Harness == "codex" {
		meta, err := readRolloutMeta(path)
		if err != nil {
			return "", err
		}
		imported.Rollout = rolloutID(path, meta)
		imported.Seed, err = usage.ImportSeedAt(source.Root, path, int64(len(data)))
		if err != nil {
			return "", err
		}
	}
	imported.Origins, err = usage.TransferOrigins(state, path, source, imported.Start, imported.End)
	if err != nil {
		return "", err
	}
	if len(ranges) > 0 && ranges[len(ranges)-1].End > int64(len(old)) {
		last := ranges[len(ranges)-1]
		if last.Start != imported.Start || last.End != imported.End || last.Hash != imported.Hash {
			return "", fmt.Errorf("conflicting pending import")
		}
		ranges = ranges[:len(ranges)-1]
	}
	ranges = append(ranges, imported)
	if err = profile.SaveImports(state, dest, ranges); err != nil {
		return "", err
	}
	published := data
	if strings.HasSuffix(path, ".zst") {
		published, err = os.ReadFile(path)
		if err != nil {
			return "", err
		}
	}
	if err = profile.Write(dest, published); err != nil {
		return "", err
	}
	return dest, nil
}
func transferConversation(state string, rec launch.Record, destination profile.Profile) (string, error) {
	source := recordProfile(rec)
	head := transcript(rec)
	if head == "" {
		return "", fmt.Errorf("source transcript is unavailable")
	}
	paths := []string{head}
	if rec.Harness == "codex" {
		var err error
		paths, err = artifactClosure(source.Root, head)
		if err != nil {
			return "", err
		}
	}
	if rec.Harness == "codex" {
		seen := map[string]bool{}
		for _, path := range paths {
			seen[path] = true
		}
		children := recordCodex(rec).ConversationArtifacts(rec.Session)
		for id, path := range children {
			if id == rec.Session {
				continue
			}
			closure, err := artifactClosure(source.Root, path)
			if err != nil {
				return "", err
			}
			for _, artifact := range closure {
				if !seen[artifact] {
					seen[artifact] = true
					paths = append(paths, artifact)
				}
			}
		}
	}
	var active string
	for _, path := range paths {
		conversation := rec.Session
		if rec.Harness == "codex" {
			meta, err := readRolloutMeta(path)
			if err != nil {
				return "", err
			}
			conversation = meta.Payload.ID
		}
		dest, err := stageArtifact(state, source, destination, path, conversation)
		if err != nil {
			return "", err
		}
		if path == head {
			active = dest
		}
	}
	if rec.Harness == "claude" {
		// Claude keeps per-conversation subagent context beside the transcript.
		dir := strings.TrimSuffix(head, ".jsonl")
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, ".jsonl") {
				_, err = stageArtifact(state, source, destination, path, rec.Session)
				return err
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return active, nil
}

func containedArtifact(root, path string, source bool) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("conversation artifact is not a regular file")
		}
	} else if source || !os.IsNotExist(err) {
		return err
	}
	ancestor := filepath.Dir(path)
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return err
		}
		next := filepath.Dir(ancestor)
		if next == ancestor {
			break
		}
		ancestor = next
	}
	actual, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(physicalProfileRoot(root), actual)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("conversation artifact directory escapes profile root")
	}
	return nil
}
