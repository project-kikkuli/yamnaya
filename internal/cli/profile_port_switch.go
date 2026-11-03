package cli

import (
	"errors"
	"fmt"
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
	m.inherit(prior)
	if prior.Head != "" && prior.State != "running" && prior.Destination.ID == dest.ID && prior.Destination.Harness == dest.Harness {
		m = prior
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
	note := switchNote(m, backgroundTasks(rec, m.Crew))
	setRecordProfile(&rec, dest)
	rec.Harness, rec.Session, rec.Model, rec.Handoff, rec.HandoffFrom, rec.Dedicated = dest.Harness, m.Head, "", note, 0, false
	if err := l.records.Save(rec); err != nil {
		return rec, err
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
