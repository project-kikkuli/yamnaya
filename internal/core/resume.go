package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// ResumePace is the least time between two sessions ym brings back by itself, so a host that restarts with many goals running
// resumes them one at a time.
const ResumePace = 10 * time.Second

// Resumer continues the sessions ym launched, each in its own worktree.
type Resumer interface {
	// Resume continues session in its own conversation with prompt as its next message or, when its transcript is gone, starts it
	// over on start in the same worktree, or on its first prompt when start is empty; resumed says which, and now is the session
	// it runs as, a new id when Codex starts over. ErrNotFound when ym launched no such session.
	Resume(session, prompt, start string) (now string, resumed bool, err error)
	// Bring resumes s, a session that ran outside ym's server, on ym's server in its own directory and conversation.
	Bring(s Live, prompt string) error
}

// recovery is what ym serve keeps between cycles about the sessions it brings back: when it last started one, the agents it
// tried since they last ran, and the cold start it is reporting on.
type recovery struct {
	mu    sync.Mutex
	last  time.Time
	tried map[string]bool
	// cold is set from the cycle that finds every launched session gone until that recovery is reported.
	cold, reported bool
	notes          []resumeNote
	// alive is what aliveFile holds; orphans are the sessions a cold start brings back from it.
	alive, orphans []Running
	// wrote is when aliveFile was last written; it is rewritten at least every freshAlive/4, so its age says when serve last looked.
	wrote time.Time
}

// resumeNote is one session a cold start brought back, or failed to.
type resumeNote struct {
	who, session string
	at           time.Time
	// fresh is set when its conversation was gone and it started over.
	fresh bool
	// failure says why it is not back, empty while it is or may still come up.
	failure string
	settled bool
}

// slot reports whether ym may bring a session back now, taking the slot when it may.
func (c *Core) slot() bool {
	now := c.now()
	r := &c.recovery
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.last.IsZero() && !now.Before(r.last) && now.Sub(r.last) < ResumePace {
		return false
	}
	r.last = now
	return true
}

func (c *Core) note(n resumeNote) {
	r := &c.recovery
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cold {
		n.at = c.now()
		r.notes = append(r.notes, n)
	}
}

// resumeText is what a resumed manager or chief reads first: why it was stopped, then what it missed while down.
func resumeText(o Org, m Manager, waiting []Message, events []GoalEvent) string {
	var sb strings.Builder
	sb.WriteString("ym: this session stopped (its host restarted or its process died) and ym resumed it in the same conversation. Pick up where you left off; your brief is at " + m.BriefPath + ".")
	if len(waiting) > 0 {
		sb.WriteString(" Messages that arrived while you were down, oldest first:")
		for _, msg := range waiting {
			sb.WriteString("\n- " + oneLine(msg.Text))
		}
	}
	if len(events) > 0 {
		sb.WriteString("\n\nWhile you were down, " + wakeOrChief(o, m, events))
	}
	return sb.String()
}

// workResumeText is what a resumed agent reads first.
const workResumeText = "ym: this session stopped (its host restarted or its tmux server ended) and ym resumed it in the same conversation. If you were in the middle of something, carry on; if your work was done, report it as you would have and stay idle."

// resumeManager continues m's own conversation when ym launched it and can; ok is false when there is nothing of it to resume.
func (c *Core) resumeManager(o Org, m Manager, waiting []Message, events []GoalEvent, start string) (Manager, bool) {
	r, can := c.Launches.(Resumer)
	if !can || m.Session == "" {
		return m, false
	}
	now, resumed, err := r.Resume(m.Session, resumeText(o, m, waiting, events), start)
	if errors.Is(err, ErrNotFound) {
		return m, false
	}
	who := managerWho(m)
	if err != nil {
		m.LastError = "resume failed: " + err.Error()
		c.Store.SaveManager(m)
		c.note(resumeNote{who: who, session: m.Session, failure: err.Error(), settled: true})
		if g, ok := o.Goals[m.GoalID]; ok {
			c.RecordGoalEvent(g.ID, EventResumeFailed, fmt.Sprintf("%s could not be resumed in session %s: %v", who, Short(m.Session), err),
				"resume-failed\x00"+m.Session+"\x00"+err.Error())
		}
		return m, true
	}
	c.note(resumeNote{who: who, session: m.Session, fresh: !resumed})
	m.Session, m.LastError = now, ""
	c.Store.SaveManager(m)
	return m, true
}

