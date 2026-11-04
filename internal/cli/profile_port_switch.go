package cli

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

var errPortRefused = errors.New("the port was refused before the session stopped")

// runsAs is the session the launch key runs as now, which a port to another harness changes; session when it has no record.
func (l launcher) runsAs(key, session string) string {
	if rec, ok := l.records.Find(key); ok && rec.Session != "" {
		return rec.Session
	}
	return session
}

// portSwitch moves rec's conversation, its turn over, to a login of another harness. The conversation gets a new id there, so what
// waits on the old id moves with it. A live source is untouched until its Stop, so a refusal up to there leaves the switch queued.
func (l launcher) portSwitch(rec launch.Record, live core.Live, dest profile.Profile, reason string) (core.SwitchOutcome, error) {
	var none core.SwitchOutcome
	dir := filepath.Dir(l.records.Dir)
	prior, err := loadMigration(dir, rec.Harness, rec.Session)
	if err != nil {
		return none, err
	}
	m := migration{Version: 1, ResumeNote: l.resumeNote, Reason: reason, Source: rec, Destination: dest, State: "prepared", Since: time.Now()}
	if prior.Version != 0 && prior.State != "running" && prior.Destination.ID == dest.ID && prior.Destination.Harness == dest.Harness {
		m.ResumeNote, m.Reason, m.Crew, m.Head = cmp.Or(m.ResumeNote, prior.ResumeNote), cmp.Or(m.Reason, prior.Reason), prior.Crew, prior.Head
	}
	if err = retargetFailover(dir, rec.Harness, rec.Session, prior, dest); err != nil {
		return none, err
	}
	hold := func() error {
		m.State = "queued"
		return saveMigration(dir, m)
	}
	if live.ID != "" {
		fresh, err := l.core.Find(rec.Harness + ":" + profile.ID(rec.Profile) + "@" + rec.Session)
		if err != nil {
			return none, errors.Join(err, hold())
		}
		if err = switchIdentity(fresh, rec); err != nil {
			return none, errors.Join(err, hold())
		}
		if err = l.turnEnded(fresh, nil); err != nil {
			if err = hold(); err != nil {
				return none, err
			}
			return core.SwitchOutcome{Queued: true, Message: fmt.Sprintf("%s went back to work; queued its switch to %s/%s for when it ends the turn", core.Short(rec.Session), dest.Harness, dest.ID)}, nil
		}
		live, m.Crew = fresh, nil
		for _, c := range live.Crew {
			if c.Status == core.Busy {
				m.Crew = append(m.Crew, busyCrew{ID: c.ID, Name: c.Name})
			}
		}
	}
	if err = saveMigration(dir, m); err != nil {
		return none, err
	}
	source := rec
	moved, err := l.portRecord(rec, live, m)
	if err != nil {
		if errors.Is(err, errPortRefused) && live.ID != "" {
			return none, errors.Join(err, hold())
		}
		return none, fmt.Errorf("handoff failed: %w; original conversation: %s", err, profileResume(source))
	}
	once := l
	once.resumeNote, once.operation = "", moved.Session
	msg, err := once.revive(moved)
	if err != nil {
		msg = fmt.Sprintf("resume failed: %v; ym revive %s tries again", err, moved.Key)
	}
	return core.SwitchOutcome{Message: fmt.Sprintf("ported %s to %s/%s as %s: %s", core.Short(source.Session), dest.Harness, dest.ID, core.Short(moved.Session), msg)}, nil
}

