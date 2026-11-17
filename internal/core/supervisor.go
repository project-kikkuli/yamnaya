package core

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const supervisorSchema = `
CREATE TABLE IF NOT EXISTS ym_supervisor_reports (num INTEGER PRIMARY KEY AUTOINCREMENT, supervisor TEXT NOT NULL, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS ym_supervisor_links (
 supervisor TEXT NOT NULL,
 target TEXT NOT NULL,
 PRIMARY KEY (supervisor, target)
);
CREATE TABLE IF NOT EXISTS ym_supervisor_evidence (
 supervisor TEXT PRIMARY KEY,
 fingerprint TEXT NOT NULL,
 generation INTEGER NOT NULL
);
`

// SupervisorEvidence is the evidence available now, not a claim that work is aligned.
type SupervisorEvidence struct {
	Target      string
	Role        string
	Ask         string
	Standing    string
	Unavailable string     `json:",omitempty"`
	Goal        *Goal      `json:",omitempty"`
	Brief       *Brief     `json:",omitempty"`
	Steer       *Steer     `json:",omitempty"`
	Latest      *GoalEvent `json:",omitempty"`
}

type SupervisorView struct {
	Supervisor     Manager
	OriginalIntent string
	Context        string
	Revision       int
	Targets        []string
	Evidence       []SupervisorEvidence
	Assessments    []SupervisorReport
	Corrections    []Steer
	Reports        []Brief
}

