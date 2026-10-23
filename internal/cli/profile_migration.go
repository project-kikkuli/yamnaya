package cli

import (
	"encoding/json"
	"fmt"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

type migration struct {
	ResumeNote  string          `json:"resume_note,omitempty"`
	Version     int             `json:"version"`
	Source      launch.Record   `json:"source"`
	Destination profile.Profile `json:"destination"`
	Expected    string          `json:"expected_rollout"`
	Head        string          `json:"published_head"`
	State       string          `json:"state"`
	Blocked     string          `json:"blocked,omitempty"`
	Fingerprint string          `json:"fingerprint,omitempty"`
}

func migrationPath(dir, harness, id string) string {
	return filepath.Join(dir, "migrations", harness+"-"+id+".json")
}
func saveMigration(dir string, m migration) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return profile.Write(migrationPath(dir, m.Source.Harness, m.Source.Session), b)
}
func loadMigration(dir, harness, id string) (migration, error) {
	var m migration
	b, err := os.ReadFile(migrationPath(dir, harness, id))
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}
func (l launcher) finishMigration(m migration) (launch.Record, error) {
	dir := filepath.Dir(l.records.Dir)
	rec := m.Source
	if err := recordProfile(rec).DefaultConstraint(); err != nil {
		return rec, err
	}
	if err := m.Destination.DefaultConstraint(); err != nil {
		return rec, err
	}
	if _, err := l.nativeLifecycle(rec, m.Destination, false); err != nil {
		return rec, err
	}
	release := func() {}
	if rec.Harness == "codex" {
		unlock, err := writerLock(m.Destination.Root, rec.Session)
		if err != nil {
			return rec, err
		}
		defer unlock()
		release, err = writerLock(rec.ConfigRoot, rec.Session)
		if err != nil {
			return rec, err
		}
	}
	defer release()
	head, err := transferConversation(dir, rec, m.Destination)
	if err != nil {
		return rec, err
	}
	m.Head, m.State = head, "published"
	if err = saveMigration(dir, m); err != nil {
		return rec, err
	}
	if _, err := l.nativeLifecycle(rec, m.Destination, false); err != nil {
		return rec, err
	}
	if rec.Harness == "codex" {
		current, err := selectedRollout(sqliteRoot(m.Destination), rec.Session)
		if err != nil {
			return rec, err
		}
		if current != head {
			if err = retargetRollout(sqliteRoot(m.Destination), rec.Session, m.Expected, head); err != nil {
				return rec, err
			}
		}
	}
	m.State = "selected"
	if err = saveMigration(dir, m); err != nil {
		return rec, err
	}
	setRecordProfile(&rec, m.Destination)
	rec.Handoff = m.ResumeNote
	if err = l.records.Save(rec); err != nil {
		return rec, err
	}
	if l.core != nil {
		org, err := l.core.LoadOrg()
		if err != nil {
			return rec, err
		}
		if manager, ok := org.ManagerOf(rec.Session); ok {
			manager.Profile = rec.Profile
			manager.ConfigRoot = rec.ConfigRoot
			if err = l.core.Store.SaveManager(manager); err != nil {
				return rec, err
			}
		}
	}
	m.State = "recorded"
	if err = saveMigration(dir, m); err != nil {
		return rec, err
	}
	return rec, nil
}
func (l launcher) recoverMigration(rec launch.Record) (launch.Record, error) {
	dir := filepath.Dir(l.records.Dir)
	m, err := loadMigration(dir, rec.Harness, rec.Session)
	if err != nil {
		return rec, err
	}
	if m.Version == 0 || m.State == "running" {
		return rec, nil
	}
	if m.Version != 1 {
		return rec, fmt.Errorf("unsupported pending profile handoff version %d", m.Version)
	}
	if l.core != nil {
		if live, err := l.core.Find(rec.Session); err == nil && live.ConfigRoot == m.Source.ConfigRoot {
			return rec, fmt.Errorf("profile handoff waits for the source to be idle and stopped; ym switch %s --profile %s", rec.Key, m.Destination.ID)
		}
	}
	return l.finishMigration(m)
}

func (l launcher) RecoverProfiles(b core.Board) error {
	dir := filepath.Dir(l.records.Dir)
	files, _ := filepath.Glob(filepath.Join(dir, "migrations", "*.json"))
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var m migration
		if err = json.Unmarshal(raw, &m); err != nil {
			return err
		}
		if m.Version != 1 || m.State == "running" {
			continue
		}
		for _, s := range b.Sessions {
			if s.Remote != "" || s.Harness != m.Source.Harness || s.ID != m.Source.Session || s.Profile != m.Destination.ID {
				continue
			}
			if s.Harness == "codex" {
				selected, err := selectedRollout(sqliteRoot(m.Destination), s.ID)
				if err != nil {
					return err
				}
				if m.Head != "" && selected != m.Head {
					return fmt.Errorf("pending handoff has an unexpected native head; source remains recoverable")
				}
			}
			rec, ok := l.records.Find(m.Source.Key)
			if !ok {
				return fmt.Errorf("pending handoff lost its launch record")
			}
			setRecordProfile(&rec, m.Destination)
			rec.NativePID = s.PID
			rec.Dedicated = s.Harness == "codex"
			if err = l.records.Save(rec); err != nil {
				return err
			}
			m.State = "running"
			m.Blocked = ""
			if err = saveMigration(dir, m); err != nil {
				return err
			}
		}
		if m.State == "running" {
			continue
		}
		fingerprint, err := l.migrationFingerprint(m)
		if err != nil {
			return err
		}
		if m.Blocked != "" && m.Fingerprint == fingerprint {
			continue
		}
		if s, ok := b.Session(m.Source.Session); ok && !s.Settled() {
			continue
		}
		_, attemptErr := l.SwitchProfile(m.Source.Key, m.Destination.ID)
		if attemptErr != nil {
			latest, err := loadMigration(dir, m.Source.Harness, m.Source.Session)
			if err != nil {
				return err
			}
			latest.Blocked = attemptErr.Error()
			latest.Fingerprint = fingerprint
			if err = saveMigration(dir, latest); err != nil {
				return err
			}
		}
	}
	return nil
}
func (l launcher) migrationFingerprint(m migration) (string, error) {
	ps, err := proc.List()
	if l.core != nil && l.core.Procs != nil {
		ps, err = l.core.Procs()
	}
	if err != nil {
		return "", err
	}
	var parts []string
	for _, p := range ps {
		if p.Command() == m.Source.Harness {
			parts = append(parts, fmt.Sprintf("%d:%d", p.PID, p.PPID))
		}
	}
	for _, p := range []string{transcript(m.Source), filepath.Join(m.Destination.Root, "auth.json"), filepath.Join(m.Destination.Root, ".claude.json"), filepath.Join(m.Destination.Root, "config.toml")} {
		if info, err := os.Stat(p); err == nil {
			parts = append(parts, fmt.Sprintf("%s:%d:%d", p, info.Size(), info.ModTime().UnixNano()))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "|"), nil
}
