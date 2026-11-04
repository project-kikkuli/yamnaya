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

// errPortHeld is portGate's answer where a port would be made; nothing has happened to the session.
var errPortHeld = errors.New("ym holds the port")

// gatePort is the one test every port ym chose passes as it is made, however it got here.
func (l launcher) gatePort(rec launch.Record, m migration) error {
	dir := filepath.Dir(l.records.Dir)
	a, err := loadFailover(dir, rec.Harness, rec.Session)
	if err != nil {
		return err
	}
	ps, err := profile.Load(dir)
	if err != nil {
		return err
	}
	e := core.Exhaustion{Harness: m.Source.Harness, Profile: m.Source.Profile, Account: m.Source.Account}
	if err = l.portGate(dir, a, ps, e, l.limits(), l.now()); err != nil {
		return fmt.Errorf("%w: %w", errPortHeld, err)
	}
	return nil
}

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
	m := migration{Version: 1, ResumeNote: l.resumeNote, Reason: reason, Source: rec, Destination: dest, State: "prepared", Since: time.Now(), Automatic: l.automatic}
	if prior.Version != 0 && prior.State != "running" && prior.Destination.ID == dest.ID && prior.Destination.Harness == dest.Harness {
		m.Automatic = m.Automatic || prior.Automatic
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
		msg = fmt.Sprintf("resume failed: %v; ym serve tries again", err)
	} else if m, err = loadMigration(dir, source.Harness, source.Session); err != nil {
		return none, err
	} else {
		m.State = "running"
		if err = saveMigration(dir, m); err != nil {
			return none, err
		}
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
		if m.Automatic {
			if err := l.gatePort(rec, m); err != nil {
				return rec, err
			}
		}
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
// stopped at: each step holds when repeated. The migration says running only once the session has come back.
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

// recoverPort finishes a port an earlier attempt left part way and brings the session back, once nothing holds it: ym off, the owner's
// throttle or a destination that has run out each wait, saying so on the row, and a session since ended stays ended.
func (l launcher) recoverPort(m migration, b core.Board) error {
	dir := filepath.Dir(l.records.Dir)
	rec, err := l.finishWritten(m)
	if errors.Is(err, profile.ErrOperationBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	done := func() error {
		m.State, m.Blocked = "running", ""
		return saveMigration(dir, m)
	}
	_, state := l.state(rec)
	if state == core.LaunchUnknown {
		return nil
	}
	if state == "running" || rec.IsLanded() || rec.Killed.After(m.Since) {
		return done()
	}
	stuck := func(why string) error {
		text := fmt.Sprintf("agent %s (%s) was stopped for a login switch to %s, and its port is written but cannot come back yet: %s. ym keeps trying; ym revive %s brings it back by hand.", core.Short(rec.Session), rec.Key, m.Destination.ID, why, rec.Key)
		return errors.Join(l.core.NoticeStuckSwitch(b, rec.Session, m.Source.AttachedAt.Unix(), text), l.holdPending(m, why))
	}
	if l.core.Off(rec.Session) {
		return stuck(heldOff)
	}
	if b.ThrottleHolds(rec.Session) || b.ThrottleHolds(m.Source.Session) {
		return stuck(heldThrottled)
	}
	if l.core.RowsUnloaded(b.Fleet) {
		return l.holdPending(m, errRowsUnread.Error())
	}
	if out := l.loginOut(m.Destination); !out.IsZero() {
		return stuck(fmt.Sprintf("%s is out of its subscription limit until %s", m.Destination.ID, quotaReset(out)))
	}
	once := l
	once.resumeNote, once.operation = "", rec.Session
	if _, err = once.revive(rec); err != nil {
		return stuck("resume failed: " + err.Error())
	}
	return done()
}

// holdPending shows why a written port has not come back yet; recoverPort tries again next pass.
func (l launcher) holdPending(m migration, why string) error {
	if m.Blocked == why {
		return nil
	}
	m.Blocked = why
	return saveMigration(filepath.Dir(l.records.Dir), m)
}

// isPort reports whether session is the far end of a port, so a lost conversation there is an error and never a fresh start.
func (l launcher) isPort(session string) bool {
	if l.core == nil || l.core.Store == nil {
		return false
	}
	continued, err := l.core.Store.Continued()
	if err != nil {
		return true
	}
	for _, next := range continued {
		if next == session {
			return true
		}
	}
	return false
}

// handoffs are the migration records by launch key, read once per sweep.
func (l launcher) handoffs() map[string]migration {
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(l.records.Dir), "migrations", "*.json"))
	out := map[string]migration{}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m migration
		if json.Unmarshal(raw, &m) == nil && m.Version != 0 {
			out[m.Source.Key] = m
		}
	}
	return out
}

// pendingHandoff reports a handoff under way whose stopped source must not be cleaned up: the switch itself stopped it, and landing it
// would drop the note and crew the handoff still has to deliver. A queued switch has not stopped anything, and an owner's kill ends it.
func pendingHandoff(m migration, rec launch.Record) bool {
	return m.Version != 0 && m.State != "running" && m.State != "queued" && !rec.Killed.After(m.Since)
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