func managerWho(m Manager) string {
	if m.Role == RoleChief {
		return "chief " + m.ID
	}
	return "manager " + m.ID
}

// GoalWork is the active manager or chief whose work a session is, directly or through the sessions that spawned it, or that runs
// as it, so ym brings it back by itself when its pane goes; ok is false for a session of no active goal's or chief's.
func (c *Core) GoalWork() func(session, parent string) (Manager, bool) {
	none := func(string, string) (Manager, bool) { return Manager{}, false }
	o, err := c.LoadOrg()
	if err != nil {
		return none
	}
	lin, err := c.Store.Lineage()
	if err != nil {
		return none
	}
	parents := maps.Clone(lin.Parent)
	return func(session, parent string) (Manager, bool) {
		if session == "" {
			return Manager{}, false
		}
		if _, ok := parents[session]; !ok && parent != "" && parent != session {
			parents[session] = parent
		}
		return o.Managed(session, parents)
	}
}

// tellWork records what happened to an agent on the goal of m, the manager it works under, or tells m when m is a chief.
func (c *Core) tellWork(b Board, m Manager, session, kind, text, key string) error {
	if _, ok := b.Org.Goals[m.GoalID]; ok {
		_, _, err := c.RecordGoalEvent(m.GoalID, kind, text, key)
		return err
	}
	return c.noticeAgent(b, b.Org, session, kind, text, key)
}

// ReviveWork resumes, one per ResumePace, every session ym brings back by itself: an agent of an active goal or chief whose pane
// went without ym landing or killing it, any session ym launched that was running when its tmux server went away, and, after a cold
// start, every session the owner started elsewhere that was running before it. A reboot brings work back and never lands it.
// Managers and chiefs come back through Supervise; nothing of an ended goal or a retired manager comes back.
func (c *Core) ReviveWork(b Board) error {
	r, can := c.Launches.(Resumer)
	if !can {
		return nil
	}
	kills, err := c.Store.Kills(timeZero)
	if err != nil {
		return err
	}
	work := c.GoalWork()
	var errs []error
	for _, l := range b.Launched {
		if l.State == "running" {
			c.recovery.mu.Lock()
			delete(c.recovery.tried, l.Session)
			c.recovery.mu.Unlock()
		}
		if l.State != "gone" || !l.Landed.IsZero() || !l.Killed.IsZero() || l.Session == "" {
			continue
		}
		m, ok := work(l.Session, l.Parent)
		if _, killed := kills[l.Session]; killed || (!ok && (!l.Revive || m.ID != "")) || (ok && m.Session == l.Session) || c.Off(l.Session) || b.throttle.Holds(l.Session) || !c.take(l.Session) {
			continue
		}
		if !ok {
			who := "session " + Short(l.Session) + " (" + l.Key + ")"
			_, resumed, err := r.Resume(l.Session, workResumeText, "")
			c.noteResult(who, l.Session, resumed, err)
			continue
		}
		who := "agent " + Short(l.Session) + " (" + l.Key + ")"
		_, resumed, err := r.Resume(l.Session, workResumeText, "")
		c.noteResult(who, l.Session, resumed, err)
		if err != nil {
			errs = append(errs, c.tellWork(b, m, l.Session, EventResumeFailed,
				fmt.Sprintf("%s's pane went and ym could not resume it: %v. Its worktree %s keeps what it wrote; ym revive %s tries again.", who, err, l.Path, l.Key),
				"resume-failed\x00"+l.Session+"\x00"+err.Error()))
			continue
		}
		how := "resumed in its own conversation"
		if !resumed {
			how = "started over on its first prompt, since its conversation was gone"
		}
		errs = append(errs, c.tellWork(b, m, l.Session, EventResumed, fmt.Sprintf("%s's pane went and ym %s.", who, how),
			"resumed\x00"+l.Session+"\x00"+c.now().Format(time.RFC3339)))
	}
	c.recovery.mu.Lock()
	orphans := slices.Clone(c.recovery.orphans)
	c.recovery.mu.Unlock()
	for _, o := range orphans {
		if _, live := b.Session(o.ID); live || c.Off(o.ID) || b.throttle.Holds(o.ID) || !c.take(o.ID) {
			continue
		}
		err := r.Bring(Live{Session: Session{ID: o.ID, Harness: o.Harness, Profile: o.Profile, ConfigRoot: o.ConfigRoot, Cwd: o.Cwd}}, workResumeText)
		c.noteResult("session "+Short(o.ID)+" in "+o.Cwd, o.ID, true, err)
	}
	return errors.Join(errs...)
}

