package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

const ownerSwitchReason = "the owner asked for it"

func (l launcher) SwitchProfile(ref, id string) (string, error) {
	out, err := l.QueueProfileSwitch(ref, id, ownerSwitchReason)
	return out.Message, err
}

// switchedOntoWall is when the login the owner chose comes back, zero while it has usage left.
func (l launcher) switchedOntoWall(ref, id string) time.Time {
	rec, ok := l.records.Find(ref)
	if !ok {
		return time.Time{}
	}
	ps, err := profile.Load(filepath.Dir(l.records.Dir))
	if err != nil {
		return time.Time{}
	}
	p, err := profile.Find(ps, rec.Harness, id)
	if err != nil {
		return time.Time{}
	}
	return l.loginOut(p)
}

// QueueProfileSwitch moves the session ref names to login id, keeping its conversation: at once when the session sits between
// turns, otherwise saved for ym serve to apply at its next turn end. reason goes into the note the resumed session reads.
func (l launcher) QueueProfileSwitch(ref, id, reason string) (core.SwitchOutcome, error) {
	out, err := l.switchProfile(ref, id, reason)
	if err != nil && !errors.Is(err, profile.ErrOperationBusy) {
		l.blockSwitch(ref, id, err)
	}
	if err == nil && id != "auto" {
		if until := l.switchedOntoWall(ref, id); !until.IsZero() {
			out.Message += fmt.Sprintf("; login %s is out of its subscription limit until %s", id, until.UTC().Format(time.RFC3339))
		}
	}
	return out, err
}

// remoteSwitch asks host's ym to switch ref, which holds the record and the queue.
func (l launcher) remoteSwitch(host, ref, id, reason string) (core.SwitchOutcome, error) {
	args := []string{"switch", ref, "--profile", id, "--json"}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	raw, err := l.core.RunRemote(host, args...)
	if err != nil {
		return core.SwitchOutcome{}, err
	}
	var out core.SwitchOutcome
	if json.Unmarshal([]byte(raw), &out) != nil {
		out.Message = raw
	}
	return out, nil
}

// blockSwitch records err on the pending switch of ref to id, so the board shows why it has not applied.
func (l launcher) blockSwitch(ref, id string, err error) {
	rec, ok := l.records.Find(ref)
	if !ok {
		return
	}
	l.updatePending(rec.Harness, rec.Session, func(m *migration) bool {
		if m.State == "running" || id != "auto" && m.Destination.ID != id {
			return false
		}
		m.Blocked = err.Error()
		return true
	})
}

