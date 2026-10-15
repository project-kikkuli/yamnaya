package launch

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// agent is a fake agent process: what each read of it shows, and how it answers signals.
type agent struct {
	looks   []core.Live
	gone    bool
	reused  bool
	obeys   map[syscall.Signal]bool
	signals []syscall.Signal
}

func (a *agent) stopper() Stopper {
	return Stopper{
		Look: func() (core.Live, error) {
			if a.gone {
				return core.Live{}, core.ErrNotFound
			}
			l := a.looks[0]
			if len(a.looks) > 1 {
				a.looks = a.looks[1:]
			}
			return l, nil
		},
		Runs: func(pid int) (bool, bool) { return !a.gone, !a.reused },
		Signal: func(pid int, sig syscall.Signal) error {
			a.signals = append(a.signals, sig)
			if a.obeys[sig] {
				a.gone = true
			}
			return nil
		},
	}
}

func live(st core.Status) core.Live {
	return core.Live{Session: core.Session{Harness: "claude", ID: "s1", PID: 7, Cwd: "/w", Status: st}}
}

func TestStopWaitsForIdleThenEndsExactlyTheAgent(t *testing.T) {
	prompted := live(core.Waiting)
	prompted.Prompt = &core.Prompt{Tool: "Bash"}
	crewBusy := live(core.Idle)
	crewBusy.Crew = []core.Member{{ID: "m", Status: core.Busy}}
	twice := live(core.Idle)
	twice.Copies = []core.Session{twice.Session, twice.Session}
	for _, c := range []struct {
		name    string
		a       agent
		signals []syscall.Signal
		err     error
	}{
		{"idle at once, SIGINT ends it", agent{looks: []core.Live{live(core.Idle)}, obeys: map[syscall.Signal]bool{syscall.SIGINT: true}}, []syscall.Signal{syscall.SIGINT}, nil},
		{"busy, prompt, crew, then idle", agent{looks: []core.Live{live(core.Busy), prompted, crewBusy, live(core.Idle)}, obeys: map[syscall.Signal]bool{syscall.SIGINT: true}}, []syscall.Signal{syscall.SIGINT}, nil},
		{"ignores SIGINT, SIGTERM ends it", agent{looks: []core.Live{live(core.Idle)}, obeys: map[syscall.Signal]bool{syscall.SIGTERM: true}}, []syscall.Signal{syscall.SIGINT, syscall.SIGTERM}, nil},
		{"ignores both", agent{looks: []core.Live{live(core.Idle)}}, []syscall.Signal{syscall.SIGINT, syscall.SIGTERM}, errors.New("still runs")},
		{"pid reused", agent{looks: []core.Live{live(core.Idle)}, reused: true}, nil, ErrReused},
		{"quit on its own", agent{gone: true}, nil, nil},
		{"two processes", agent{looks: []core.Live{twice}}, nil, errors.New("processes")},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := c.a
			got, err := a.stopper().Stop(live(core.Busy))
			if (err == nil) != (c.err == nil) || (errors.Is(c.err, ErrReused) && !errors.Is(err, ErrReused)) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if len(a.signals) != len(c.signals) {
				t.Fatalf("signals %v, want %v", a.signals, c.signals)
			}
			for i := range a.signals {
				if a.signals[i] != c.signals[i] {
					t.Fatalf("signals %v, want %v", a.signals, c.signals)
				}
			}
			if got.ID != "s1" || got.Cwd != "/w" {
				t.Fatalf("Stop returned %+v, want the session as last read", got.Session)
			}
		})
	}
}

func TestAdoptedKeepsTheSessionsDirectoryAndBranch(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q", "-b", "trunk")
	run(t, dir, "git", "commit", "-q", "--allow-empty", "-m", "init")
	s := live(core.Idle)
	s.ID, s.Cwd, s.Title, s.Parent = "0123456789abcdef", dir, "Fix login", "lead"
	rec := Adopted(s, s.Since)
	real, _ := filepath.EvalSymlinks(dir)
	gitDir, _ := filepath.EvalSymlinks(rec.Worktree.GitDir)
	if rec.Key != "adopt-01234567" || !rec.Adopted || rec.Session != s.ID || rec.Parent != "lead" || rec.Prompt != "Fix login" ||
		rec.Worktree.Path != dir || rec.Worktree.Branch != "trunk" || gitDir != filepath.Join(real, ".git") || rec.Head == "" {
		t.Fatalf("record %+v", rec)
	}
}
