package core

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/work"
)

func (c *Core) ObserveWork(b Board) error {
	all, err := c.Store.Work.List()
	if err != nil {
		return err
	}
	agents, err := c.Store.Work.Agents()
	if err != nil {
		return err
	}
	byAgentSession := map[string]string{}
	for _, a := range agents {
		if a.Session != "" {
			byAgentSession[a.Session] = a.Workspace
		}
	}
	byOrigin := map[string]work.Scope{}
	bySession := map[string]work.Scope{}
	for _, w := range all {
		byOrigin[w.Origin] = w
		for session := range w.Bindings {
			bySession[session] = w
		}
	}
	alive := map[string]bool{}
	for _, p := range b.Projects {
		if len(p.Sessions) == 0 {
			continue
		}
		origin := ThisHost() + "/" + p.Key
		w, ok := byOrigin[origin]
		for _, session := range p.Sessions {
			if id := byAgentSession[session.Key()]; id != "" {
				w, err = c.Store.Work.Get(id)
				if err != nil {
					return err
				}
				ok = true
				break
			}
		}
		if !ok {
			for _, session := range p.Sessions {
				if found, exists := bySession[session.Key()]; exists {
					w, ok = found, true
					break
				}
			}
		}
		if !ok {
			w, err = c.Store.Work.Ensure(origin, p.Repo, p.Name)
			if err != nil {
				return err
			}
		}

		for _, session := range p.Sessions {
			alive[session.Key()] = true
			if _, bound := w.Bindings[session.Key()]; !bound {
				w, err = c.Store.Work.Commit(work.Event{ID: "bind/" + w.ID + "/" + session.Key(), Work: w.ID, Expected: w.Revision, Type: "bind", Target: session.Key(), Actor: "discovery", Execution: &work.Execution{Harness: session.Harness, Directory: session.Cwd}})
				if err != nil {
					return err
				}
				if err = c.Store.Work.AttachDefaults(w); err != nil {
					return err
				}
			}
			observed := work.ExecutionObservation{State: session.Status.String(), Result: session.Result}
			if w.Observations[session.Key()] != observed {
				w, err = c.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "execution", Target: session.Key(), Actor: "discovery", Observation: observed, Text: "Native session " + session.Key() + " is " + observed.State + "\n" + observed.Result})
				if err != nil {
					return err
				}
			}
		}

		if w.Purpose == "" && p.Sessions[0].Task != "" {
			_, err = c.Store.Work.Commit(work.Event{ID: "purpose/" + w.ID, Work: w.ID, Expected: w.Revision, Type: "purpose", Text: p.Sessions[0].Task, Actor: "discovery"})
			if err != nil {
				return err
			}
		}
	}

	latest, err := c.Store.Work.List()
	if err != nil {
		return err
	}
	for _, w := range latest {
		for session, observed := range w.Observations {
			if !alive[session] && observed.State != "exited" {
				observed.State = "exited"
				w, err = c.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "execution", Target: session, Actor: "discovery", Observation: observed, Text: "Native session " + session + " exited; assess its result evidence and retain unfinished responsibility"})
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (c *Core) attachWork(b *Board) error {
	b.WorkErrors, _ = c.Store.Work.Coverage("@github")
	all, err := c.Store.Work.List()
	if err != nil {
		return err
	}
	byOrigin := map[string]*work.Scope{}
	bySession := map[string]*work.Scope{}
	for i := range all {
		w := &all[i]
		byOrigin[w.Origin] = w
		for session := range w.Bindings {
			bySession[session] = w
		}
	}
	attached := map[string]bool{}
	for i := range b.Projects {
		p := &b.Projects[i]
		w := byOrigin[ThisHost()+"/"+p.Key]
		if w == nil {
			for _, s := range p.Sessions {
				if w = bySession[s.Key()]; w != nil {
					break
				}
			}
		}
		if w != nil {
			p.Work = w
			attached[w.ID] = true
		}
	}
	for i := range all {
		w := &all[i]
		if !attached[w.ID] {
			b.Projects = append(b.Projects, Project{Key: "work:" + w.ID, Name: w.Name, Repo: w.Repo, RepoName: workRepoName(w.Repo), Work: w})
		}
	}
	peers, err := c.Store.Work.Peers()
	if err != nil {
		return err
	}
	for _, peer := range peers {
		if peer.Error != "" && len(peer.Scopes) == 0 {
			b.WorkErrors = append(b.WorkErrors, peer.Host+": "+peer.Error)
		}
		pending, rejected, err := c.Store.Work.RemoteStatus(peer.Host)
		if err != nil {
			return err
		}
		for _, scope := range peer.Scopes {
			w := scope
			w.Pending += pending[scope.ID]
			w.CoverageErrors = append(w.CoverageErrors, rejected[scope.ID]...)
			w.Host = peer.Host
			w.ID = peer.Host + "/" + w.ID
			w.Changes = max(0, w.Revision-c.Store.Work.LastSeen(w.ID))
			name := w.Name
			if peer.Error != "" {
				name += " (host unavailable)"
				w.CoverageErrors = append(w.CoverageErrors, peer.Error)
			}
			b.Projects = append(b.Projects, Project{Key: "work:" + w.ID, Name: name, Repo: peer.Host + ":" + w.Repo, RepoName: workRepoName(w.Repo) + " @ " + peer.Host, Work: &w})
		}
	}
	return nil
}

func workRepoName(repo string) string {
	if repo == "" {
		return "(no repo)"
	}
	if filepath.Base(repo) == ".git" {
		return filepath.Base(filepath.Dir(repo))
	}
	return filepath.Base(strings.TrimSuffix(repo, ".git"))
}

func (c *Core) WorkFor(b Board, query string) (work.Scope, error) {
	for _, p := range b.Projects {
		if p.Work != nil && (p.Work.ID == query || p.Key == query) {
			return *p.Work, nil
		}
	}
	if w, err := c.Store.Work.Find(query); err == nil {
		return w, nil
	}
	key, err := b.ProjectKey(query)
	if err != nil {
		return work.Scope{}, err
	}
	if p, ok := b.Project(key); ok && p.Work != nil {
		return *p.Work, nil
	}
	if err = c.ObserveWork(b); err != nil {
		return work.Scope{}, err
	}
	return c.Store.Work.Get(ThisHost() + "/" + key)
}

func (c *Core) WorkCycle(b Board) error {
	if err := c.ObserveWork(b); err != nil {
		return err
	}
	all, err := c.Store.Work.List()
	if err != nil {
		return err
	}
	for _, w := range all {
		if !w.WakeAt.IsZero() && !w.WakeAt.After(c.now()) {
			_, err = c.Store.Work.Commit(work.Event{ID: fmt.Sprintf("wake/%s/%d", w.ID, w.WakeAt.UnixNano()), Work: w.ID, Expected: w.Revision, Type: "wake", Actor: "trigger", At: c.now()})
			if err != nil {
				return err
			}
		}
	}
	return c.DeliverWork(b)
}

func (c *Core) DeliverWork(b Board) error {
	return c.Store.Work.Deliver(func(session, text string) (deliveryErr error) {
		defer func() {
			if deliveryErr != nil {
				c.Store.Work.RunResult(session, deliveryErr)
			}
		}()
		s, ok := b.Session(c.Resolve(session))
		if !ok {
			if c.Wake != nil {
				return c.Wake(session, text)
			}
			return fmt.Errorf("session is not reachable")
		}
		if len(s.Copies) > 1 {
			return fmt.Errorf("session has multiple processes")
		}
		message := "Work update available. Read ym --session " + session + " work inbox before continuing.\n" + text
		if c.BackgroundSend != nil {
			return c.BackgroundSend(s, message)
		}
		return c.message(s.Session, message, false)
	})
}

func (c *Core) ChangeWork(w work.Scope, e work.Event) (work.Scope, error) {
	e.Work, e.Expected = w.ID, w.Revision
	if e.Type == "context" {
		e.Context = w.Context.Revision
	}
	if e.Type == "decide" || e.Type == "feedback" || e.Type == "follow-through" || e.Type == "reviewed" || e.Type == "unreview" {
		e.Review = w.Reviews[e.Target]
		e.Context = w.Context.Revision
	}
	if w.Host == "" {
		return c.ApplyWork(e)
	}
	e.ID = fmt.Sprintf("%s/%d", ThisHost(), time.Now().UnixNano())
	e.Work = strings.TrimPrefix(w.ID, w.Host+"/")
	return w, c.Store.Work.QueueRemote(w.Host, e)
}

func (c *Core) ApplyWork(e work.Event) (work.Scope, error) {
	if c.Store.Work.HasEvent(e.ID) {
		return c.Store.Work.Commit(e)
	}
	w, err := c.Store.Work.Get(e.Work)
	if err != nil {
		return w, err
	}
	if (e.Type == "feedback" || e.Type == "decide" || e.Type == "reviewed") && c.CheckReview != nil {
		r, ok := w.Reviews[e.Target]
		if !ok {
			return w, fmt.Errorf("review not found")
		}
		for _, source := range w.Sources {
			if source.URL != r.URL {
				continue
			}
			current, err := c.CheckReview(source)
			if err != nil {
				return w, err
			}
			if current.Version != e.Review.Version {
				_, err = c.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "observe", Actor: "source", Review: current, PreviousVersion: r.Version})
				if err != nil {
					return w, err
				}
				return w, fmt.Errorf("source changed; refresh and reconcile the review before judgment")
			}
		}
	}
	return c.Store.Work.Commit(e)
}

func attachAgentUsage(b *Board) {
	for i := range b.Projects {
		p := &b.Projects[i]
		if p.Work == nil || (p.Work.Kind != "chief" && p.Work.Kind != "agent") {
			continue
		}
		for _, a := range b.Agents {
			if a.Workspace != p.Work.ID {
				continue
			}
			if len(p.Sessions) == 0 {
				p.Status = Idle
				if a.Running {
					p.Status = Busy
				}
			}
			counted := map[string]bool{}
			for _, s := range p.Sessions {
				counted[s.Key()] = true
			}
			for _, d := range p.Dormant {
				counted[d.ID()] = true
			}
			for _, id := range append(a.Sessions, a.Session) {
				if id != "" && !counted[id] {
					p.Spend = p.Spend.Plus(b.Fleet.Sessions[id].Total)
					counted[id] = true
				}
			}
		}
	}
}
