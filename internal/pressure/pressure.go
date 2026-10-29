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

// Memory trips at the kernel's warning level or swap past half of RAM, and holds until swap falls under a quarter.
// The processor trips at 90% busy and holds above 75%. Either counts only while ym's agents hold a share of it: a
// quarter to trip, a sixth to hold, so Spotlight or a meeting app never throttles agents.
const (
	LevelWarn            = 2
	SwapHigh, SwapHold   = 0.5, 0.25
	BusyHigh, BusyHold   = 0.9, 0.75
	ShareHigh, ShareHold = 0.25, 1.0 / 6
)

type Process struct {
	PID, PPID, Nice int
	NiceUnavailable bool `json:",omitempty"`
	CPU             float64
	RSS             uint64 `json:",omitempty"`
	Started         int64
	Command         string
}

// Reading is the host with ym's share of it: AgentCPU and CPU are percent of one core summed over the agents' process
// trees and over every process, AgentMemory the agents' resident bytes.
type Reading struct {
	Host
	AgentCPU, CPU float64
	AgentMemory   uint64
}

func (r Reading) shares() (memory, cpu float64) {
	if used := r.Memory - min(r.Free, r.Memory); used > 0 {
		memory = float64(r.AgentMemory) / float64(used)
	}
	if r.CPU > 0 {
		cpu = r.AgentCPU / r.CPU
	}
	return memory, cpu
}

// strain names the resource the agents strain, memory before processor, and whether it passes the trip and hold bars.
func (r Reading) strain() (cause string, high, hold bool) {
	memShare, cpuShare := r.shares()
	ram := float64(r.Memory)
	memHigh := (r.Level >= LevelWarn || float64(r.Swap) >= SwapHigh*ram) && memShare >= ShareHigh
	memHold := (r.Level >= LevelWarn || float64(r.Swap) >= SwapHold*ram) && memShare >= ShareHold
	cpuHigh := r.Busy >= BusyHigh && cpuShare >= ShareHigh
	cpuHold := r.Busy >= BusyHold && cpuShare >= ShareHold
	switch {
	case memHigh || memHold:
		cause = "memory"
	case cpuHigh || cpuHold:
		cause = "cpu"
	}
	return cause, memHigh || cpuHigh, memHold || cpuHold
}

// Reason is "memory: kernel pressure warn, swap 15.5 GiB, agents hold 11.0 GiB (46% of used)" or
// "cpu: 94% busy, agents use 61% of it".
func (r Reading) Reason(cause string) string {
	memShare, cpuShare := r.shares()
	switch cause {
	case "memory":
		level := map[int]string{0: "unread", 1: "normal", 2: "warn", 4: "critical"}[r.Level]
		return fmt.Sprintf("memory: kernel pressure %s, swap %.1f GiB, agents hold %.1f GiB (%.0f%% of used)",
			level, float64(r.Swap)/(1<<30), float64(r.AgentMemory)/(1<<30), memShare*100)
	case "cpu":
		return fmt.Sprintf("cpu: %.0f%% busy, agents use %.0f%% of it", r.Busy*100, cpuShare*100)
	}
	return ""
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
	Reading             Reading   `json:"reading"`
	Cause               string    `json:"cause,omitempty"`
	Reason              string    `json:"reason,omitempty"`
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

// Line describes the throttle given how many slots are held; with none held the next slot is still granted.
func (s State) Line(held int) string {
	head := "throttle:"
	if s.Cause != "" {
		head = "throttle (" + s.Cause + "):"
	}
	switch {
	case s.Holding && held == 0:
		return fmt.Sprintf("%s heavy agent jobs nice 15 (%d live), 0 slots held, next slot granted", head, len(s.Changed))
	case s.Holding:
		return fmt.Sprintf("%s heavy agent jobs nice 15 (%d live), %d slots held, new slots wait", head, len(s.Changed), held)
	case s.Active:
		return "throttle: recovering, new slots open; clears after 3 minutes without agent pressure"
	case !s.Above.IsZero():
		return fmt.Sprintf("throttle: observing %s pressure from agents", s.Cause)
	}
	return ""
}

// Status is Line with the reason behind it.
func (s State) Status(held int) string {
	line := s.Line(held)
	if line != "" && s.Reason != "" && (s.Holding || !s.Above.IsZero()) {
		line += " · " + s.Reason
	}
	return line
}

func (s *State) Observe(at time.Time, r Reading) error {
	s.At, s.Reading = at, r
	if r.Memory == 0 {
		return errors.New("overload: physical memory is unavailable")
	}
	cause, high, hold := r.strain()
	if high {
		if s.Above.IsZero() {
			s.Above = at
		}
		if !s.Active && at.Sub(s.Above) >= HighFor {
			s.Active, s.Since, s.Notified = true, at, map[string]bool{}
		}
	} else {
		s.Above = time.Time{}
	}
	if !hold {
		if s.Below.IsZero() {
			s.Below = at
		}
		if s.Active && at.Sub(s.Below) >= LowFor {
			s.Active = false
		}
	} else {
		s.Below = time.Time{}
	}
	s.Holding = s.Active && hold
	switch {
	case cause != "" && (high || s.Active):
		s.Cause, s.Reason = cause, r.Reason(cause)
	case !s.Active:
		s.Cause, s.Reason = "", ""
	}
	if s.Notified == nil {
		s.Notified = map[string]bool{}
	}
	return nil
}

// Sample observes the host with the agents' share of it, then lowers only child jobs of verified agent roots, in CPU order.
func (s *State) Sample(at time.Time, host Host, procs []Process, roots, jobs map[int]Owner, set func(Process, int) error) error {
	table := map[int]Process{}
	for _, p := range procs {
		table[p.PID] = p
	}
	owners := map[int]Owner{}
	for pid, owner := range jobs {
		owners[pid] = owner
	}
	for _, p := range procs {
		seen := map[int]bool{}
		for id := p.PID; id > 1 && !seen[id]; id = table[id].PPID {
			seen[id] = true
			if owner, ok := jobs[id]; ok {
				owners[p.PID] = owner
				break
			}
			if owner, ok := roots[id]; ok {
				owners[p.PID] = owner
				break
			}
		}
	}
	r := Reading{Host: host}
	for _, p := range procs {
		r.CPU += p.CPU
		if _, ok := owners[p.PID]; ok {
			r.AgentCPU += p.CPU
			r.AgentMemory += p.RSS
		}
	}
	if err := s.Observe(at, r); err != nil {
		return err
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
		if !targets[p.PID] || p.NiceUnavailable || p.Nice >= Nice || recorded[p.PID] {
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

func (s State) Notices(held int) []Notice {
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
		slots := "slots open"
		if s.Holding && held > 0 {
			slots = "slots held"
		}
		out = append(out, Notice{Manager: changes[0].Manager, Goal: goal, Text: fmt.Sprintf("%s; child jobs nice 15; %s; top: %s", s.Reason, slots, strings.Join(top, ", "))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manager < out[j].Manager })
	return out
}
