// Package slot shares the machine between heavy commands: each holds one of a fixed number of slots, sized from the
// machine's physical cores and memory, for as long as it runs. Slots are flock'd files in the same directory the
// agent-slot script uses, so both draw from one pool.
package slot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/pressure"
)

const gib = 1 << 30

// Claim is who holds or waits for a slot, and for what.
type Claim struct {
	Slot    int       `json:"slot,omitempty"`
	Lane    string    `json:"lane,omitempty"`
	PID     int       `json:"pid"`
	Command []string  `json:"command"`
	Cwd     string    `json:"cwd,omitempty"`
	Harness string    `json:"harness,omitempty"`
	Session string    `json:"session,omitempty"`
	Since   time.Time `json:"since"`
}

// Line is the claim's command as one line, or what is known of a holder that left no claim.
func (c Claim) Line() string {
	if len(c.Command) == 0 {
		return "agent-slot (no details)"
	}
	return strings.Join(c.Command, " ")
}

// Status is the pool as it stands: its capacity, who holds a slot, oldest first, and who waits, longest first.
type Status struct {
	Pressure pressure.State    `json:"pressure"`
	Throttle pressure.Throttle `json:"throttle"`
	Capacity int               `json:"capacity"`
	Cores    int               `json:"cores"`
	Memory   uint64            `json:"memory"`
	Workers  int               `json:"workers"`
	Holders  []Claim           `json:"holders"`
	Waiters  []Claim           `json:"waiters"`
}

// Pool is the slots in Dir; Capacity is how many there are.
type Pool struct {
	Dir      string
	Capacity int
	Cores    int
	Memory   uint64
	// Poll is how often a waiter tries the slots again.
	Poll time.Duration
}

// Capacity leaves two cores and a quarter of memory (at least 4 GiB) to the machine, then gives each slot two cores and 4 GiB.
func Capacity(cores int, memory uint64) int {
	cpu := max(1, (cores-2)/2)
	free := int64(memory) - int64(max(4*gib, memory/4))
	return min(cpu, max(1, int(free/(4*gib))))
}

// Dir is the slot directory agent-slot uses: $XDG_STATE_HOME/agent-slot, else ~/.local/state/agent-slot.
func Dir() (string, error) {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "agent-slot"), nil
}

// Machine is this machine's pool in Dir.
func Machine() (Pool, error) {
	dir, err := Dir()
	if err != nil {
		return Pool{}, err
	}
	cores, memory, err := resources()
	if err != nil {
		return Pool{}, err
	}
	return Pool{Dir: dir, Capacity: Capacity(cores, memory), Cores: cores, Memory: memory, Poll: 500 * time.Millisecond}, nil
}

func (p Pool) lockPath(n int) string  { return filepath.Join(p.Dir, strconv.Itoa(n)+".lock") }
func (p Pool) claimPath(n int) string { return filepath.Join(p.Dir, strconv.Itoa(n)+".json") }
func (p Pool) waitDir() string        { return filepath.Join(p.Dir, "waiting") }

// Acquire waits until it holds a slot, then records c as its holder. Waiting, c is listed among the waiters; onWait
// runs once, the first time every slot is taken. Release gives the slot back.
func (p Pool) Acquire(ctx context.Context, c Claim, onWait func()) (release func(), slot int, err error) {
	if err = os.MkdirAll(p.waitDir(), 0o755); err != nil {
		return nil, 0, err
	}
	waiting := filepath.Join(p.waitDir(), strconv.Itoa(c.PID)+".json")
	defer os.Remove(waiting)
	for first := true; ; first = false {
		state, readErr := pressure.Read(p.Dir)
		if readErr != nil {
			return nil, 0, readErr
		}
		for n := 1; n <= p.Capacity; n++ {
			f, err := p.tryLock(n)
			if err != nil {
				return nil, 0, err
			}
			if f == nil && state.Holding {
				break
			}
			if f == nil {
				continue
			}
			if state.Holding {
				idle, err := p.othersIdle(n)
				if err != nil {
					f.Close()
					return nil, 0, err
				}
				if !idle {
					f.Close()
					break
				}
			}
			c.Slot = n
			c.Since = time.Now()
			if err = writeJSON(p.claimPath(n), c); err != nil {
				f.Close()
				return nil, 0, err
			}
			return func() {
				os.Remove(p.claimPath(n))
				f.Close()
			}, n, nil
		}
		if first {
			if err = writeJSON(waiting, c); err != nil {
				return nil, 0, err
			}
			if onWait != nil {
				onWait()
			}
		}
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-time.After(p.Poll):
		}
	}
}

