package pressure

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const HighFor = time.Minute
const LowFor = 3 * time.Minute
const Nice = 15

type Process struct {
	PID, PPID, Nice int
	CPU             float64
	Started         int64
	Command         string
}

type Owner struct {
	Session, Goal, Manager string
}

type Change struct {
	Process
	Owner
	Original int
}

type State struct {
	At                  time.Time `json:"at"`
	Load                float64   `json:"load"`
	Cores               int       `json:"cores"`
	Above, Below, Since time.Time
	Active, Holding     bool
	Changed             []Change
	Notified            map[string]bool
}

func Read(dir string) (State, error) {
	var s State
	raw, err := os.ReadFile(filepath.Join(dir, "overload.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(raw, &s)
	return s, err
}

func (s State) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "overload.json")
	if err = os.WriteFile(path+".new", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".new", path)
}

func (s State) Line() string {
	switch {
	case s.Holding:
		return fmt.Sprintf("throttle: heavy agent jobs nice 15 (%d live), new slots held", len(s.Changed))
	case s.Active:
		return "throttle: recovering, new slots open; clears after 3 minutes below 1.25x cores"
	case !s.Above.IsZero():
		return "throttle: observing load above 2x cores"
	}
	return ""
}

func (s *State) Observe(at time.Time, load float64, cores int) error {
	s.At, s.Load, s.Cores = at, load, cores
	if cores <= 0 {
		return errors.New("overload: physical core count is unavailable")
	}
	if load > 2*float64(cores) {
		if s.Above.IsZero() {
			s.Above = at
		}
		if !s.Active && at.Sub(s.Above) >= HighFor {
			s.Active, s.Since, s.Notified = true, at, map[string]bool{}
		}
	} else {
		s.Above = time.Time{}
	}
	if load < 1.25*float64(cores) {
		if s.Below.IsZero() {
			s.Below = at
		}
		if s.Active && at.Sub(s.Below) >= LowFor {
			s.Active = false
		}
	} else {
		s.Below = time.Time{}
	}
	s.Holding = s.Active && load >= 1.25*float64(cores)
	if s.Notified == nil {
		s.Notified = map[string]bool{}
	}
	return nil
}

// Sample lowers only child jobs of verified agent roots, in CPU order.
func (s *State) Sample(at time.Time, load float64, cores int, procs []Process, roots map[int]Owner, set func(Process, int) error) error {
	if err := s.Observe(at, load, cores); err != nil {
		return err
	}
	table := map[int]Process{}
	for _, p := range procs {
		table[p.PID] = p
	}
	kept := s.Changed[:0]
	recorded := map[int]bool{}
	for _, c := range s.Changed {
		if p, ok := table[c.PID]; ok && p.Started == c.Started {
			c.CPU = p.CPU
			kept = append(kept, c)
			recorded[c.PID] = true
		}
	}
	s.Changed = kept
	if !s.Holding {
		return nil
	}
	owners := map[int]Owner{}
	for _, p := range procs {
		seen := map[int]bool{}
		for id := p.PID; id > 1 && !seen[id]; id = table[id].PPID {
			seen[id] = true
			if owner, ok := roots[id]; ok {
				owners[p.PID] = owner
				break
			}
		}
	}
	// A CPU-consuming job's idle children inherit its priority too; shells and harnesses never do.
	targets := map[int]bool{}
	for _, p := range procs {
		if _, ok := owners[p.PID]; !ok || p.CPU <= 0 || protected(p.Command) {
			continue
		}
		if _, root := roots[p.PID]; root {
			continue
		}
		targets[p.PID] = true
	}
	for _, p := range procs {
		if _, ok := owners[p.PID]; !ok || protected(p.Command) {
			continue
		}
		if _, root := roots[p.PID]; root {
			continue
		}
		seen := map[int]bool{}
		for id := p.PPID; id > 1 && !seen[id]; id = table[id].PPID {
			seen[id] = true
			if _, root := roots[id]; root {
				break
			}
			if protected(table[id].Command) {
				break
			}
			if targets[id] {
				targets[p.PID] = true
				break
			}
		}
	}
	ordered := append([]Process(nil), procs...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].CPU == ordered[j].CPU {
			return ordered[i].PID < ordered[j].PID
		}
		return ordered[i].CPU > ordered[j].CPU
	})
	var failures []error
	for _, p := range ordered {
		if !targets[p.PID] || p.Nice >= Nice || recorded[p.PID] {
			continue
		}
		if err := set(p, Nice); err != nil {
			if !errors.Is(err, os.ErrProcessDone) {
				failures = append(failures, fmt.Errorf("renice %s pid %d: %w", p.Command, p.PID, err))
			}
			continue
		}
		s.Changed = append(s.Changed, Change{Process: p, Owner: owners[p.PID], Original: p.Nice})
	}
	return errors.Join(failures...)
}

func protected(command string) bool {
	base := strings.TrimPrefix(filepath.Base(command), "-")
	switch base {
	case "claude", "claude.exe", "codex", "codex.exe", "ym", "tmux", "WindowServer", "Terminal", "iTerm2", "zsh", "bash", "sh", "fish", "dash", "ksh", "tcsh", "csh":
		return true
	}
	return false
}

type Notice struct{ Manager, Goal, Text string }

func (s State) Notices() []Notice {
	if !s.Active {
		return nil
	}
	groups := map[string][]Change{}
	for _, c := range s.Changed {
		if c.Goal != "" && c.Manager != "" && !s.Notified[c.Goal] {
			groups[c.Goal] = append(groups[c.Goal], c)
		}
	}
	var out []Notice
	for goal, changes := range groups {
		sort.Slice(changes, func(i, j int) bool { return changes[i].CPU > changes[j].CPU })
		var top []string
		for _, c := range changes[:min(3, len(changes))] {
			top = append(top, fmt.Sprintf("%s pid %d %.0f%%", filepath.Base(c.Command), c.PID, c.CPU))
		}
		slots := "new slots open"
		if s.Holding {
			slots = "new slots held"
		}
		out = append(out, Notice{Manager: changes[0].Manager, Goal: goal, Text: fmt.Sprintf("ym: overload %.1f on %d cores; %s heavy child jobs niced to 15, %s; top: %s", s.Load, s.Cores, goal, slots, strings.Join(top, ", "))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manager < out[j].Manager })
	return out
}
