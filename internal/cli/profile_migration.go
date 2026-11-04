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

const (
	waitingBlocker = "the session waits at the quota dialog: "
	heldOff        = "ym is off for this session"
	heldThrottled  = "the owner's throttle holds this session"
)

// heldNoLonger reports a blocker that only said why a hold waited, which the hold's end clears.
func (m migration) heldNoLonger() bool { return m.Blocked == heldOff || m.Blocked == heldThrottled }

// showHeld says on the row why a switch is not applying, or clears that.
func (l launcher) showHeld(m migration, why string) error {
	return l.updatePending(m.Source.Harness, m.Source.Session, func(latest *migration) bool {
		if latest.Blocked == why || latest.Destination.ID != m.Destination.ID {
			return false
		}
		latest.Blocked, latest.Fingerprint = why, ""
		return true
	})
}

type migration struct {
	ResumeNote  string          `json:"resume_note,omitempty"`
	Reason      string          `json:"reason,omitempty"`
	Version     int             `json:"version"`
	Source      launch.Record   `json:"source"`
	Destination profile.Profile `json:"destination"`
	// Automatic is a switch ym chose itself, whose port must pass portGate.
	Automatic   bool   `json:"automatic,omitempty"`
	Expected    string `json:"expected_rollout"`
	Head        string `json:"published_head"`
	State       string `json:"state"`
	Blocked     string `json:"blocked,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	// BlockedAt is when serve last tried it, so a host where nothing changes still tries again in time.
	BlockedAt time.Time  `json:"blocked_at,omitzero"`
	NoteRead  bool       `json:"note_read,omitempty"`
	Crew      []busyCrew `json:"crew,omitempty"`
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
	if m.State == "queued" && !l.loginOut(m.Destination).IsZero() {
		return rec, nil
	}
	if m.Destination.Harness != rec.Harness {
		if m.Automatic && m.Head == "" {
			if back, ok, err := l.backToHarness(m, rec); err != nil {
				return rec, err
			} else if ok {
				m = back
			}
		}
	}
	if m.Destination.Harness != rec.Harness {
		return l.portRecord(rec, core.Live{}, m)
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
	if l.core != nil && l.core.RowsUnloaded(b.Fleet) {
		return nil
	}
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
		if m.portWritten() {
			if err = l.recoverPort(m, b); err != nil {
				return err
			}
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
		if l.core.Off(m.Source.Session) {
			if err = l.showHeld(m, heldOff); err != nil {
				return err
			}
			continue
		}
		if b.ThrottleHolds(m.Source.Session) {
			if err = l.showHeld(m, heldThrottled); err != nil {
				return err
			}
			continue
		}
		if m.heldNoLonger() {
			if err = l.showHeld(m, ""); err != nil {
				return err
			}
		}
		if decided, err := l.redecidePort(m, rec); err != nil {
			return err
		} else if decided {
			continue
		}
		if s, ok := b.Session(m.Source.Session); ok {
			if err := l.turnEnded(s, nil); err != nil {
				if err = l.showWaiting(m, s, err); err != nil {
					return err
				}
				continue
			}
		}
		if m.State == "queued" && !l.loginOut(m.Destination).IsZero() {
			if err = l.reroute(m, rec); err != nil {
				return err
			}
			continue
		}
		fingerprint, err := l.migrationFingerprint(m)
		if err != nil {
			return err
		}
		if m.Blocked != "" && m.Fingerprint == fingerprint && (m.BlockedAt.IsZero() || time.Since(m.BlockedAt) < core.ResumeRetry) {
			continue
		}
		ref := m.Source.Key
		if !recorded {
			ref = m.Source.Session
		}
		next := l
		next.queuedTo = m.Destination.ID
		next.toHarness = m.Destination.Harness
		_, attemptErr := next.QueueProfileSwitch(ref, m.Destination.ID, m.Reason)
		if attemptErr != nil && !errors.Is(attemptErr, profile.ErrOperationBusy) {
			err = l.updatePending(m.Source.Harness, m.Source.Session, func(latest *migration) bool {
				if latest.Destination.ID != m.Destination.ID {
					return false
				}
				latest.Blocked, latest.Fingerprint, latest.BlockedAt = attemptErr.Error(), fingerprint, time.Now()
				return true
			})
			if err != nil {
				return err
			}
			if m.State != "queued" && l.core != nil {
				text := fmt.Sprintf("agent %s (%s) was stopped for a login switch to %s that has not finished: %v. ym keeps trying; ym revive %s brings it back by hand.", core.Short(m.Source.Session), m.Source.Key, m.Destination.ID, attemptErr, m.Source.Key)
				if err = l.core.NoticeStuckSwitch(b, m.Source.Session, m.Source.AttachedAt.Unix(), text); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// reroute handles a queued switch whose login ran out while it waited: ym's own failover moves on to another usable login, and any
// other waits, showing why, until that login resets. Neither lands on the used-up login.
func (l launcher) reroute(m migration, rec launch.Record) error {
	dir := filepath.Dir(l.records.Dir)
	out := l.loginOut(m.Destination)
	block := func() error {
		return l.updatePending(m.Source.Harness, m.Source.Session, func(latest *migration) bool {
			if latest.Destination.ID != m.Destination.ID {
				return false
			}
			latest.Blocked, latest.Fingerprint = fmt.Sprintf("%s is out of its subscription limit until %s", m.Destination.ID, out.UTC().Format(time.RFC3339)), ""
			return true
		})
	}
	a, err := loadFailover(dir, m.Source.Harness, m.Source.Session)
	if err != nil {
		return err
	}
	if !failoverPending(a) {
		return block()
	}
	ps, err := profile.Load(dir)
	if err != nil {
		return err
	}
	now, limits := l.now(), l.limits()
	target, err := l.destination(dir, a, ps, core.Exhaustion{Harness: m.Source.Harness, Profile: m.Source.Profile, Account: m.Source.Account}, m.Source.Worktree.Path, limits, now)
	if err != nil {
		return block()
	}
	next := l
	next.toHarness = target.Harness
	next.automatic = true
	ref := m.Source.Key
	if rec.Key == "" {
		ref = m.Source.Session
	}
	_, err = next.QueueProfileSwitch(ref, target.ID, m.Reason)
	if err != nil && !errors.Is(err, profile.ErrOperationBusy) {
		a.failed(target.Account, now.Add(core.QuotaRecheck), err)
		if err := saveFailover(dir, m.Source.Harness, a); err != nil {
			return err
		}
		return block()
	}
	return nil
}

// redecidePort puts a failover's queued port through the gate again until it applies: a login of the session's own harness may have come back.
// decided is whether anything but applying it follows.
func (l launcher) redecidePort(m migration, rec launch.Record) (decided bool, err error) {
	if !m.Automatic || m.Head != "" || m.Destination.Harness == m.Source.Harness || m.State != "queued" && m.State != "prepared" {
		return false, nil
	}
	if err = l.gatePort(m.Source, m); err != nil && !errors.Is(err, errPortHeld) {
		return true, err
	}
	back, ok, backErr := l.backToHarness(m, rec)
	if backErr != nil || ok {
		_ = back
		return true, backErr
	}
	if err != nil {
		return true, l.updatePending(m.Source.Harness, m.Source.Session, func(latest *migration) bool {
			latest.Blocked = err.Error()
			return true
		})
	}
	return false, nil
}

// backToHarness turns a port ym chose back onto a usable login of the session's own harness, if one has come back, and saves that.
func (l launcher) backToHarness(m migration, rec launch.Record) (migration, bool, error) {
	dir := filepath.Dir(l.records.Dir)
	ps, err := profile.Load(dir)
	if err != nil {
		return m, false, err
	}
	a, err := loadFailover(dir, m.Source.Harness, m.Source.Session)
	if err != nil {
		return m, false, err
	}
	target, err := failoverTarget(usableLogins(dir, a, ps, l.limits(), l.now()), core.Exhaustion{Harness: m.Source.Harness, Account: m.Source.Account}, m.Source.Worktree.Path)
	if err != nil {
		return m, false, nil
	}
	if _, unanswered := l.loginChecks([]profile.Profile{target}, ""); unanswered[loginKey(target)] {
		return m, false, nil
	}
	m.Destination, m.Automatic, m.Blocked = target, false, ""
	if err = retargetFailover(dir, m.Source.Harness, m.Source.Session, m, target); err != nil {
		return m, false, err
	}
	return m, true, saveMigration(dir, m)
}

// showWaiting says on the row why a switch cannot apply to a session stopped at the quota dialog, which never ends its turn by itself;
// one working or waiting for anything else simply has not ended its turn, and any such blocker from earlier no longer holds.
func (l launcher) showWaiting(m migration, s core.Live, why error) error {
	blocker := ""
	var stuck *core.QuotaStopUnproven
	if errors.As(why, &stuck) {
		blocker = why.Error()
	} else if !strings.HasPrefix(m.Blocked, waitingBlocker) {
		return nil
	}
	if blocker != "" {
		blocker = waitingBlocker + blocker
	}
	return l.updatePending(m.Source.Harness, m.Source.Session, func(latest *migration) bool {
		if latest.Blocked == blocker || latest.Destination.ID != m.Destination.ID {
			return false
		}
		latest.Blocked, latest.Fingerprint = blocker, ""
		return true
	})
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

// atStopReasons are, by harness:session, what a row shows of a session that stopped on its limit. The board shows the reason only while its
// screen shows that stop; once Claude's own prompt is back, scrollback says nothing, and Barred tells how its login still stands.
func (l launcher) atStopReasons(dir string) map[string]core.Switching {
	files, _ := filepath.Glob(filepath.Join(dir, "failovers", "*.json"))
	out := map[string]core.Switching{}
	limits, now := l.limits(), l.now()
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var a failoverAttempt
		if json.Unmarshal(raw, &a) != nil {
			continue
		}
		harness, _, _ := strings.Cut(filepath.Base(path), "-")
		stop := core.Switching{AtStop: true, Account: a.On}
		if a.On != "" {
			login := profile.Profile{Harness: harness, ID: a.State.Profile, Account: a.On}
			if !barredUntil(dir, login, limits, now).IsZero() {
				record, _ := readBar(barPath(dir, login))
				stop.Barred = "stopped at its limit earlier, its login is out, " + barredText(record, harness, a.State.Session)
			}
		}
		stop.Blocked = stop.Barred
		if a.State.Status == "blocked" {
			stop.Blocked = atStopReason(dir, a, harness)
		}
		if stop.Blocked != "" {
			out[harness+":"+a.State.Session] = stop
		}
	}
	return out
}

// PendingSwitches are the login switches not yet running, by harness:session, for the board to show.
func (l launcher) PendingSwitches() map[string]core.Switching {
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(l.records.Dir), "migrations", "*.json"))
	out := l.atStopReasons(filepath.Dir(l.records.Dir))
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m struct {
			Version int    `json:"version"`
			State   string `json:"state"`
			Blocked string `json:"blocked"`
			Head    string `json:"published_head"`
			Source  struct {
				Harness string `json:"harness"`
				Session string `json:"session"`
			} `json:"source"`
			Destination struct {
				ID      string `json:"id"`
				Harness string `json:"harness"`
			} `json:"destination"`
		}
		if json.Unmarshal(raw, &m) != nil || m.Version != 1 || m.State == "running" {
			continue
		}
		to := m.Destination.ID
		if m.Destination.Harness != m.Source.Harness {
			to = m.Destination.Harness + "/" + to
		}
		out[m.Source.Harness+":"+m.Source.Session] = core.Switching{To: to, Blocked: m.Blocked, Stopped: m.State != "queued"}
		if m.Head != "" {
			out[m.Destination.Harness+":"+m.Head] = core.Switching{To: to, Blocked: m.Blocked, Stopped: true}
		}
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