// take reports whether ym may try to bring session back now: it has not tried this serve, and the pace allows one now.
func (c *Core) take(session string) bool {
	c.recovery.mu.Lock()
	tried := c.recovery.tried[session]
	c.recovery.mu.Unlock()
	if tried || !c.slot() {
		return false
	}
	c.recovery.mu.Lock()
	defer c.recovery.mu.Unlock()
	if c.recovery.tried == nil {
		c.recovery.tried = map[string]bool{}
	}
	c.recovery.tried[session] = true
	return true
}

func (c *Core) noteResult(who, session string, resumed bool, err error) {
	if err != nil {
		c.note(resumeNote{who: who, session: session, failure: err.Error(), settled: true})
		return
	}
	c.note(resumeNote{who: who, session: session, fresh: !resumed})
}

// aliveFile lists the sessions running outside ym's server at ym serve's last look, so a cold start knows what the owner had open.
const aliveFile = "alive.json"

// Running is a session that ran outside ym's tmux server when ym serve last looked.
type Running struct {
	Profile    string `json:"profile,omitempty"`
	ConfigRoot string `json:"config_root,omitempty"`
	ID         string `json:"id"`
	Harness    string `json:"harness"`
	Cwd        string `json:"cwd"`
}

// running lists b's sessions on this host that ym did not launch and could resume: they have an id, a directory and a terminal.
func running(b Board) []Running {
	launched := map[string]bool{}
	for _, l := range b.Launched {
		launched[l.Session] = true
	}
	var out []Running
	for _, s := range b.Sessions {
		if s.Remote != "" || s.Background || s.ID == "" || s.Cwd == "" || launched[s.ID] {
			continue
		}
		out = append(out, Running{ID: s.ID, Harness: s.Harness, Profile: s.Profile, ConfigRoot: s.ConfigRoot, Cwd: s.Cwd})
	}
	slices.SortFunc(out, func(a, b Running) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// trackRunning keeps aliveFile current while ym's server runs, and leaves it as the last host's state while every launch is gone.
func (c *Core) trackRunning(b Board) {
	if c.StateDir == "" || cold(b) {
		return
	}
	now := running(b)
	c.recovery.mu.Lock()
	same := slices.Equal(now, c.recovery.alive) && c.recovery.alive != nil && time.Since(c.recovery.wrote) < freshAlive/4
	c.recovery.mu.Unlock()
	if same {
		return
	}
	raw, err := json.Marshal(now)
	if err != nil {
		return
	}
	path := filepath.Join(c.StateDir, aliveFile)
	if os.WriteFile(path+".new", raw, 0o600) == nil && os.Rename(path+".new", path) == nil {
		c.recovery.mu.Lock()
		c.recovery.alive, c.recovery.wrote = now, time.Now()
		c.recovery.mu.Unlock()
	}
}

// freshAlive bounds how old aliveFile may be, on a host that did not restart since, for what it lists to have just gone down.
const freshAlive = 2 * time.Minute

// lastRunning is what aliveFile says ran before the host went down, less what runs again now, what ym kill stopped and what ran in
// the worktree of a launch ym killed or landed, as a stray conversation of an ended manager does. It counts
// only when written before the host last booted, or moments ago, so a serve started long after its last look brings back nothing.
func (c *Core) lastRunning(b Board) []Running {
	path := filepath.Join(c.StateDir, aliveFile)
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if at := info.ModTime(); !at.Before(c.booted()) && time.Since(at) > freshAlive {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var was []Running
	if json.Unmarshal(raw, &was) != nil {
		return nil
	}
	kills, err := c.Store.Kills(timeZero)
	if err != nil {
		return nil
	}
	ended := map[string]bool{}
	for _, l := range b.Launched {
		if l.Path != "" && !ended[l.Path] {
			ended[l.Path] = !l.Killed.IsZero() || !l.Landed.IsZero()
		}
	}
	for _, l := range b.Launched {
		if l.Killed.IsZero() && l.Landed.IsZero() {
			ended[l.Path] = false
		}
	}
	return slices.DeleteFunc(was, func(r Running) bool {
		_, live := b.Session(r.ID)
		_, killed := kills[r.ID]
		return live || killed || ended[r.Cwd]
	})
}

// cold reports whether every session ym launched on this host and has not landed is gone, as after a restart of the host: its
// tmux server is not running.
func cold(b Board) bool {
	n := 0
	for _, l := range b.Launched {
		if !l.Landed.IsZero() {
			continue
		}
		if l.State != "gone" {
			return false
		}
		n++
	}
	return n > 0
}

// watchCold opens a cold start's report on the cycle that finds every launched session gone, taking the sessions the owner had
// running elsewhere to bring back; one report covers it until a pane runs again.
func (c *Core) watchCold(b Board) {
	r := &c.recovery
	r.mu.Lock()
	defer r.mu.Unlock()
	if !cold(b) {
		if r.reported {
			r.reported = false
		}
		return
	}
	if !r.cold && !r.reported {
		r.cold, r.notes = true, nil
		if c.StateDir != "" {
			r.orphans = c.lastRunning(b)
		}
	}
}

// reportCold raises one desktop notification once every session a cold start brought back is up or has failed, and nothing is left
// to bring back: what resumed in its own conversation, what started over, and what could not come back.
func (c *Core) reportCold(b Board) error {
	r := &c.recovery
	r.mu.Lock()
	if !r.cold {
		r.mu.Unlock()
		return nil
	}
	now := c.now()
	for i := range r.notes {
		n := &r.notes[i]
		if n.settled {
			continue
		}
		if _, ok := b.Session(n.session); ok {
			n.settled = true
			continue
		}
		for _, l := range b.Launched {
			if l.Session == n.session && l.State != "gone" && !l.MayRun() {
				n.failure, n.settled = "its agent "+exitText(l.State)+" right after resuming", true
			}
		}
		if !n.settled && now.Sub(n.at) >= StartGrace {
			n.failure, n.settled = "it did not come up within "+StartGrace.String(), true
		}
	}
	busy := !r.last.IsZero() && now.Sub(r.last) < ResumePace
	for _, n := range r.notes {
		busy = busy || !n.settled
	}
	for _, o := range r.orphans {
		_, live := b.Session(o.ID)
		busy = busy || (!live && !r.tried[o.ID])
	}
	if busy || c.awaitingResume(b) {
		r.mu.Unlock()
		return nil
	}
	notes := r.notes
	r.cold, r.reported, r.notes, r.orphans = false, true, nil, nil
	r.mu.Unlock()
	if len(notes) == 0 {
		return nil
	}
	title, body := coldText(notes)
	return c.notify(title, body)
}

// awaitingResume reports whether a manager or chief ym brings back is still down and not waiting out a backoff after a failure.
func (c *Core) awaitingResume(b Board) bool {
	for _, m := range b.Org.Ordered() {
		if !b.Org.Active(m) || m.Host != "" || b.Alive(m) || c.now().Before(m.NextAttempt) {
			continue
		}
		return true
	}
	return false
}

func coldText(notes []resumeNote) (string, string) {
	var back, fresh, failed []string
	for _, n := range notes {
		switch {
		case n.failure != "":
			failed = append(failed, n.who+" ("+n.failure+")")
		case n.fresh:
			fresh = append(fresh, n.who)
		default:
			back = append(back, n.who)
		}
	}
	var parts []string
	if len(back) > 0 {
		parts = append(parts, "Resumed in their own conversations: "+strings.Join(back, ", ")+".")
	}
	if len(fresh) > 0 {
		parts = append(parts, "Started over, their conversations gone: "+strings.Join(fresh, ", ")+".")
	}
	if len(failed) > 0 {
		parts = append(parts, "Could not resume: "+strings.Join(failed, ", ")+".")
	}
	title := fmt.Sprintf("ym: %d of %d sessions back after a restart", len(back)+len(fresh), len(notes))
	return title, strings.Join(parts, " ")
}

// booted is when this host last started, zero when it cannot tell.
func (c *Core) booted() time.Time {
	if c.Boot != nil {
		return c.Boot()
	}
	return bootTime()
}
