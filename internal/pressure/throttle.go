package pressure

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Paused is a process the owner's throttle stopped, under the session it runs for.
type Paused struct {
	PID     int
	Started int64
	Command string
	Session string
}

// Throttle is the owner's blanket throttle: every process it stopped, until it resumes them. Stopped processes use no
// processor and allocate nothing, so the machine can page their memory out instead of thrashing. Ended is when it last
// resumed any. Blanket holds every ym agent, so ym serve also brings none back, even those that were not running.
type Throttle struct {
	Blanket bool      `json:",omitempty"`
	Since   time.Time `json:",omitempty"`
	Ended   time.Time `json:",omitempty"`
	Paused  []Paused  `json:",omitempty"`
}

func (t Throttle) On() bool { return t.Blanket || len(t.Paused) > 0 }

// Holds reports whether the throttle keeps session stopped: ym serve neither resumes, relaunches nor nudges it.
func (t Throttle) Holds(session string) bool { return t.Blanket || t.Has(session) }

// Sessions are the paused sessions' keys, sorted.
func (t Throttle) Sessions() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range t.Paused {
		if !seen[p.Session] {
			seen[p.Session] = true
			out = append(out, p.Session)
		}
	}
	sort.Strings(out)
	return out
}

func (t Throttle) Has(session string) bool {
	for _, p := range t.Paused {
		if p.Session == session {
			return true
		}
	}
	return false
}

// Line is the board's banner while the throttle is on.
func (t Throttle) Line() string {
	if !t.On() {
		return ""
	}
	who := fmt.Sprintf("%d agents paused", len(t.Sessions()))
	if t.Blanket {
		who = fmt.Sprintf("every ym agent paused (%d running), none resumed or relaunched", len(t.Sessions()))
	}
	return fmt.Sprintf("THROTTLED: %s, %d processes stopped since %s; Q or ym unthrottle resumes them",
		who, len(t.Paused), t.Since.Local().Format(time.Kitchen))
}

func ReadThrottle(dir string) (Throttle, error) {
	var t Throttle
	raw, err := os.ReadFile(filepath.Join(dir, "throttle.json"))
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return t, err
	}
	return t, json.Unmarshal(raw, &t)
}

func (t Throttle) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "throttle.json")
	if err = os.WriteFile(path+".new", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".new", path)
}

// Signal sends sig to p unless its pid now names another process.
func Signal(p Process, sig syscall.Signal) error {
	start, err := started(p.PID)
	if err != nil {
		return err
	}
	if start != p.Started {
		return os.ErrProcessDone
	}
	err = syscall.Kill(p.PID, sig)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

// Pause stops each root session's process and everything under it, parents first so none forks a child it misses,
// and lists again until a pass stops nothing new. ym's own processes and their subtrees keep running, so the board and
// ym unthrottle still work.
func (t *Throttle) Pause(at time.Time, list func() ([]Process, error), roots map[int]string, signal func(Process, syscall.Signal) error) (int, error) {
	stopped := map[int]bool{}
	for _, p := range t.Paused {
		stopped[p.PID] = true
	}
	total := 0
	var failures []error
	for {
		procs, err := list()
		if err != nil {
			return total, errors.Join(append(failures, err)...)
		}
		children := map[int][]Process{}
		for _, p := range procs {
			children[p.PPID] = append(children[p.PPID], p)
		}
		byPID := map[int]Process{}
		for _, p := range procs {
			byPID[p.PID] = p
		}
		pids := make([]int, 0, len(roots))
		for pid := range roots {
			pids = append(pids, pid)
		}
		sort.Ints(pids)
		n := 0
		for _, root := range pids {
			p, ok := byPID[root]
			if !ok {
				continue
			}
			queue := []Process{p}
			for len(queue) > 0 {
				p, queue = queue[0], queue[1:]
				if spared(p.Command) {
					continue
				}
				if !stopped[p.PID] {
					if err := signal(p, syscall.SIGSTOP); err != nil {
						if !errors.Is(err, os.ErrProcessDone) {
							failures = append(failures, fmt.Errorf("stop %s pid %d: %w", p.Command, p.PID, err))
						}
						continue
					}
					stopped[p.PID] = true
					t.Paused = append(t.Paused, Paused{PID: p.PID, Started: p.Started, Command: p.Command, Session: roots[root]})
					n++
				}
				queue = append(queue, children[p.PID]...)
			}
		}
		total += n
		if n == 0 {
			break
		}
	}
	if total > 0 && t.Since.IsZero() {
		t.Since = at
	}
	return total, errors.Join(failures...)
}

// Resume continues every paused process whose session in reports true, children first, and forgets it.
func (t *Throttle) Resume(at time.Time, in func(session string) bool, signal func(Process, syscall.Signal) error) (int, error) {
	var kept []Paused
	var failures []error
	n := 0
	for i := len(t.Paused) - 1; i >= 0; i-- {
		p := t.Paused[i]
		if !in(p.Session) {
			kept = append(kept, p)
			continue
		}
		err := signal(Process{PID: p.PID, Started: p.Started, Command: p.Command}, syscall.SIGCONT)
		switch {
		case err == nil:
			n++
		case !errors.Is(err, os.ErrProcessDone):
			failures = append(failures, fmt.Errorf("continue %s pid %d: %w", p.Command, p.PID, err))
			kept = append(kept, p)
		}
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	t.Paused = kept
	if n > 0 {
		t.Ended = at
	}
	if !t.On() {
		t.Since = time.Time{}
	}
	return n, errors.Join(failures...)
}

func spared(command string) bool {
	switch strings.TrimPrefix(filepath.Base(command), "-") {
	case "ym", "tmux":
		return true
	}
	return false
}