func (s *Store) SupervisorTargets(supervisor string) ([]string, error) {
	rows, err := s.db.Query(`SELECT target FROM ym_supervisor_links WHERE supervisor=? ORDER BY target`, supervisor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []string{}
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func (c *Core) NewSupervisor(name, intent, harness, profile string, targets []string) (Manager, error) {
	if err := c.refuseSupervisorWork(""); err != nil {
		return Manager{}, err
	}
	name, intent = strings.TrimSpace(name), strings.TrimSpace(intent)
	if name == "" || intent == "" {
		return Manager{}, errors.New("a supervisor needs a name and the owner's intent")
	}
	if err := CheckName(name); err != nil {
		return Manager{}, err
	}
	harness, err := managerHarness(harness)
	if err != nil {
		return Manager{}, err
	}
	// Resolve every attachment before recording a session that cannot fulfil its remit.
	b, err := c.supervisorBoard()
	if err != nil {
		return Manager{}, err
	}
	resolved := []string{}
	for _, target := range targets {
		m, err := b.Org.lookup(c.hereFrom(c.Store.Unnicked(target)))
		if err != nil {
			return Manager{}, err
		}
		if m.Role == RoleSupervisor || m.Retired {
			return Manager{}, fmt.Errorf("%s is not an active manager or chief", target)
		}
		resolved = append(resolved, m.ID)
	}
	m, err := c.Store.AddManager(Manager{Role: RoleSupervisor, Name: name, Remit: intent, Harness: harness, Profile: profile, LaunchedAt: c.now()})
	if err != nil {
		return m, err
	}
	m.BriefPath = filepath.Join(c.StateDir, "supervisors", m.ID, "brief.md")
	m.Directory = filepath.Join(c.StateDir, "supervisors", m.ID, "workspace")
	if err := writeBrief(m.BriefPath, "# "+name+"\n\n"+intent+"\n"); err != nil {
		return m, err
	}
	if err := c.Store.SaveManager(m); err != nil {
		return m, err
	}
	for _, target := range resolved {
		if _, err := c.Store.db.Exec(`INSERT OR IGNORE INTO ym_supervisor_links VALUES (?,?)`, m.ID, target); err != nil {
			return m, err
		}
	}
	o, err := c.LoadOrg()
	if err != nil {
		return m, err
	}
	return c.launch(b, o, m), nil
}

func (c *Core) SupervisorAttach(supervisor, target string, detach bool) error {
	b, err := c.supervisorBoard()
	if err != nil {
		return err
	}
	m, err := b.Org.lookup(supervisor)
	if err != nil {
		return err
	}
	if m.Role != RoleSupervisor || m.Host != "" || m.Retired {
		return errors.New("attachment owner must be an active local supervisor")
	}
	target = c.hereFrom(c.Store.Unnicked(target))
	if !detach {
		t, err := b.Org.lookup(target)
		if err != nil {
			return err
		}
		if t.Role == RoleSupervisor || t.Retired {
			return errors.New("supervisors attach only to active managers or chiefs")
		}
		target = t.ID
	} else if t, err := b.Org.lookup(target); err == nil {
		target = t.ID
	}
	if detach {
		_, err = c.Store.db.Exec(`DELETE FROM ym_supervisor_links WHERE supervisor=? AND target=?`, m.ID, target)
	} else {
		_, err = c.Store.db.Exec(`INSERT OR IGNORE INTO ym_supervisor_links VALUES (?,?)`, m.ID, target)
	}
	return err
}

func (c *Core) supervises(supervisor, target string) bool {
	m, err := c.Store.Manager(supervisor)
	if err != nil || m.Role != RoleSupervisor || m.Retired {
		return false
	}
	targets, err := c.Store.SupervisorTargets(supervisor)
	if err != nil {
		return false
	}
	b, err := c.supervisorBoard()
	if err != nil {
		return false
	}
	for _, root := range targets {
		if root == target {
			return true
		}
		if host, _, remote := SplitRemote(root); remote {
			fresh := false
			for _, r := range c.remoteBoards() {
				if r.Host == host {
					fresh = r.Err == nil && !r.Stale && !r.Late(c.now()) && !r.At.IsZero()
				}
			}
			if !fresh {
				continue
			}
		}
		for _, below := range b.Org.Subtree(root) {
			if below.ID == target {
				return true
			}
		}
	}
	return false
}

func (c *Core) Supervisor(ref string) (SupervisorView, error) {
	return c.supervisorView(ref, true)
}

func (c *Core) supervisorView(ref string, history bool) (SupervisorView, error) {
	m, context, revision, err := c.Context(ref)
	if err != nil {
		return SupervisorView{}, err
	}
	if m.Role != RoleSupervisor || m.Host != "" {
		return SupervisorView{}, errors.New("expected a local supervisor")
	}
	targets, err := c.Store.SupervisorTargets(m.ID)
	if err != nil {
		return SupervisorView{}, err
	}
	var corrections []Steer
	var assessments []SupervisorReport
	var reports []Brief
	if history {
		if corrections, err = c.Store.Steers(m.ID); err != nil {
			return SupervisorView{}, err
		}
		if assessments, err = c.Store.SupervisorReports(m.ID); err != nil {
			return SupervisorView{}, err
		}
		if reports, err = c.Store.Briefs(m.ID); err != nil {
			return SupervisorView{}, err
		}
	}

	b, err := c.supervisorBoard()
	if err != nil {
		return SupervisorView{}, err
	}
	briefs, err := c.Store.LatestBriefs()
	if err != nil {
		return SupervisorView{}, err
	}
	steers, err := c.Store.LatestSteers()
	if err != nil {
		return SupervisorView{}, err
	}
	view := SupervisorView{Supervisor: m, OriginalIntent: m.Remit, Context: context, Revision: revision, Targets: targets, Corrections: corrections, Reports: reports, Assessments: assessments}
	seen := map[string]bool{}
	for _, target := range targets {
		ids := []string{target}
		for _, below := range b.Org.Subtree(target) {
			ids = append(ids, below.ID)
		}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			e := SupervisorEvidence{Target: id}
			m, exists := b.Org.Managers[id]
			if !exists {
				e.Unavailable = "target absent from current board"
			} else {
				e.Role, e.Ask = m.Role, b.Org.Ask(m)
				if g, ok := b.Org.Goals[m.GoalID]; ok {
					e.Goal = &g
				}
				if brief, ok := briefs[id]; ok {
					e.Brief = &brief
				}
				if steer, ok := steers[id]; ok {
					e.Steer = &steer
				}
				if n, ok := b.Node(id); ok {
					e.Standing, e.Goal, e.Brief, e.Steer, e.Latest = n.Standing, n.Goal, n.Fresh.Brief, n.Steer, supervisorLatest(n.Feed)
				} else {
					e.Unavailable = "target has no current board row"
				}
			}
			if host, _, remote := SplitRemote(id); remote {
				e.Unavailable = "remote evidence unavailable"
				for _, r := range c.remoteBoards() {
					if r.Host == host {
						_, rest, _ := SplitRemote(id)
						e.Standing, e.Brief = r.Standing[rest], r.Fresh[rest].Brief
						if st, ok := r.Steers[rest]; ok {
							e.Steer = &st
						}
						if g, ok := b.Org.Goals[m.GoalID]; ok {
							e.Goal = &g
							e.Latest = supervisorLatest(r.Feeds[rest])
							if e.Latest == nil {
								_, localGoal, _ := SplitRemote(g.ID)
								e.Latest = supervisorLatest(r.Feeds[localGoal])
							}
						}
						if exists && r.Err == nil && !r.Stale && !r.Late(c.now()) && !r.At.IsZero() {
							e.Unavailable = ""
						} else {
							e.Unavailable = "remote evidence stale or unavailable"
						}
					}
				}
			}
			view.Evidence = append(view.Evidence, e)
		}
	}
	sort.Slice(view.Evidence, func(i, j int) bool { return view.Evidence[i].Target < view.Evidence[j].Target })
	return view, nil
}

func (c *Core) watchSupervisors(b Board, o Org) error {
	for _, m := range o.Ordered() {
		if m.Role != RoleSupervisor || !o.Active(m) || c.Off(m.Session) {
			continue
		}
		if _, ok := c.reach(b, m.Session); !ok {
			continue
		}
		view, err := c.supervisorView(m.ID, false)
		if err != nil {
			return err
		}
		fingerprint, err := supervisorFingerprint(view)
		if err != nil {
			return err
		}
		var previous string
		var generation int
		if err := c.Store.db.QueryRow(`SELECT coalesce((SELECT fingerprint FROM ym_supervisor_evidence WHERE supervisor=?),''), coalesce((SELECT generation FROM ym_supervisor_evidence WHERE supervisor=?),0)`, m.ID, m.ID).Scan(&previous, &generation); err != nil {
			return err
		}
		if previous == fingerprint {
			continue
		}
		if err := c.requestAction(b, m.Session, "supervise", fmt.Sprintf("%d:%s", generation+1, fingerprint), "Attached evidence changed. Read ym supervisor show "+m.ID+"; compare primary evidence against original intent and all corrections. Record an evidence-backed report; steer only within existing authorization. A brief or done claim alone is not proof."); err != nil {
			return err
		}
		if _, err := c.Store.db.Exec(`INSERT INTO ym_supervisor_evidence VALUES (?,?,?) ON CONFLICT(supervisor) DO UPDATE SET fingerprint=excluded.fingerprint, generation=excluded.generation`, m.ID, fingerprint, generation+1); err != nil {
			return err
		}
	}
	return nil
}

func (c *Core) supervisorPrompt(sb *strings.Builder, o Org, m Manager) {
	fmt.Fprintf(sb, "You are %s, a supervisor in yamnaya (ym). You retain the owner's intent, inspect evidence and report alignment. You are separate from the chief/manager execution hierarchy.\n\nOriginal owner intent:\n%s\n\n", m.Shown(), m.Remit)
	c.planNow(sb, o, m)
	fmt.Fprintf(sb, "Read ym supervisor show %s for attachments, original intent, current corrections, evidence freshness and prior reports. Read ym goal context --json %s and ym goal context --all %s for context history.\n", m.ID, m.ID, m.ID)
	sb.WriteString("Read primary evidence (diffs, tests, artifacts, review findings), not just worker summaries. Distinguish observed facts, inference and unavailable evidence; never claim alignment from stale remote snapshots. Record an assessment using ym supervisor report <your-id> --assessment <findings> --uncertainty <gaps-or-none> --evidence <primary-source>, repeating --evidence for each source. This preserves the observed snapshot and context revision. Keep a concise current brief using ym goal brief. Preserve original intent and every correction when interpreting the current context.\n")
	sb.WriteString("You may steer attached managers and chiefs using ym goal steer --text <correction> <target>; steering is attributed to you and requires adoption. Keep every correction within the owner's existing authorization. Never approve new work, merges, spending or scope; escalate missing decisions to the owner. Do not implement, start workers/goals/chiefs, reparent reports, close goals or grant authority. Attachments do not make you the workflow owner. Other agents keep working when you are absent; they never wait for your approval.\n")
	c.briefRules(sb, m)
	c.steerRules(sb, m)
	sb.WriteString(wakeRules)
	sb.WriteString("Concrete evidence changes create durable actions; inspect, report or steer, then complete the action. Do not poll or schedule recurring checks for unchanged evidence. The owner may talk to you directly. Record their corrections using ym supervisor remember <your-id> --text <correction> --source <conversation-reference>. Your record remains attributed to you; it is not a grant of new authority.\n")
}

func (c *Core) supervisorBoard() (Board, error) {
	b, err := c.Board()
	if err == nil && !c.ShowRemotes {
		c.mergeRemoteOrg(&b.Org, c.remoteBoards(), nil)
	}
	return b, err
}

func (c *Core) refuseSupervisorWork(session string) error {
	sessions := []string{session}
	if c.Requester != nil {
		sessions = append(sessions, c.Requester())
	}
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	for _, candidate := range sessions {
		if candidate == "" {
			continue
		}
		if m, ok := o.ManagerOf(c.Resolve(candidate)); ok && m.Role == RoleSupervisor {
			return errors.New("supervisors inspect, report and steer; implementation and delegation belong to managers and chiefs")
		}
	}
	return nil
}

func supervisorFingerprint(view SupervisorView) (string, error) {
	type signal struct {
		Target, Ask, Unavailable, Failure, State, Evidence, Summary string
		WorkRevision                                                int64
		Brief, Steer                                                int
		Adopted                                                     bool
		Event                                                       int64
	}
	signals := []signal{}
	for _, e := range view.Evidence {
		s := signal{Target: e.Target, Ask: e.Ask, Unavailable: e.Unavailable}
		if e.Standing == StandStuck || e.Standing == StandDown {
			s.Failure = e.Standing
		}
		if e.Goal != nil {
			s.State = e.Goal.State
			s.WorkRevision = e.Goal.WorkRevision
			s.Evidence = e.Goal.Evidence
			s.Summary = e.Goal.Summary
		}
		if e.Brief != nil {
			s.Brief = e.Brief.Version
		}
		if e.Steer != nil {
			s.Steer = e.Steer.Revision
			s.Adopted = e.Steer.Adopted()
		}
		if e.Latest != nil {
			switch e.Latest.Kind {
			case EventIdleAgain, EventNudge, EventRefreshed, EventMachineThrottled, EventRelayed, EventTimer:
			default:
				s.Event = e.Latest.Seq
			}
		}
		signals = append(signals, s)
	}
	raw, err := json.Marshal(struct {
		Revision int
		Signals  []signal
	}{view.Revision, signals})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func supervisorLatest(feed Feed) *GoalEvent {
	var latest *GoalEvent
	events := append([]GoalEvent{}, feed.Recent...)
	if feed.Latest != nil {
		events = append(events, *feed.Latest)
	}
	for _, e := range events {
		switch e.Kind {
		case EventIdleAgain, EventNudge, EventRefreshed, EventMachineThrottled, EventRelayed, EventTimer:
			continue
		}
		if latest == nil || e.Seq > latest.Seq {
			copy := e
			latest = &copy
		}
	}
	return latest
}