// updatePending changes the session's migration record under its operation lock; a held lock or absent record changes nothing.
func (l launcher) updatePending(harness, session string, change func(*migration) bool) error {
	dir := filepath.Dir(l.records.Dir)
	release, err := profile.Operation(dir, harness, session)
	if errors.Is(err, profile.ErrOperationBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	m, err := loadMigration(dir, harness, session)
	if err != nil || m.Version == 0 || !change(&m) {
		return err
	}
	return saveMigration(dir, m)
}

func (l launcher) switchProfile(ref, id, reason string) (core.SwitchOutcome, error) {
	var none core.SwitchOutcome
	if l.core != nil {
		if host, rest, ok := l.core.RemoteSession(ref); ok {
			return l.remoteSwitch(host, rest, id, reason)
		}
	}
	rec, recorded := l.records.Find(ref)
	var live core.Live
	if l.core != nil {
		lookup := ref
		if recorded && rec.Session != "" {
			lookup = rec.Harness + ":" + profile.ID(rec.Profile) + "@" + rec.Session
		}
		s, err := l.core.Find(lookup)
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			return none, err
		}
		if err == nil {
			live = s
			if s.Remote != "" {
				return l.remoteSwitch(s.Remote, s.ID, id, reason)
			}
		}
	}
	if !recorded && live.ID == "" {
		return none, fmt.Errorf("no session %q to switch", ref)
	}
	var midTurn error
	if live.ID != "" {
		if err := switchIdentity(live, rec); err != nil {
			return none, err
		}
		midTurn = l.turnEnded(live, nil)
		if !recorded {
			rec = launch.Adopted(live, time.Now())
		}
		if live.Model != "" {
			rec.Model = live.Model
		}
	}
	if rec.ConfigRoot == "" {
		ps, err := profile.Load(filepath.Dir(l.records.Dir))
		if err != nil {
			return none, err
		}
		p, err := profile.Find(ps, rec.Harness, rec.Profile)
		if err != nil {
			return none, err
		}
		setRecordProfile(&rec, p)
	}
	if err := recordProfile(rec).DefaultConstraint(); err != nil {
		return none, err
	}
	release, err := profile.Operation(filepath.Dir(l.records.Dir), rec.Harness, rec.Session)
	if err != nil {
		return none, err
	}
	defer release()
	l.operation = rec.Session
	if l.queuedTo != "" {
		current, err := loadMigration(filepath.Dir(l.records.Dir), rec.Harness, rec.Session)
		if err != nil {
			return none, err
		}
		if current.Version == 0 || current.State == "running" || current.Destination.ID != l.queuedTo || current.State == "queued" && live.ID == "" {
			return core.SwitchOutcome{Message: fmt.Sprintf("the switch of %s to %s was replaced or cancelled", core.Short(rec.Session), l.queuedTo)}, nil
		}
	}
	harness := rec.Harness
	if l.toHarness != "" {
		harness = l.toHarness
	}
	dest, err := l.selectProfile(harness, id)
	if id == "auto" {
		ps, e := profile.Load(filepath.Dir(l.records.Dir))
		if e != nil {
			return none, e
		}
		source := recordProfile(rec)
		a, e := loadFailover(filepath.Dir(l.records.Dir), rec.Harness, rec.Session)
		if e != nil {
			return none, e
		}
		if source.Account == "" {
			return none, fmt.Errorf("current account identity is unknown; select a profile explicitly")
		}
		limits, now := l.limits(), l.now()
		dest, err = failoverTarget(usableLogins(filepath.Dir(l.records.Dir), a, ps, limits, now), core.Exhaustion{Harness: rec.Harness, Account: source.Account}, limits, now, rec.Worktree.Path)
	}
	if err != nil {
		return none, err
	}
	if dest.Root == rec.ConfigRoot {
		pending, err := loadMigration(filepath.Dir(l.records.Dir), rec.Harness, rec.Session)
		if err != nil {
			return none, err
		}
		if pending.State == "queued" {
			if err = l.closeQueued(rec.Harness, rec.Session, "the owner cancelled the switch to "+pending.Destination.ID); err != nil {
				return none, err
			}
			return core.SwitchOutcome{Message: fmt.Sprintf("cancelled the queued switch of %s to %s; it stays on %s", core.Short(rec.Session), pending.Destination.ID, dest.ID)}, nil
		}
		if pending.Version != 0 && pending.State != "running" && pending.Destination.ID == dest.ID {
			rec = pending.Source
		} else {
			return none, fmt.Errorf("session already uses profile %s", dest.ID)
		}
	}
	if err = switchAuthentication(dest, rec.Worktree.Path); err != nil {
		return none, err
	}

	if l.core != nil {
		b, err := l.core.Board()
		if err != nil {
			return none, err
		}
		for _, s := range b.Sessions {
			if s.Remote == "" && s.Harness == rec.Harness && s.ID == rec.Session && profile.ID(s.Profile) != profile.ID(rec.Profile) {
				return none, fmt.Errorf("destination conversation is already running; source is unchanged")
			}
		}
	}
	if midTurn != nil {
		return l.queueSwitch(rec, dest, reason)
	}
	_, state := l.state(rec)
	if live.ID == "" && (state == "running" || state == core.LaunchUnknown) {
		return none, fmt.Errorf("cannot prove session idle; source is unchanged")
	}
	if dest.Harness != rec.Harness {
		return l.portSwitch(rec, live, dest, reason)
	}
	var expected string
	if rec.Harness == "codex" {
		expected, err = selectedRollout(sqliteRoot(dest), rec.Session)
		if err != nil {
			return none, err
		}
	}
	dir := filepath.Dir(l.records.Dir)
	source := rec
	m := migration{Version: 1, ResumeNote: l.resumeNote, Reason: reason, Source: rec, Destination: dest, Expected: expected, State: "prepared", Since: time.Now()}
	prior, err := loadMigration(dir, rec.Harness, rec.Session)
	if err != nil {
		return none, err
	}
	m.inherit(prior)
	if err = retargetFailover(dir, rec.Harness, rec.Session, prior, dest); err != nil {
		return none, err
	}
	inFlight := prior.Version != 0 && prior.State != "running" && prior.State != "queued"
	if inFlight {
		if prior.Destination.ID != dest.ID {
			return none, fmt.Errorf("finish pending handoff to %s first", prior.Destination.ID)
		}
		m = prior
	}
	// A live source is untouched until its Stop, so a refusal up to there leaves the switch queued.
	hold := func() error {
		if inFlight && m.State != "prepared" || live.ID == "" {
			return saveMigration(dir, m)
		}
		m.State = "queued"
		return saveMigration(dir, m)
	}
	children, err := l.nativeLifecycle(rec, dest, true)
	if err == nil && rec.Harness == "codex" {
		var probe func()
		if probe, err = writerLock(dest.Root, rec.Session); err == nil {
			probe()
		}
	}
	if err != nil {
		return none, errors.Join(err, hold())
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
			return core.SwitchOutcome{Queued: true, Message: fmt.Sprintf("%s went back to work; queued its switch to %s/%s for when it ends the turn", core.Short(rec.Session), rec.Harness, dest.ID)}, nil
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
	if live.ID != "" {
		if err = l.drainNative(children, live.PID); err != nil {
			return none, errors.Join(err, hold())
		}
		if _, err = l.core.Stop(live); err != nil {
			return none, errors.Join(err, hold())
		}
		m.Since = time.Now()
		if err = saveMigration(dir, m); err != nil {
			return none, err
		}
	}
	if err = l.awaitDrain(children); err != nil {
		return none, err
	}
	rec, err = l.finishMigration(m)
	if err != nil {
		return none, fmt.Errorf("handoff failed: %w; original conversation: %s", err, profileResume(source))
	}

	once := l
	once.resumeNote = ""
	msg, err := once.revive(rec)
	if err != nil {
		return none, fmt.Errorf("profile %s selected, but resume failed: %w; original conversation: %s", dest.ID, err, profileResume(source))
	}
	m, err = loadMigration(dir, rec.Harness, rec.Session)
	if err != nil {
		return none, err
	}
	m.State, m.Blocked = "running", ""
	if err = saveMigration(dir, m); err != nil {
		return none, err
	}
	if err = settleFailover(dir, rec.Harness, rec.Session, dest); err != nil {
		return none, err
	}
	return core.SwitchOutcome{Message: fmt.Sprintf("switched %s to %s/%s: %s", core.Short(rec.Session), rec.Harness, dest.ID, msg)}, nil
}

// queueSwitch saves the switch of rec to dest for ym serve to apply when the session next ends its turn.
func (l launcher) queueSwitch(rec launch.Record, dest profile.Profile, reason string) (core.SwitchOutcome, error) {
	var none core.SwitchOutcome
	dir := filepath.Dir(l.records.Dir)
	prior, err := loadMigration(dir, rec.Harness, rec.Session)
	if err != nil {
		return none, err
	}
	if prior.Version != 0 && prior.State != "running" && prior.State != "queued" {
		if prior.Destination.ID != dest.ID {
			return none, fmt.Errorf("finish pending handoff to %s first", prior.Destination.ID)
		}
		return core.SwitchOutcome{Queued: true, Message: fmt.Sprintf("the switch of %s to %s/%s is already under way", core.Short(rec.Session), rec.Harness, dest.ID)}, nil
	}
	m := migration{Version: 1, ResumeNote: l.resumeNote, Reason: reason, Source: rec, Destination: dest, State: "queued", Since: time.Now()}
	m.inherit(prior)
	if err = retargetFailover(dir, rec.Harness, rec.Session, prior, dest); err != nil {
		return none, err
	}
	if err = saveMigration(dir, m); err != nil {
		return none, err
	}
	return core.SwitchOutcome{Queued: true, Message: fmt.Sprintf("%s is mid-turn; queued its switch to %s/%s for when it ends the turn", core.Short(rec.Session), rec.Harness, dest.ID)}, nil
}

func profileResume(rec launch.Record) string {
	args, err := resumeArgv(rec, "")
	if err != nil {
		return "ym revive " + rec.Key
	}
	var quoted []string
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return "cd " + shellQuote(rec.Worktree.Path) + " && " + strings.Join(quoted, " ")
}
