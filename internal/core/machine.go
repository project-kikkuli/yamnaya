package core

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/slot"
)

// Machine is how this host's shared slots and processors stand.
type Machine struct {
	Slots slot.Status
	Load  float64
	// Holders and Waiters are the slot claims, each placed with the session and goal it runs for.
	Holders, Waiters []SlotUse
	// Hogs are the sessions, or programs outside any session, using the most processor time, read only while Overloaded.
	Hogs []Hog
}

// SlotUse is a slot claim and the session and goal it runs for; Session is "" when no session runs it.
type SlotUse struct {
	slot.Claim
	Session, Goal, Name string
}

// Hog is a session, or a program outside any session, and the cores it uses, in percent of one.
type Hog struct {
	Session, Goal, Name string
	CPU                 float64
}

// Overloaded reports whether the machine runs well past its cores: a load average above one and a half per core.
func (m Machine) Overloaded() bool { return m.Slots.Cores > 0 && m.Load > 1.5*float64(m.Slots.Cores) }

// Who names what a use runs for: its goal and session name, else its working directory.
func (u SlotUse) Who() string {
	dir := ""
	if u.Cwd != "" {
		dir = filepath.Base(u.Cwd)
	}
	return who(u.Goal, u.Name, dir)
}

// Who names what a hog is: its goal and session name, else its program.
func (h Hog) Who() string { return who(h.Goal, h.Name, h.Name) }

func who(goal, name, fallback string) string {
	switch {
	case goal != "" && name != "":
		return goal + " " + name
	case name != "":
		return name
	}
	return fallback
}

// cpuCache keeps the last processor reading, since ps costs a tenth of a second.
type cpuCache struct {
	mu  sync.Mutex
	at  time.Time
	cpu map[int]float64
}

const cpuTTL = 15 * time.Second

func (c *cpuCache) read(now time.Time, read func() (map[int]float64, error)) map[int]float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cpu == nil || now.Sub(c.at) >= cpuTTL {
		if cpu, err := read(); err == nil {
			c.cpu, c.at = cpu, now
		}
	}
	return c.cpu
}

// machine reads the slots and load and places each claim, and each heavy process when overloaded, with its session and goal.
func (c *Core) machine(b *Board, procs []proc.Proc) {
	if c.Slots == nil {
		return
	}
	pool, err := c.Slots()
	if err != nil {
		return
	}
	st, err := pool.Status()
	if err != nil {
		return
	}
	m := Machine{Slots: st}
	m.Load, _ = slot.Load()
	if c.Loads != nil {
		m.Load = c.Loads()
	}
	place := placer(*b, procs)
	for _, cl := range st.Holders {
		m.Holders = append(m.Holders, place.use(cl))
	}
	for _, cl := range st.Waiters {
		m.Waiters = append(m.Waiters, place.use(cl))
	}
	if m.Overloaded() && c.CPU != nil {
		m.Hogs = place.hogs(c.cpu.read(b.At, c.CPU))
	}
	b.Machine = m
}

type places struct {
	table   proc.Table
	byPID   map[int]string
	session map[string]bool
	named   map[string][2]string
}

func placer(b Board, procs []proc.Proc) places {
	p := places{table: proc.Index(procs), byPID: map[int]string{}, session: map[string]bool{}, named: map[string][2]string{}}
	for _, s := range b.Sessions {
		p.session[s.Key()] = true
		if s.PID > 0 && s.Remote == "" {
			p.byPID[s.PID] = s.Key()
		}
		name := s.Title
		if name == "" {
			name = Short(s.Key())
		}
		p.named[s.Key()] = [2]string{"", name}
	}
	var walk func(Node)
	walk = func(n Node) {
		goal := n.Manager.ID
		if n.Goal != nil {
			goal = n.Goal.ID
		}
		if n.Live != nil {
			p.named[n.Live.Key()] = [2]string{goal, n.Manager.ID}
		}
		for _, w := range n.Workers {
			if w.Member == nil {
				p.named[w.Session] = [2]string{goal, w.Name}
			}
		}
		for _, ch := range n.Children {
			walk(ch)
		}
	}
	for _, n := range b.Tree {
		walk(n)
	}
	return p
}

// sessionOf is the session pid runs under, "" when none.
func (p places) sessionOf(pid int) string {
	if k, ok := p.byPID[pid]; ok {
		return k
	}
	for _, a := range p.table.Ancestors(pid) {
		if k, ok := p.byPID[a.PID]; ok {
			return k
		}
	}
	return ""
}

func (p places) use(cl slot.Claim) SlotUse {
	u := SlotUse{Claim: cl, Session: p.sessionOf(cl.PID)}
	if u.Session == "" && p.session[cl.Session] {
		u.Session = cl.Session
	}
	if u.Session != "" {
		n := p.named[u.Session]
		u.Goal, u.Name = n[0], n[1]
	}
	return u
}

// hogs sums processor use by session, or by program outside any session, and keeps the three heaviest above half a core.
func (p places) hogs(cpu map[int]float64) []Hog {
	sum := map[string]*Hog{}
	for pid, pct := range cpu {
		if pct <= 0 {
			continue
		}
		key := p.sessionOf(pid)
		h := Hog{Session: key}
		if key != "" {
			n := p.named[key]
			h.Goal, h.Name = n[0], n[1]
		} else if pr, ok := p.table[pid]; ok {
			h.Name = pr.Command()
			key = "prog:" + h.Name
		} else {
			continue
		}
		if sum[key] == nil {
			sum[key] = &h
		}
		sum[key].CPU += pct
	}
	var out []Hog
	for _, h := range sum {
		if h.CPU >= 50 {
			out = append(out, *h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CPU > out[j].CPU })
	return out[:min(3, len(out))]
}

// Summary is one line on the slots and load: "slots 2/4 held, 1 waiting · load 11.4 on 12 cores".
func (m Machine) Summary() string {
	s := fmt.Sprintf("slots %d/%d held", len(m.Holders), m.Slots.Capacity)
	if len(m.Waiters) > 0 {
		s += fmt.Sprintf(", %d waiting", len(m.Waiters))
	}
	return s + fmt.Sprintf(" · load %.1f on %d cores", m.Load, m.Slots.Cores)
}

// Overload names the load and the top consumers, "" when the machine is not overloaded.
func (m Machine) Overload() string {
	if !m.Overloaded() {
		return ""
	}
	s := fmt.Sprintf("overloaded: load %.0f on %d cores", m.Load, m.Slots.Cores)
	var top []string
	for _, h := range m.Hogs {
		top = append(top, fmt.Sprintf("%s %.0f%%", h.Who(), h.CPU))
	}
	if len(top) > 0 {
		s += " · top: " + strings.Join(top, ", ")
	}
	return s
}

// Line is what the use runs, for whom and for how long: "goal3 tests  go test ./...  4m".
func (u SlotUse) Line(now time.Time) string {
	s := u.Claim.Line()
	if who := u.Who(); who != "" {
		s = who + "  " + s
	}
	if !u.Since.IsZero() {
		s += "  " + Span(now.Sub(u.Since))
	}
	return s
}

// Use is the slot session key holds or waits for, with waiting set when it waits.
func (m Machine) Use(key string) (u SlotUse, waiting, ok bool) {
	for _, h := range m.Holders {
		if h.Session == key {
			return h, false, true
		}
	}
	for _, w := range m.Waiters {
		if w.Session == key {
			return w, true, true
		}
	}
	return SlotUse{}, false, false
}
