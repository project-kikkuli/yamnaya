package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func (l launcher) SwitchProfile(ref, id string) (string, error) {
	return l.QueueProfileSwitch(ref, id, "the owner asked for it")
}

// QueueProfileSwitch moves the session ref names to login id, keeping its conversation: at once when the session sits between
// turns, otherwise saved for ym serve to apply at its next turn end. reason goes into the note the resumed session reads.
func (l launcher) QueueProfileSwitch(ref, id, reason string) (string, error) {
	msg, err := l.switchProfile(ref, id, reason)
	if err != nil {
		l.blockSwitch(ref, id, err)
	}
	return msg, err
}

// blockSwitch records err on the pending switch of ref to id, so the board shows why it has not applied.
func (l launcher) blockSwitch(ref, id string, err error) {
	rec, ok := l.records.Find(ref)
	if !ok {
		return
	}
	dir := filepath.Dir(l.records.Dir)
	m, loadErr := loadMigration(dir, rec.Harness, rec.Session)
	if loadErr != nil || m.Version == 0 || m.State == "running" || id != "auto" && m.Destination.ID != id {
		return
	}
	m.Blocked = err.Error()
	saveMigration(dir, m)
}

func (l launcher) switchProfile(ref, id, reason string) (string, error) {
	if l.core != nil {
		if host, rest, ok := l.core.RemoteSession(ref); ok {
			return l.core.RunRemote(host, "switch", rest, "--profile", id)
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
			return "", err
		}
		if err == nil {
			live = s
			if s.Remote != "" {
				return l.core.RunRemote(s.Remote, "switch", s.ID, "--profile", id)
			}
		}
	}
	if !recorded && live.ID == "" {
		return "", fmt.Errorf("no session %q to switch", ref)
	}
	var midTurn error
	if live.ID != "" {
		if err := switchIdentity(live, rec); err != nil {
			return "", err
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
			return "", err
		}
		p, err := profile.Find(ps, rec.Harness, rec.Profile)
		if err != nil {
			return "", err
		}
		setRecordProfile(&rec, p)
	}
	if err := recordProfile(rec).DefaultConstraint(); err != nil {
		return "", err
	}
	release, err := profile.Operation(filepath.Dir(l.records.Dir), rec.Harness, rec.Session)
	if err != nil {
		return "", err
	}
	defer release()
	l.operation = rec.Session
	dest, err := l.selectProfile(rec.Harness, id)
	if id == "auto" {
		ps, e := profile.Load(filepath.Dir(l.records.Dir))
		if e != nil {
			return "", e
		}
		source := recordProfile(rec)
		a, e := loadFailover(filepath.Dir(l.records.Dir), rec.Harness, rec.Session)
		if e != nil {
			return "", e
		}
		if source.Account == "" {
			return "", fmt.Errorf("current account identity is unknown; select a profile explicitly")
		}
		dest, err = failoverTarget(ps, core.Exhaustion{Harness: rec.Harness, Account: source.Account}, profileLimits(filepath.Dir(l.records.Dir)), a.Pools, time.Now(), rec.Worktree.Path)
	}
	if err != nil {
		return "", err
	}
	if err = switchAuthentication(dest, rec.Worktree.Path); err != nil {
		return "", err
	}
	if dest.Root == rec.ConfigRoot {
		pending, err := loadMigration(filepath.Dir(l.records.Dir), rec.Harness, rec.Session)
		if err != nil {
			return "", err
		}
		if pending.State == "queued" {
			if err = os.Remove(migrationPath(filepath.Dir(l.records.Dir), rec.Harness, rec.Session)); err != nil {
				return "", err
			}
			return fmt.Sprintf("cancelled the queued switch of %s to %s; it stays on %s", core.Short(rec.Session), pending.Destination.ID, dest.ID), nil
		}
		if pending.Version != 0 && pending.State != "running" && pending.Destination.ID == dest.ID {
			rec = pending.Source
		} else {
			return "", fmt.Errorf("session already uses profile %s", dest.ID)
		}
	}

	if l.core != nil {
		b, err := l.core.Board()
		if err != nil {
			return "", err
		}
		for _, s := range b.Sessions {
			if s.Remote == "" && s.Harness == rec.Harness && s.ID == rec.Session && profile.ID(s.Profile) != profile.ID(rec.Profile) {
				return "", fmt.Errorf("destination conversation is already running; source is unchanged")
			}
		}
	}
	if midTurn != nil {
		return l.queueSwitch(rec, dest, reason)
	}
	_, state := l.state(rec)
	if live.ID == "" && (state == "running" || state == core.LaunchUnknown) {
		return "", fmt.Errorf("cannot prove session idle; source is unchanged")
	}
	var expected string
	if rec.Harness == "codex" {
		expected, err = selectedRollout(sqliteRoot(dest), rec.Session)
		if err != nil {
			return "", err
		}
	}
	dir := filepath.Dir(l.records.Dir)
	source := rec
	m := migration{Version: 1, ResumeNote: l.resumeNote, Reason: reason, Source: rec, Destination: dest, Expected: expected, State: "prepared"}
	prior, err := loadMigration(dir, rec.Harness, rec.Session)
	if err != nil {
		return "", err
	}
	m.inherit(prior)
	if prior.Version != 0 && prior.State != "running" && prior.State != "queued" {
		if prior.Destination.ID != dest.ID {
			return "", fmt.Errorf("finish pending handoff to %s first", prior.Destination.ID)
		}
		m = prior
	} else if err = saveMigration(dir, m); err != nil {
		return "", err
	}
	children, err := l.nativeLifecycle(rec, dest, true)
	if err != nil {
		return "", err
	}
	if rec.Harness == "codex" {
		probe, err := writerLock(dest.Root, rec.Session)
		if err != nil {
			return "", err
		}
		probe()
	}
	if live.ID != "" {
		quotaWaiting := live.Status == core.Waiting
		for _, cp := range live.Copies {
			quotaWaiting = quotaWaiting || cp.Status == core.Waiting
		}
		if quotaWaiting {
			fresh, err := l.core.Find(rec.Harness + ":" + profile.ID(rec.Profile) + "@" + rec.Session)
			if err != nil {
				return "", err
			}
			if err = l.switchReady(fresh, rec, nil); err != nil {
				if m.State != "prepared" {
					return "", err
				}
				m.State = "queued"
				if err = saveMigration(dir, m); err != nil {
					return "", err
				}
				return fmt.Sprintf("%s went back to work; queued its switch to %s/%s for when it ends the turn", core.Short(rec.Session), rec.Harness, dest.ID), nil
			}
			live = fresh
		}
		if err = l.drainNative(children, live.PID); err != nil {
			return "", err
		}
		if _, err = l.core.Stop(live); err != nil {
			return "", err
		}
	}
	if err = l.awaitDrain(children); err != nil {
		return "", err
	}
	rec, err = l.finishMigration(m)
	if err != nil {
		return "", fmt.Errorf("handoff failed: %w; original conversation: %s", err, profileResume(source))
	}

	msg, err := l.revive(rec)
	if err != nil {
		return "", fmt.Errorf("profile %s selected, but resume failed: %w; original conversation: %s", dest.ID, err, profileResume(source))
	}
	m, err = loadMigration(dir, rec.Harness, rec.Session)
	if err != nil {
		return "", err
	}
	m.State, m.Blocked = "running", ""
	if err = saveMigration(dir, m); err != nil {
		return "", err
	}
	return fmt.Sprintf("switched %s to %s/%s: %s", core.Short(rec.Session), rec.Harness, dest.ID, msg), nil
}

// queueSwitch saves the switch of rec to dest for ym serve to apply when the session next ends its turn.
func (l launcher) queueSwitch(rec launch.Record, dest profile.Profile, reason string) (string, error) {
	dir := filepath.Dir(l.records.Dir)
	prior, err := loadMigration(dir, rec.Harness, rec.Session)
	if err != nil {
		return "", err
	}
	if prior.Version != 0 && prior.State != "running" && prior.State != "queued" {
		if prior.Destination.ID != dest.ID {
			return "", fmt.Errorf("finish pending handoff to %s first", prior.Destination.ID)
		}
		return fmt.Sprintf("the switch of %s to %s/%s is already under way", core.Short(rec.Session), rec.Harness, dest.ID), nil
	}
	m := migration{Version: 1, ResumeNote: l.resumeNote, Reason: reason, Source: rec, Destination: dest, State: "queued"}
	m.inherit(prior)
	if err = saveMigration(dir, m); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s is mid-turn; queued its switch to %s/%s for when it ends the turn", core.Short(rec.Session), rec.Harness, dest.ID), nil
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
