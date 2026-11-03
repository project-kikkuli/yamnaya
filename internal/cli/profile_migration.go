package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/nativefile"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

type migration struct {
	ResumeNote  string          `json:"resume_note,omitempty"`
	Reason      string          `json:"reason,omitempty"`
	Version     int             `json:"version"`
	Source      launch.Record   `json:"source"`
	Destination profile.Profile `json:"destination"`
	Expected    string          `json:"expected_rollout"`
	Head        string          `json:"published_head"`
	State       string          `json:"state"`
	Blocked     string          `json:"blocked,omitempty"`
	Fingerprint string          `json:"fingerprint,omitempty"`
	NoteRead    bool            `json:"note_read,omitempty"`
	Crew        []busyCrew      `json:"crew,omitempty"`
	// Since is when the record began, or the switch's own stop: a kill stamp past it is the owner's, not the switch's.
	Since time.Time `json:"since,omitzero"`
}

// inherit keeps the reason and note a queued switch to the same login was saved with when the attempt applying it brings none.
func (m *migration) inherit(prior migration) {
	if prior.State != "queued" || prior.Destination.ID != m.Destination.ID {
		return
	}
	m.Blocked, m.Fingerprint = prior.Blocked, prior.Fingerprint
	if m.Reason == "" {
		m.Reason = prior.Reason
	}
	if m.ResumeNote == "" {
		m.ResumeNote = prior.ResumeNote
	}
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
	note := switchNote(m, backgroundTasks(rec, m.Crew))
	setRecordProfile(&rec, m.Destination)
	rec.Handoff = note
	if carried, err := nativefile.ReadFile(transcript(rec)); err == nil {
		rec.HandoffFrom = len(carried)
	}
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
	source := rec
	setRecordProfile(&source, recordProfile(m.Source))
	source.Account = m.Source.Account
	m.Source = source
	if m.State == "queued" && rec.Harness == "codex" {
		if m.Expected, err = selectedRollout(sqliteRoot(m.Destination), rec.Session); err != nil {
			return rec, err
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
		if m.Version != 1 {
			continue
		}
		if m.State == "running" {
			if !m.NoteRead {
				if err = l.settleNote(m); err != nil {
					return err
				}
			}
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
			if err = settleFailover(dir, m.Source.Harness, m.Source.Session, m.Destination); err != nil {
				return err
			}
		}
		if m.State == "running" {
			continue
		}
		rec, recorded := l.records.Find(m.Source.Key)
		_, live := b.Session(m.Source.Session)
		landed := recorded && rec.IsLanded()
		killed := recorded && rec.Killed.After(m.Since)
		early := m.State == "queued" || m.State == "prepared"
		if !live && (early && (landed || killed) || m.State == "queued" && !recorded) {
			if err = l.closeQueued(m.Source.Harness, m.Source.Session, "the session ended before its switch to "+m.Destination.ID); err != nil {
				return err
			}
			continue
		}
		// Serve never resumes an ended session: an exited one keeps its queued switch, and a published handoff stays whole, for ym revive.
		if !live && (landed || killed || m.State == "queued") {
			continue
		}
		if s, ok := b.Session(m.Source.Session); ok && l.turnEnded(s, nil) != nil {
			continue
		}
		fingerprint, err := l.migrationFingerprint(m)
		if err != nil {
			return err
		}
		if m.Blocked != "" && m.Fingerprint == fingerprint {
			continue
		}
		ref := m.Source.Key
		if !recorded {
			ref = m.Source.Session
		}
		next := l
		next.queuedTo = m.Destination.ID
		_, attemptErr := next.QueueProfileSwitch(ref, m.Destination.ID, m.Reason)
		if attemptErr != nil && !errors.Is(attemptErr, profile.ErrOperationBusy) {
			err = l.updatePending(m.Source.Harness, m.Source.Session, func(latest *migration) bool {
				if latest.Destination.ID != m.Destination.ID {
					return false
				}
				latest.Blocked, latest.Fingerprint = attemptErr.Error(), fingerprint
				return true
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// closeQueued removes the session's pending switch, and says why on any failover that was waiting on it.
func (l launcher) closeQueued(harness, session, why string) error {
	dir := filepath.Dir(l.records.Dir)
	if err := os.Remove(migrationPath(dir, harness, session)); err != nil {
		return err
	}
	return closeFailover(dir, harness, session, why)
}

// settleNote marks m's resume note read once the resumed session's record no longer carries it.
func (l launcher) settleNote(m migration) error {
	return l.updatePending(m.Source.Harness, m.Source.Session, func(latest *migration) bool {
		if latest.State != "running" {
			return false
		}
		rec, ok := l.records.Find(m.Source.Key)
		if ok && rec.Handoff != "" {
			if read, err := l.dropReadHandoff(rec); err != nil || read.Handoff != "" {
				return false
			}
		}
		latest.NoteRead = true
		return true
	})
}

// PendingSwitches are the login switches not yet running, by harness:session, for the board to show.
func (l launcher) PendingSwitches() map[string]core.Switching {
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(l.records.Dir), "migrations", "*.json"))
	out := map[string]core.Switching{}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m struct {
			Version int    `json:"version"`
			State   string `json:"state"`
			Blocked string `json:"blocked"`
			Source  struct {
				Harness string `json:"harness"`
				Session string `json:"session"`
			} `json:"source"`
			Destination struct {
				ID string `json:"id"`
			} `json:"destination"`
		}
		if json.Unmarshal(raw, &m) != nil || m.Version != 1 || m.State == "running" {
			continue
		}
		out[m.Source.Harness+":"+m.Source.Session] = core.Switching{To: m.Destination.ID, Blocked: m.Blocked}
	}
	return out
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
