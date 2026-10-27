package launch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// Stopper ends an agent that runs in a terminal ym does not own, once it sits idle, so ym can resume it on its own server.
type Stopper struct {
	// Look re-reads the session; core.ErrNotFound once no process runs it.
	Look func() (core.Live, error)
	// Runs reports whether pid is alive and still runs the session, so a reused pid is never signalled.
	Runs   func(pid int) (alive, same bool)
	Signal func(pid int, sig syscall.Signal) error
	Poll   time.Duration
	// Grace is how long each signal gets to end the agent.
	Grace time.Duration
	Say   func(string)
}

// ErrReused means the agent's pid now runs something else.
var ErrReused = errors.New("pid no longer runs the session")

// Stop waits until the session is settled, then ends exactly its agent process with SIGINT, then SIGTERM. It returns the session as last read.
func (s Stopper) Stop(l core.Live) (core.Live, error) {
	said := ""
	for {
		next, err := s.Look()
		if errors.Is(err, core.ErrNotFound) {
			return l, nil
		}
		if err != nil {
			return l, err
		}
		l = next
		if len(l.Copies) > 1 {
			return l, fmt.Errorf("%s runs in %d processes; quit all but one first", core.Short(l.ID), len(l.Copies))
		}
		if l.Settled() {
			break
		}
		if why := unsettled(l); why != said {
			s.say("waiting: " + why)
			said = why
		}
		time.Sleep(s.Poll)
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		alive, same := s.Runs(l.PID)
		if !alive {
			return l, nil
		}
		if !same {
			return l, fmt.Errorf("pid %d: %w", l.PID, ErrReused)
		}
		s.say(fmt.Sprintf("ending %s pid %d with %s", l.Harness, l.PID, sigName(sig)))
		if err := s.Signal(l.PID, sig); err != nil {
			return l, err
		}
		for end := time.Now().Add(s.Grace); ; time.Sleep(s.Poll) {
			if alive, same := s.Runs(l.PID); !alive || !same {
				return l, nil
			}
			if !time.Now().Before(end) {
				break
			}
		}
	}
	return l, fmt.Errorf("%s pid %d still runs after SIGINT and SIGTERM", l.Harness, l.PID)
}

func (s Stopper) say(msg string) {
	if s.Say != nil {
		s.Say(msg)
	}
}

func unsettled(l core.Live) string {
	switch {
	case l.Prompt != nil:
		return "permission prompt " + l.Prompt.String()
	case l.Status == core.Waiting:
		return "waiting on a dialog"
	case l.Status != core.Idle:
		return "agent is " + l.Status.String()
	}
	return "crew still working"
}

func sigName(sig syscall.Signal) string {
	if sig == syscall.SIGINT {
		return "SIGINT"
	}
	return "SIGTERM"
}

// Unadoptable says why ym gives no launch record to a session working in dir, or "": the home directory holds everything
// the owner has, so nothing ym does to a launch may ever point at it, and another ym's rehearsal is that ym's: one whose state dir,
// state, lies outside it. A scoped ym adopts nothing outside the root holding its state dir.
func Unadoptable(dir, state string, scoped bool) string {
	if core.ForeignRehearsal(dir, state) || (scoped && core.OutsideOwnRoot(dir, state)) {
		return dir + " belongs to another ym's rehearsal or lies outside this ym's own; only the ym running that rehearsal adopts its sessions"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	have, err := os.Stat(dir)
	if err != nil {
		return ""
	}
	if want, err := os.Stat(home); err == nil && os.SameFile(have, want) {
		return dir + " is the home directory; start the agent in a repository to have ym adopt it"
	}
	return ""
}

// AdoptKey names the launch record of an adopted session.
func AdoptKey(id string) string { return "adopt-" + id }

// Adopted is the launch record that resumes s on ym's server in the directory it ran in. Its directory stays the owner's: landing it never removes it.
func Adopted(s core.Live, at time.Time) Record {
	w := Worktree{Path: s.Cwd}
	if common, err := git(s.Cwd, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
		w.GitDir = filepath.Clean(common)
	}
	if b, err := git(s.Cwd, "branch", "--show-current"); err == nil {
		w.Branch = strings.TrimSpace(b)
	}
	title := s.Title
	if title == "" {
		title = s.Task
	}
	return Record{Key: AdoptKey(s.ID), Harness: s.Harness, Profile: s.Profile, ConfigRoot: s.ConfigRoot, ConfigExplicit: s.Profile != "" && s.Profile != "default", Session: s.ID, Parent: s.Parent, Prompt: title, Worktree: w, Head: Head(s.Cwd),
		Repo: core.RepoID(w.GitDir), Started: at, Adopted: true}
}
