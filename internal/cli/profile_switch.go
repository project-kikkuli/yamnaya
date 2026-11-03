package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func (l launcher) SwitchProfile(ref, id string) (string, error) {
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
	var wait error
	if live.ID != "" {
		if err := l.switchReady(live, rec, nil); err != nil {
			var later unsettled
			if !errors.As(err, &later) {
				return "", err
			}
			wait = err
		}
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
		switch {
		case pending.State == migrationQueued:
			if err := os.Remove(migrationPath(filepath.Dir(l.records.Dir), rec.Harness, rec.Session)); err != nil {
				return "", err
			}
			return fmt.Sprintf("dropped pending switch of %s to %s; it stays on %s", core.Short(rec.Session), pending.Destination.ID, dest.ID), nil
		case pending.Version != 0 && pending.State != "running" && pending.Destination.ID == dest.ID:
			rec = pending.Source
		default:
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
	dir := filepath.Dir(l.records.Dir)
	prior, err := loadMigration(dir, rec.Harness, rec.Session)
	if err != nil {
		return "", err
	}
	inFlight := prior.Version != 0 && prior.State != "running" && prior.State != migrationQueued
	if inFlight && prior.Destination.ID != dest.ID {
		return "", fmt.Errorf("finish pending handoff to %s first", prior.Destination.ID)
	}
	if wait != nil {
		if inFlight {
			return "", wait
		}
		if prior.State != migrationQueued || prior.Destination.ID != dest.ID {
			if err = saveMigration(dir, migration{Version: 1, ResumeNote: l.resumeNote, Source: rec, Destination: dest, State: migrationQueued}); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("%s: switch pending -> %s; ym serve applies it once the session is idle at its prompt (%v)", core.Short(rec.Session), dest.ID, wait), nil
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
	source := rec
	m := prior
	if !inFlight {
		m = migration{Version: 1, ResumeNote: l.resumeNote, Source: rec, Destination: dest, Expected: expected, State: migrationQueued}
		if m.ResumeNote == "" && prior.State == migrationQueued && prior.Destination.ID == dest.ID {
			m.ResumeNote = prior.ResumeNote
		}
		if err = saveMigration(dir, m); err != nil {
			return "", err
		}
	}
	block := func(err error) error {
		latest, loadErr := loadMigration(dir, rec.Harness, rec.Session)
		if loadErr == nil && latest.State == migrationQueued {
			latest.Blocked = err.Error()
			loadErr = saveMigration(dir, latest)
		}
		return errors.Join(err, loadErr)
	}
	children, err := l.nativeLifecycle(rec, dest, true)
	if err != nil {
		return "", block(err)
	}
	if rec.Harness == "codex" {
		probe, err := writerLock(dest.Root, rec.Session)
		if err != nil {
			return "", block(err)
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
				return "", err
			}
			live = fresh
		}
		m.Running = runningWork(live)
	}
	if m.State == migrationQueued {
		m.State = "prepared"
	}
	m.Blocked = ""
	if err = saveMigration(dir, m); err != nil {
		return "", err
	}
	if live.ID != "" {
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
	m.State = "running"
	if err = saveMigration(dir, m); err != nil {
		return "", err
	}
	return fmt.Sprintf("switched %s to %s/%s: %s", core.Short(rec.Session), rec.Harness, dest.ID, msg), nil
}

// runningWork names the background tasks and subagents s had running: what its next process must re-check.
func runningWork(s core.Live) []string {
	seen := map[string]bool{}
	var out []string
	for id, a := range s.Running {
		seen[id] = true
		out = append(out, fmt.Sprintf("%s (task %s)", a, id))
	}
	for _, c := range s.Crew {
		if c.Status == core.Busy && !seen[c.ID] {
			out = append(out, fmt.Sprintf("subagent %s (%s)", c.Name, c.ID))
		}
	}
	slices.Sort(out)
	return out
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
