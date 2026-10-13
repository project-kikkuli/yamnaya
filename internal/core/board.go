package core

import (
	"sort"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// Core composes harnesses, hosts and the store.
type Core struct {
	Store     *Store
	Harnesses []Harness
	Hosts     []Host
	Procs     func() ([]proc.Proc, error)
	Now       func() time.Time
}

// Live is a discovered session with where it runs and how many processes run it.
type Live struct {
	Session
	Handle  *Handle
	Copies  []Session
	Project string
}

// Location names where the session runs, for display.
func (l Live) Location() string {
	if l.Handle == nil {
		return "no host"
	}
	return l.Handle.Host + " " + l.Handle.Label
}

type Project struct {
	Key      string
	Name     string
	Sessions []Live
	Items    []Item
	Phases   Phases
	Status   Status
}

func (p Project) Crew() int {
	n := 0
	for _, s := range p.Sessions {
		n += len(s.Crew)
	}
	return n
}

// Duplicated reports whether any session of the project runs in more than one process.
func (p Project) Duplicated() bool {
	for _, s := range p.Sessions {
		if len(s.Copies) > 1 {
			return true
		}
	}
	return false
}

// Need is one row of the needs-you list: a permission prompt or an open item.
type Need struct {
	Session *Live
	Item    *Item
	Project string
}

type Board struct {
	At       time.Time
	Sessions []Live
	Projects []Project
	Needs    []Need

	parents map[string]string
	lin     Lineage
}

func (b Board) Session(key string) (Live, bool) {
	for _, s := range b.Sessions {
		if s.Key() == key {
			return s, true
		}
	}
	return Live{}, false
}

func (b Board) Project(key string) (Project, bool) {
	for _, p := range b.Projects {
		if p.Key == key {
			return p, true
		}
	}
	return Project{}, false
}

// ProjectOf is the key of the project the session belongs to, live or not.
func (b Board) ProjectOf(session string) string {
	return projectOf(session, b.parents, b.lin)
}

func (c *Core) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// owned lists each harness's terminal processes. A launcher wrapping the same harness on the same terminal gives way to its child.
func (c *Core) owned(procs []proc.Proc) [][]proc.Proc {
	byHarness := make([][]proc.Proc, len(c.Harnesses))
	owner := map[int]int{}
	for _, p := range procs {
		for i, h := range c.Harnesses {
			if h.Owns(p) {
				owner[p.PID] = i
				break
			}
		}
	}
	wraps := map[int]bool{}
	table := proc.Index(procs)
	for _, p := range procs {
		i, ok := owner[p.PID]
		parent, found := table[p.PPID]
		if pi, owned := owner[p.PPID]; ok && found && owned && pi == i && parent.TTY == p.TTY {
			wraps[p.PPID] = true
		}
	}
	for _, p := range procs {
		if i, ok := owner[p.PID]; ok && p.TTY != "" && !wraps[p.PID] {
			byHarness[i] = append(byHarness[i], p)
		}
	}
	return byHarness
}

// Board reads every session, groups sessions into projects and lists what needs the owner.
func (c *Core) Board() (Board, error) {
	b := Board{At: c.now()}
	procs, err := c.Procs()
	if err != nil {
		return b, err
	}
	byHarness := c.owned(procs)
	var found []Session
	for i, h := range c.Harnesses {
		found = append(found, h.Discover(byHarness[i])...)
	}
	harnessOf := map[string]Harness{}
	for _, h := range c.Harnesses {
		harnessOf[h.Name()] = h
	}
	copies := map[string][]Session{}
	for _, s := range found {
		if s.ID != "" {
			copies[s.Harness+"/"+s.ID] = append(copies[s.Harness+"/"+s.ID], s)
		}
	}
	for _, s := range found {
		l := Live{Session: s, Copies: copies[s.Harness+"/"+s.ID]}
		if h, host := c.locate(s.TTY); host != nil {
			l.Handle = &h
			if (l.Status == Waiting || l.CheckScreen) && l.Prompt == nil {
				if screen, err := host.Screen(h); err == nil {
					if hn := harnessOf[s.Harness]; hn != nil {
						if p, ok := hn.ParsePrompt(screen); ok {
							l.Prompt = &p
						}
					}
				}
			}
		}
		b.Sessions = append(b.Sessions, l)
	}
	lin, err := c.Store.Lineage()
	if err != nil {
		return b, err
	}
	parents := processParents(b.Sessions, proc.Index(procs))
	for child, parent := range lin.Parent {
		if _, ok := parents[child]; !ok {
			parents[child] = parent
		}
	}
	items, err := c.Store.Items(ItemFilter{})
	if err != nil {
		return b, err
	}
	b.parents, b.lin = parents, lin
	b.Projects = group(b.Sessions, items, parents, lin)
	for i := range b.Projects {
		p := &b.Projects[i]
		if p.Phases, err = c.Store.Phases(p.Key); err != nil {
			return b, err
		}
		for j := range p.Sessions {
			p.Sessions[j].Project = p.Key
		}
	}
	for i := range b.Sessions {
		b.Sessions[i].Project = projectOf(b.Sessions[i].Key(), parents, lin)
	}
	b.Needs = needs(b)
	return b, nil
}

func (c *Core) locate(tty string) (Handle, Host) {
	if tty == "" {
		return Handle{}, nil
	}
	for _, host := range c.Hosts {
		if h, ok := host.Locate(tty); ok {
			if h.Host == "" {
				h.Host = host.Name()
			}
			return h, host
		}
	}
	return Handle{}, nil
}

func (c *Core) host(name string) Host {
	for _, h := range c.Hosts {
		if h.Name() == name {
			return h
		}
	}
	return nil
}

// processParents links each session to the nearest ancestor process that runs another session.
func processParents(sessions []Live, table proc.Table) map[string]string {
	byPID := map[int]string{}
	for _, s := range sessions {
		byPID[s.PID] = s.Key()
	}
	parents := map[string]string{}
	for _, s := range sessions {
		for _, a := range table.Ancestors(s.PID) {
			if key, ok := byPID[a.PID]; ok && key != s.Key() {
				parents[s.Key()] = key
				break
			}
		}
	}
	return parents
}

// projectOf follows spawn links to the root session, then the owner's merges.
func projectOf(key string, parents map[string]string, lin Lineage) string {
	seen := map[string]bool{key: true}
	for {
		p, ok := parents[key]
		if !ok || seen[p] {
			break
		}
		seen[p] = true
		key = p
	}
	for {
		into, ok := lin.Merged[key]
		if !ok || seen[into] {
			return key
		}
		seen[into] = true
		key = into
	}
}

func group(sessions []Live, items []Item, parents map[string]string, lin Lineage) []Project {
	byKey := map[string]*Project{}
	var order []string
	get := func(key string) *Project {
		p := byKey[key]
		if p == nil {
			p = &Project{Key: key}
			byKey[key] = p
			order = append(order, key)
		}
		return p
	}
	seen := map[string]bool{}
	for _, s := range sessions {
		if seen[s.Key()] {
			continue
		}
		seen[s.Key()] = true
		p := get(projectOf(s.Key(), parents, lin))
		p.Sessions = append(p.Sessions, s)
	}
	for _, it := range items {
		key := projectOf(it.Session, parents, lin)
		if it.State == ItemOpen || byKey[key] != nil {
			p := get(key)
			p.Items = append(p.Items, it)
		}
	}
	out := make([]Project, 0, len(order))
	for _, key := range order {
		p := byKey[key]
		sort.SliceStable(p.Sessions, func(i, j int) bool { return p.Sessions[i].Key() == key && p.Sessions[j].Key() != key })
		p.Name = lin.Name[key]
		if p.Name == "" {
			p.Name = defaultName(*p)
		}
		p.Status = projectStatus(*p)
		out = append(out, *p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (len(out[i].Sessions) == 0) != (len(out[j].Sessions) == 0) {
			return len(out[i].Sessions) > 0
		}
		return false
	})
	return out
}

func defaultName(p Project) string {
	for _, s := range p.Sessions {
		if s.Key() == p.Key {
			if s.Title != "" {
				return s.Title
			}
			if s.Repo != "" {
				return strings.TrimSuffix(s.Repo+" "+s.Branch, " ")
			}
		}
	}
	for _, s := range p.Sessions {
		if s.Title != "" {
			return s.Title
		}
	}
	return Short(p.Key)
}

// Short is a session key cut to its first eight characters.
func Short(key string) string {
	if len(key) > 8 {
		return key[:8]
	}
	return key
}

func projectStatus(p Project) Status {
	if len(p.Sessions) == 0 {
		return Unknown
	}
	best := Unknown
	rank := map[Status]int{Waiting: 4, Busy: 3, Idle: 2, Unknown: 1}
	for _, s := range p.Sessions {
		if rank[s.Status] > rank[best] {
			best = s.Status
		}
	}
	return best
}

// needs orders permission prompts first, then open items by how much they block, oldest first.
func needs(b Board) []Need {
	var out []Need
	asked := map[string]bool{}
	for i := range b.Sessions {
		s := &b.Sessions[i]
		if asked[s.Key()] {
			continue
		}
		if s.Prompt != nil || (s.Status == Waiting && strings.Contains(strings.ToLower(s.WaitingFor), "permission")) {
			out = append(out, Need{Session: s, Project: s.Project})
			asked[s.Key()] = true
		}
	}
	var items []Need
	for i := range b.Projects {
		p := &b.Projects[i]
		for j := range p.Items {
			if p.Items[j].State == ItemOpen {
				items = append(items, Need{Item: &p.Items[j], Project: p.Key})
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, c := items[i].Item, items[j].Item
		if a.Blocks != c.Blocks {
			return a.Blocks > c.Blocks
		}
		return a.CreatedAt.Before(c.CreatedAt)
	})
	return append(out, items...)
}