// tryLock returns slot n's lock file held, or nil when another process holds it.
func (p Pool) tryLock(n int) (*os.File, error) {
	f, err := os.OpenFile(p.lockPath(n), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, nil
	}
	return f, nil
}

// othersIdle reports whether every slot but n is free: overload comes from jobs outside the slots, so it holds new
// slots only while one runs, and an idle pool always grants one.
func (p Pool) othersIdle(n int) (bool, error) {
	for m := 1; m <= p.Capacity; m++ {
		if m == n {
			continue
		}
		f, err := p.tryLock(m)
		if err != nil || f == nil {
			return false, err
		}
		f.Close()
	}
	return true, nil
}

// Held counts the slots whose locks are taken.
func (p Pool) Held() (int, error) {
	held := 0
	for n := 1; n <= p.Capacity; n++ {
		busy, err := p.busy(n)
		if err != nil {
			return held, err
		}
		if busy {
			held++
		}
	}
	return held, nil
}

func (p Pool) busy(n int) (bool, error) {
	f, err := os.Open(p.lockPath(n))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil, nil
}

// Status reads who holds and waits for the pool's slots. A slot is held while its lock is; its claim names the holder
// unless an agent-slot script holds it, which leaves none.
func (p Pool) Status() (Status, error) {
	st := Status{Capacity: p.Capacity, Cores: p.Cores, Memory: p.Memory, Workers: p.Workers(), Holders: []Claim{}, Waiters: []Claim{}}
	var err error
	if st.Pressure, err = pressure.Read(p.Dir); err != nil {
		return st, err
	}
	if st.Throttle, err = pressure.ReadThrottle(p.Dir); err != nil {
		return st, err
	}
	for n := 1; n <= p.Capacity; n++ {
		busy, err := p.busy(n)
		if err != nil {
			return st, err
		}
		if !busy {
			continue
		}
		c, err := readClaim(p.claimPath(n))
		if err != nil || !alive(c.PID) {
			c = Claim{}
		}
		c.Slot = n
		st.Holders = append(st.Holders, c)
	}
	entries, err := os.ReadDir(p.waitDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return st, err
	}
	for _, e := range entries {
		path := filepath.Join(p.waitDir(), e.Name())
		c, err := readClaim(path)
		if err != nil || !alive(c.PID) {
			os.Remove(path)
			continue
		}
		st.Waiters = append(st.Waiters, c)
	}
	sort.Slice(st.Holders, func(i, j int) bool { return st.Holders[i].Slot < st.Holders[j].Slot })
	sort.Slice(st.Waiters, func(i, j int) bool { return st.Waiters[i].Since.Before(st.Waiters[j].Since) })
	return st, nil
}

func readClaim(path string) (Claim, error) {
	var c Claim
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(raw, &c)
}

func writeJSON(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp" + strconv.Itoa(os.Getpid())
	if err = os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// CPU reads each process's share of a core, in percent, from ps.
func CPU() (map[int]float64, error) {
	out, err := exec.Command("ps", "-A", "-o", "pid=,pcpu=").Output()
	if err != nil {
		return nil, err
	}
	return ParseCPU(string(out)), nil
}

// Mem reads each process's resident memory, in bytes, from ps.
func Mem() (map[int]float64, error) {
	out, err := exec.Command("ps", "-A", "-o", "pid=,rss=").Output()
	if err != nil {
		return nil, err
	}
	mem := ParseCPU(string(out))
	for pid, kib := range mem {
		mem[pid] = kib * 1024
	}
	return mem, nil
}

// ParseCPU reads ps's "pid value" lines, such as pid and pcpu.
func ParseCPU(out string) map[int]float64 {
	cpu := map[int]float64{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		pct, err2 := strconv.ParseFloat(f[1], 64)
		if err1 == nil && err2 == nil {
			cpu[pid] = pct
		}
	}
	return cpu
}
