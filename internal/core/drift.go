package core

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/drift"
)

// OffRepoFiles is how many files an agent must write in other repositories before it counts as working outside its own.
const OffRepoFiles = 3

// placeDrift keeps the signs of a session that still runs and of crew still at work, and adds writes landing outside the session's
// repository. It runs after place, so each session knows its worktree.
func (c *Core) placeDrift(found []Session, now time.Time) {
	for i := range found {
		s := &found[i]
		if s.Status == Done || s.Status == Moved {
			s.Drift = nil
		} else if sign, ok := c.offRepo(*s, now); ok {
			s.Drift = append(s.Drift, sign)
		}
		for j := range s.Crew {
			if s.Crew[j].Status != Busy {
				s.Crew[j].Drift = nil
			}
		}
	}
}

// offRepo reports a session that wrote OffRepoFiles or more files in repositories other than the one it runs in; another clone of its
// own repository counts as its own.
func (c *Core) offRepo(s Session, now time.Time) (drift.Sign, bool) {
	own := s.RepoID
	if own == "" {
		own = s.RepoKey
	}
	if own == "" {
		return drift.Sign{}, false
	}
	elsewhere := map[string][]string{}
	n := 0
	for _, f := range s.Files {
		top := c.worktreeOf(f)
		if top == "" {
			continue
		}
		g := c.gitFor(top, now)
		if g.Common == "" || g.ID() == own || g.Common == s.RepoKey {
			continue
		}
		elsewhere[g.Name()] = append(elsewhere[g.Name()], filepath.Base(f))
		n++
	}
	if n < OffRepoFiles {
		return drift.Sign{}, false
	}
	var repos []string
	for r, fs := range elsewhere {
		repos = append(repos, fmt.Sprintf("%s (%s)", r, strings.Join(fs[:min(3, len(fs))], ", ")))
	}
	sort.Strings(repos)
	return drift.Sign{Kind: drift.OffRepo, Count: n, At: s.Doing.At,
		Evidence: fmt.Sprintf("runs in %s but wrote %d files in %s", s.Repo, n, strings.Join(repos, "; "))}, true
}

// Adrift reports whether any agent in the node, its manager's own session included, shows a sign of going nowhere.
func (n Node) Adrift() bool {
	if n.Live != nil && len(n.Live.Drift) > 0 {
		return true
	}
	for _, a := range n.Agents {
		if a.Adrift() {
			return true
		}
	}
	return false
}

// Adrift reports whether the session or any of its crew shows a sign of going nowhere.
func (l Live) Adrift() bool {
	if len(l.Drift) > 0 {
		return true
	}
	for _, m := range l.Crew {
		if len(m.Drift) > 0 {
			return true
		}
	}
	return false
}

// NoticeDrift records, once per episode, each sign that a goal's agent, its manager, or a crew member of either is going nowhere, as an
// event on the goal: it wakes the manager with the evidence and reaches its chiefs. An agent of a chief is told to the chief. A session
// no manager runs is shown on the board and left to the owner.
func (c *Core) NoticeDrift(b Board) error {
	o := b.Org
	var errs []error
	for _, s := range b.Sessions {
		key := s.Key()
		who := "agent " + Short(key)
		if m, isManager := o.ManagerOf(key); isManager {
			who = "manager " + m.ID
		} else if s.Title != "" {
			who += " (" + oneLine(s.Title) + ")"
		}
		notice := func(who, id string, sign drift.Sign) {
			ep := "adrift\x00" + id + "\x00" + sign.Kind + "\x00" + sign.Since.UTC().Format(time.RFC3339Nano)
			if err := c.noticeAgent(b, o, key, EventAgentAdrift, driftText(who, sign), ep); err != nil {
				errs = append(errs, err)
			}
		}
		for _, sign := range s.Drift {
			notice(who, key, sign)
		}
		for _, m := range s.Crew {
			for _, sign := range m.Drift {
				notice("crew member "+m.Name+" of "+who, key+"/"+m.ID, sign)
			}
		}
	}
	return errors.Join(errs...)
}

var driftAdvice = map[string]string{
	drift.Looping:    "It keeps retrying without a new diagnosis: find out why, then redirect it or remove the blocker.",
	drift.Compacting: "Its context keeps refilling before it can act: give it a narrower task or restart it with a fresh brief.",
	drift.OffRepo:    "Its writes land outside its own repository: check it is working where its goal needs it.",
}

func driftText(who string, s drift.Sign) string {
	return who + " looks adrift, " + s.Kind + ": " + s.Evidence + ". " + driftAdvice[s.Kind]
}