// portRecord ports m's conversation unless a past attempt already has, stopping live first, then points the record and everything
// waiting on the old id at the new session. A saved Head is the port already written, so a retry never writes a second one.
func (l launcher) portRecord(rec launch.Record, live core.Live, m migration) (launch.Record, error) {
	if l.core == nil || l.core.PreparePort == nil {
		return rec, fmt.Errorf("%w: no profile-aware porter", errPortRefused)
	}
	dir := filepath.Dir(l.records.Dir)
	old, dest := rec.Session, m.Destination
	if m.Head == "" {
		session := core.Session{Harness: rec.Harness, ID: old, Profile: rec.Profile, ConfigRoot: rec.ConfigRoot, Cwd: rec.Worktree.Path}
		if live.ID != "" {
			session = live.Session
		}
		plan, err := l.core.PreparePort(session, dest.Harness, dest.ID)
		if err != nil {
			return rec, fmt.Errorf("%w: %w", errPortRefused, err)
		}
		defer plan.Close()
		if live.ID != "" {
			if _, err = l.core.Stop(live); err != nil {
				return rec, fmt.Errorf("%w: %w", errPortRefused, err)
			}
			m.Since = time.Now()
		}
		m.State = "prepared"
		if err = saveMigration(dir, m); err != nil {
			return rec, err
		}
		if m.Head, err = plan.Write(); err != nil {
			return rec, err
		}
		if err = saveMigration(dir, m); err != nil {
			return rec, err
		}
	}
	return l.finishPort(rec, m)
}

// portWritten reports a port to another harness whose new session exists but whose links may not all point at it yet.
func (m migration) portWritten() bool {
	return m.State != "running" && m.Head != "" && m.Destination.Harness != m.Source.Harness
}

// finishPort points the record and everything waiting on the old id at the written port, from whichever step an earlier attempt
// stopped at: each step holds when repeated, and the migration says running only once they all have.
func (l launcher) finishPort(rec launch.Record, m migration) (launch.Record, error) {
	dir := filepath.Dir(l.records.Dir)
	old, dest := m.Source.Session, m.Destination
	if rec.Session != m.Head {
		note := switchNote(m, backgroundTasks(rec, m.Crew))
		setRecordProfile(&rec, dest)
		rec.Harness, rec.Session, rec.Model, rec.Handoff, rec.HandoffFrom, rec.Dedicated = dest.Harness, m.Head, "", note, 0, false
		if err := l.records.Save(rec); err != nil {
			return rec, err
		}
	}
	if err := l.core.Store.ContinueSession(old, m.Head, dest.Harness, dest.ID, dest.Root); err != nil {
		return rec, err
	}
	m.State = "running"
	if err := saveMigration(dir, m); err != nil {
		return rec, err
	}
	return rec, settleFailover(dir, m.Source.Harness, old, dest)
}

// finishWritten finishes a written port under the conversation's operation lock; the record is found by its key, as it may already
// name the new session.
func (l launcher) finishWritten(m migration) (launch.Record, error) {
	release, err := profile.Operation(filepath.Dir(l.records.Dir), m.Source.Harness, m.Source.Session)
	if err != nil {
		return launch.Record{}, err
	}
	defer release()
	rec, ok := l.records.Find(m.Source.Key)
	if !ok {
		rec = m.Source
	}
	return l.finishPort(rec, m)
}

// recoverPort finishes a port an earlier attempt left part way and brings the session back, unless it has since been ended.
func (l launcher) recoverPort(m migration) error {
	rec, err := l.finishWritten(m)
	if errors.Is(err, profile.ErrOperationBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, state := l.state(rec); state == "running" || state == core.LaunchUnknown || rec.IsLanded() || rec.Killed.After(m.Since) {
		return nil
	}
	once := l
	once.resumeNote, once.operation = "", rec.Session
	_, err = once.revive(rec)
	return err
}

// portedTo is the session that carries session's conversation now, finishing a port an earlier attempt left part way, so a resume of
// the old id never reads as a session ym does not know and start a new conversation beside the ported one.
func (l launcher) portedTo(session string) (string, error) {
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(l.records.Dir), "migrations", "*-"+session+".json"))
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return session, err
		}
		var m migration
		if err = json.Unmarshal(raw, &m); err != nil {
			return session, err
		}
		if !m.portWritten() {
			continue
		}
		if _, err = l.finishWritten(m); err != nil {
			return session, err
		}
	}
	if l.core == nil || l.core.Store == nil {
		return session, nil
	}
	continued, err := l.core.Store.Continued()
	if err != nil {
		return session, err
	}
	for seen := map[string]bool{session: true}; continued[session] != "" && !seen[continued[session]]; {
		session = continued[session]
		seen[session] = true
	}
	return session, nil
}
