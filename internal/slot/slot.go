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

	"github.com/project-kikkuli/yamnaya/internal/disk"
	"github.com/project-kikkuli/yamnaya/internal/pressure"
)

const gib = 1 << 30

// Claim is who holds or waits for a slot, and for what.
type Claim struct {
	Slot int    `json:"slot,omitempty"`
	Lane string `json:"lane,omitempty"`
	// Status fills in LaneHolder, the job that has its lane, and LanePlace and SlotPlace, a waiter's place in line from 1.
	// None is set for a waiter from a ym older than tickets, which takes no place.
	LaneHolder bool `json:"lane_holder,omitempty"`
	LanePlace  int  `json:"lane_place,omitempty"`
	SlotPlace  int  `json:"slot_place,omitempty"`
	// DiskLow is the filesystem keeping this waiter from a slot. Only the waiter can know it, since it depends on its own
	// environment, so it writes this into its own ticket; ReportsDisk says it does, which a ticket from an older ym does not.
	DiskLow     *disk.Usage `json:"disk_low,omitempty"`
	ReportsDisk bool        `json:"reports_disk,omitempty"`
	PID         int         `json:"pid"`
	Command     []string    `json:"command"`
	Cwd         string      `json:"cwd,omitempty"`
	Harness     string      `json:"harness,omitempty"`
	Session     string      `json:"session,omitempty"`
	Since       time.Time   `json:"since"`
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
	// Disk is the lowest filesystem holding back a waiter, which says so on its claim, or nil. Jobs on other disks can still get slots.
	Disk    *disk.Usage `json:"disk,omitempty"`
	Holders []Claim     `json:"holders"`
	Waiters []Claim     `json:"waiters"`
	// SlotsWait is whether the memory hold refuses a free slot to every job now. Pressure.Holding also covers cpu, which never does.
	SlotsWait bool `json:"slots_wait"`
}

// Pool is the slots in Dir; Capacity is how many there are.
type Pool struct {
	Dir      string
	Capacity int
	Cores    int
	Memory   uint64
	// Poll is how often a waiter tries the slots again.
	Poll time.Duration
	// Low reports the filesystem, among those it watches and extra, so low new slots wait; nil never holds them.
	Low func(extra ...string) (disk.Usage, bool)
}

func (p Pool) low(extra ...string) (disk.Usage, bool) {
	if p.Low == nil {
		return disk.Usage{}, false
	}
	return p.Low(extra...)
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
	return Pool{Dir: dir, Capacity: Capacity(cores, memory), Cores: cores, Memory: memory, Poll: 500 * time.Millisecond, Low: disk.Held}, nil
}

func (p Pool) lockPath(n int) string  { return filepath.Join(p.Dir, strconv.Itoa(n)+".lock") }
func (p Pool) claimPath(n int) string { return filepath.Join(p.Dir, strconv.Itoa(n)+".json") }
func (p Pool) waitDir() string        { return filepath.Join(p.Dir, "waiting") }
func (p Pool) ticketDir() string      { return filepath.Join(p.Dir, "lanes") }
func (p Pool) slotLineDir() string    { return filepath.Join(p.Dir, "slot-line") }

// Acquire waits until it holds a slot and the disk is not too low, then records c as its holder. onWait runs once, the first
// time it gets none. A claim on no lane takes its turn by a ticket, oldest first, behind any live lane holder waiting for a
// slot; a lane's holder is already listed by the ticket Lane gave it, and a lane's other waiters never come here. A job held
// for its own filesystem keeps its place without holding up those behind it. Release gives the slot back.
func (p Pool) Acquire(ctx context.Context, c Claim, onWait func()) (release func(), slot int, err error) {
	var place *ticket
	var own string
	if c.Lane == "" {
		if err = os.MkdirAll(p.slotLineDir(), 0o755); err != nil {
			return nil, 0, err
		}
		if place, err = takeTicket(p.slotLineDir(), c); err != nil {
			return nil, 0, err
		}
		defer place.drop()
		own = place.path
	} else if own, err = p.ownHolderTicket(c.Lane); err != nil {
		return nil, 0, err
	}
	var reported *disk.Usage
	recovering := 0
	for first := true; ; first = false {
		state, readErr := pressure.Read(p.Dir)
		if readErr != nil {
			return nil, 0, readErr
		}
		capacity := p.Capacity
		var held *disk.Usage
		if u, low := p.low(c.Cwd); low {
			capacity, held = 0, &u
		}
		// older waiters on the same filesystem poll within two of this job's polls and clear their own holds; until then, passing
		// them would take their place
		if held == nil && reported != nil {
			recovering = 2
		}
		deferring := recovering > 0
		recovering = max(0, recovering-1)
		if !sameHold(held, reported) && own != "" {
			if err = markTicketDisk(own, c, held); err != nil {
				return nil, 0, err
			}
			reported = held
		}
		if place != nil && !deferring {
			if deferring, err = p.behind(place); err != nil {
				return nil, 0, err
			}
		}
		for n := 1; n <= capacity && !deferring; n++ {
			f, err := p.tryLock(n)
			if err != nil {
				return nil, 0, err
			}
			if f == nil && state.HoldsSlots() {
				break
			}
			if f == nil {
				continue
			}
			if state.HoldsSlots() {
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
		if first && onWait != nil {
			onWait()
		}
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-time.After(p.Poll):
		}
	}
}

// behind reports whether a slot goes to someone else first: a live lane holder, or an older plain waiter. A job its own
// filesystem holds back can take no slot, so nobody waits for it.
func (p Pool) behind(place *ticket) (bool, error) {
	waits, err := p.laneHolderWaits()
	if err != nil || waits {
		return waits, err
	}
	line, err := liveTickets(p.slotLineDir(), queuedTicket)
	if err != nil {
		return false, err
	}
	for _, path := range line {
		if path == place.path {
			return false, nil
		}
		if !p.diskHolds(path) {
			return true, nil
		}
	}
	return true, nil
}

// diskHolds reports whether the job with the ticket at path says its filesystem holds it back.
func (p Pool) diskHolds(path string) bool {
	c, err := readClaim(path)
	return err == nil && p.diskHold(c) != nil
}

// diskHold is what c says holds it back. Only a claim from a ym that does not report it is judged here, by its cwd.
func (p Pool) diskHold(c Claim) *disk.Usage {
	if c.ReportsDisk {
		return c.DiskLow
	}
	if u, low := p.low(c.Cwd); low {
		return &u
	}
	return nil
}

// sameHold reports whether two holds would read the same on a status line, so a waiter rewrites its ticket only for a change.
func sameHold(a, b *disk.Usage) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Path == b.Path && int(a.Free()*1000) == int(b.Free()*1000)
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
		c.LaneHolder = c.Lane != ""
		st.Holders = append(st.Holders, c)
	}
	entries, err := os.ReadDir(p.waitDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return st, err
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(p.waitDir(), e.Name())
		c, err := readClaim(path)
		if err != nil || !alive(c.PID) {
			os.Remove(path)
			continue
		}
		st.Waiters = append(st.Waiters, c)
	}
	onLanes, err := p.laneClaims()
	if err != nil {
		return st, err
	}
	st.Waiters = append(st.Waiters, onLanes...)
	line, err := liveTickets(p.slotLineDir(), queuedTicket)
	if err != nil {
		return st, err
	}
	for i, path := range line {
		if c, err := readClaim(path); err == nil {
			c.SlotPlace = i + 1
			st.Waiters = append(st.Waiters, c)
		}
	}
	for i := range st.Waiters {
		u := p.diskHold(st.Waiters[i])
		st.Waiters[i].DiskLow = u
		if u != nil && (st.Disk == nil || u.Free() < st.Disk.Free()) {
			st.Disk = u
		}
	}
	st.SlotsWait = st.Pressure.HoldsSlots() && len(st.Holders) > 0
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
