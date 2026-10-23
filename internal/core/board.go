package core

import (
	"fmt"
	"hash/fnv"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/profile"
	"github.com/project-kikkuli/yamnaya/internal/slot"
)

// Core composes harnesses, hosts and the store.
type Core struct {
	ProfileList func() []LoginProfile
	Store       *Store
	Harnesses   []Harness
	Hosts       []Host
	Procs       func() ([]proc.Proc, error)
	Now         func() time.Time
	Meter       Meter
	// Git places a directory in its repository; nil leaves sessions without a repo.
	Git func(dir string) GitInfo
	// PRs fetches pull request status for RefreshPRs.
	PRs PRSource
	// Landings reads whether a goal's pull requests merged green, for verification.
	Landings Landings
	// Launches reports launched sessions that exited; nil shows none.
	Launches Launches
	// Roots are the directories new-session pickers look for repositories under; nil looks nowhere.
	Roots func() []string
	// Ask is the line of an agent's reply that asks the owner for something, "" when none; nil lists no waiting sessions.
	Ask func(reply string) string
	// Health renders the weekly health trend; nil shows none.
	Health func() (string, error)
	// Port rebuilds session id, run in cwd, in harness to and returns the new session id.
	Port        func(cwd, id, to string) (string, error)
	PreparePort func(Session, string, string) (PortPlan, error)
	// Signal and KillGrace are how Kill signals a process and how long it waits for it to end; nil and zero mean syscall.Kill and 3s.
	Signal         func(pid int, sig syscall.Signal) error
	KillGrace      time.Duration
	BackgroundSend func(Live, string) error
	// Log takes what ym serve should record, such as why a queued answer or message did not reach its agent.
	Log           func(string)
	undeliveredMu sync.Mutex
	undelivered   map[string]string
	// Notify raises a desktop notification for the owner; nil raises none.
	Notify    func(title, body string) error
	KeepAlive func() error
	// Boot is when this host last started; nil reads it from the system.
	Boot func() time.Time
	// Woke is when this host last woke from sleep; a session's silence while it slept does not make it hung.
	Woke time.Time
	// Pace is how long a wake waits for followers and the rest wait for a digest; nil means WakeDebounce and DigestEvery.
	Pace *WakePace
	// StateDir holds ym's files beside its database, such as goal briefs.
	StateDir string
	// Remotes are the other hosts ym routes commands to; ShowRemotes also puts their boards on this one.
	Remotes     Remotes
	ShowRemotes bool
	// RunSelf runs this host's ym with args, for a relay that asks for a ym command; nil refuses such relays.
	RunSelf func(args ...string) (string, error)
	// Slots is this host's pool of shared slots, Loads its load average, CPU each process's processor use and Mem its resident
	// memory in bytes; a nil Slots leaves the board without a Machine, a nil Loads reads the system's.
	Slots func() (slot.Pool, error)
	Loads func() float64
	CPU   func() (map[int]float64, error)
	Mem   func() (map[int]float64, error)

	gc       gitCache
	fw       forwardedSet
	prompts  promptCache
	reuse    boardReuse
	paced    PRPace
	recovery recovery
	cpu      cpuCache
	mem      cpuCache
}

// Live is a discovered session with where it runs and how many processes run it.
type Live struct {
	Session
	Handle *Handle
	// App is the program the session's terminal belongs to when no host adapter knows it, such as iTerm2.
	App string
	// Muxed is set when a tmux server ym does not reach runs the session's terminal.
	Muxed   bool
	Copies  []Session
	Project string
	// Messages are the follow-ups the owner sent this session.
	Messages []Message
	// Remote is the host the session runs on when that is not this one; Where is where that host says it runs.
	Remote, Where string
}

// Location names where the session runs, for display.
func (l Live) Location() string {
	if l.Remote != "" {
		return l.Remote + " " + l.Where
	}
	if l.Background {
		return "bg job"
	}
	if l.Handle == nil {
		where := strings.TrimPrefix(l.TTY, "/dev/")
		if where == "" {
			where = "no terminal"
		}
		if l.App != "" {
			where = l.App + " " + where
		}
		return where
	}
	return l.Handle.Host + " " + l.Handle.Label
}

// Project is a mission: a root session and the sessions it spawned.
type Project struct {
	Key  string
	Name string
	// Repo is the repository's identity, the same for every clone on every host; RepoDir is the git common dir of the clone
	// its root session works in.
	Repo     string
	RepoName string
	RepoDir  string
	Sessions []Live
	Items    []Item
	Phases   Phases
	Status   Status
	Spend    Spend
	// Dormant are launched sessions of the mission whose agents exited.
	Dormant []Dormant
	// Vibes are the agents' reads of the mission, newest first.
	Vibes []Vibe
	// Manager is the manager whose tree the mission's sessions belong to; empty for a mission the owner started.
	Manager string
}

// Shown reports whether the mission has a session running or one ym can revive.
func (p Project) Shown() bool {
	return len(p.Sessions) > 0 || len(p.Dormant) > 0
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

// Need is one row of the needs-you list: a permission prompt, an open item, or an idle session whose last reply asks the owner.
type Need struct {
	Session *Live
	Item    *Item
	Project string
	// Reply is the waiting session's last reply, or its replies back to the last one that holds the questions; Line is the first question.
	Reply, Line string
	// Asks are every open owner item of a session no manager runs, folded into this one row; Item is the oldest.
	Asks []Item
	// Goal is a stuck goal that needs the owner.
	Goal *Goal
	// PR is a pull request ready to merge that no manager may merge; Manager is the manager whose agent opened it, if any.
	PR      *PR
	Manager string
	// Review is a pull request GitHub asks the owner to review.
	Review *ReviewRequest
	// Docs are the open asks on one document waiting on the owner, newest first; Doc is the newest.
	Doc  *DocAsk
	Docs []DocAsk
	// Steer is the owner's revision of a goal's plan that its manager, Manager, left unadopted through every nudge.
	Steer *Steer
	// Relay is one this host carries that the host it goes to keeps refusing, RefusalsBeforeOwner times or for RefusedBeforeOwner.
	Relay *Relay
}

// Repo is every mission whose root session works in one repository, across all its worktrees.
type Repo struct {
	Key  string
	Name string
	// Dir is the git common dir of one of its clones, a clone on this host when it has one.
	Dir      string
	Missions []Project
}

type Board struct {
	// Org is every manager, chief and goal; Tree arranges them chiefs over managers.
	Org      Org
	Tree     []Node
	Launched []Launched
	At       time.Time
	Sessions []Live
	Projects []Project
	Repos    []Repo
	Needs    []Need
	Profiles []LoginProfile
	Fleet    Fleet
	Kills    map[string]time.Time
	// Stale are open items whose mission has no live session left.
	Stale []Need
	// Gen numbers the boards built; a board reused because nothing it reads changed keeps the number.
	Gen uint64
	// Remotes are the other hosts the board reads, reachable or not.
	Remotes []RemoteBoard
	// PRPace is how this host's ym serve last read pull requests.
	PRPace PRPace
	// Machine is how this host's shared slots and processors stand.
	Machine Machine
	// remoteStanding is how each manager and chief on another host stands by that host's reading.
	remoteStanding map[string]string

	parents map[string]string
	lin     Lineage
	// moved maps each live process's session that continued under another id to the session carrying it now.
	moved map[string]string
	// index and projects are each session's and project's first place in Sessions and Projects, once those are final.
	index, projects map[string]int
}

// Session is the live session key names, or the one key's conversation continued in.
func (b Board) Session(key string) (Live, bool) {
	if next, ok := b.moved[key]; ok {
		key = next
	}
	if b.index == nil {
		b.indexSessions()
	}
	if i, ok := b.index[key]; ok && i >= 0 {
		return b.Sessions[i], true
	}
	return Live{}, false
}

func (b *Board) indexSessions() {
	b.index = make(map[string]int, len(b.Sessions))
	bind := func(key string, i int) {
		if prev, ok := b.index[key]; ok {
			if prev >= 0 {
				a, s := b.Sessions[prev], b.Sessions[i]
				if a.Harness != s.Harness || profile.ID(a.Profile) != profile.ID(s.Profile) || a.Remote != s.Remote {
					b.index[key] = -1
				}
			}
			return
		}
		b.index[key] = i
	}
	for i, s := range b.Sessions {
		ref := s.Ref()
		explicit := s.Harness + ":" + profile.ID(s.Profile) + "@" + s.Key()
		if s.Remote != "" {
			ref = RemoteKey(s.Remote, ref)
			explicit = RemoteKey(s.Remote, explicit)
		}
		bind(explicit, i)
		bind(ref, i)
		bind(s.Key(), i)
	}

	b.projects = make(map[string]int, len(b.Projects))
	for i, p := range b.Projects {
		if _, ok := b.projects[p.Key]; !ok {
			b.projects[p.Key] = i
		}
	}
}

func (b Board) Repo(key string) (Repo, bool) {
	for _, r := range b.Repos {
		if r.Key == key {
			return r, true
		}
	}
	return Repo{}, false
}

func (b Board) Project(key string) (Project, bool) {
	if b.projects != nil {
		if i, ok := b.projects[key]; ok {
			return b.Projects[i], true
		}
		return Project{}, false
	}
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

// terminals fingerprints the processes that run on a terminal; it changes when one starts, exits or moves.
func terminals(procs []proc.Proc) uint64 {
	var sum uint64
	for _, p := range procs {
		if p.TTY == "" {
			continue
		}
		h := fnv.New64a()
		fmt.Fprintf(h, "%d\x00%d\x00%s", p.PID, p.PPID, p.TTY)
		sum += h.Sum64()
	}
	return sum
}

// Board reads every session, groups sessions into projects and lists what needs the owner.
func (c *Core) Board() (Board, error) {
	b := Board{At: c.now()}
	if c.ProfileList != nil {
		b.Profiles = c.ProfileList()
	}
	var err error
	if b.Kills, err = c.Store.Kills(b.At.Add(-24 * time.Hour)); err != nil {
		return b, err
	}
	procs, err := c.Procs()
	if err != nil {
		return b, err
	}
	stamp := terminals(procs)
	for _, h := range c.Hosts {
		if w, ok := h.(Watching); ok {
			w.Terminals(stamp)
		}
	}
	byHarness := c.owned(procs)
	discovered := make([][]Session, len(c.Harnesses))
	var wg sync.WaitGroup
	for i, h := range c.Harnesses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			discovered[i] = h.Discover(byHarness[i])
		}()
	}
	wg.Wait()
	fp, reusable := c.fingerprint(stamp, len(procs), byHarness)
	if prev, ok := c.reuse.get(fp, reusable, b.At); ok {
		again := c.again(prev, b.At)
		c.machine(&again, procs)
		return again, nil
	}
	var found []Session
	for _, d := range discovered {
		for _, s := range d {
			if !c.foreign(s.Cwd) {
				found = append(found, s)
			}
		}
	}
	c.place(found, b.At)
	c.placeDrift(found, b.At)
	harnessOf := map[string]Harness{}
	for _, h := range c.Harnesses {
		harnessOf[h.Name()] = h
	}
	copies := map[string][]Session{}
	for _, s := range found {
		if s.ID != "" {
			copies[s.Harness+"/"+s.Profile+"/"+s.ID] = append(copies[s.Harness+"/"+s.Profile+"/"+s.ID], s)
		}
	}
	table := proc.Index(procs)
	b.Sessions = make([]Live, 0, len(found))
	b.moved = map[string]string{}
	for _, s := range found {
		if s.Status == Moved {
			b.moved[s.ID] = c.Resolve(s.ID)
			continue
		}
		l := Live{Session: s, Copies: copies[s.Harness+"/"+s.Profile+"/"+s.ID]}
		h, host := c.locate(s.TTY)
		if host == nil {
			l.App, l.Muxed = appOf(table, s.PID), muxed(table, s.PID)
		}
		if host != nil {
			l.Handle = &h
			if (l.Status == Waiting || l.CheckScreen) && l.Prompt == nil {
				l.Prompt = c.prompts.on(l.Session, h, b.At, func() *Prompt {
					if screen, err := host.Screen(h); err == nil {
						if hn := harnessOf[s.Harness]; hn != nil {
							if p, ok := hn.ParsePrompt(screen); ok {
								return &p
							}
						}
					}
					return nil
				})
			}
		}
		b.Sessions = append(b.Sessions, l)
	}
	c.attachPRs(b.Sessions)
	if err := c.attachMessages(b.Sessions, b.moved); err != nil {
		return b, err
	}
	lin, err := c.Store.Lineage()
	if err != nil {
		return b, err
	}
	for old, now := range b.moved {
		if _, ok := lin.Parent[now]; !ok && now != old {
			if err := c.Store.Link(now, old); err != nil {
				return b, err
			}
			lin.Parent[now] = old
		}
	}
	parents := processParents(b.Sessions, table)
	for _, s := range b.Sessions {
		if _, ok := parents[s.Key()]; !ok && s.Parent != "" && s.Parent != s.Key() {
			parents[s.Key()] = s.Parent
		}
	}
	for child, parent := range lin.Parent {
		if _, ok := parents[child]; !ok {
			parents[child] = parent
		}
	}
	items, err := c.Store.Items(ItemFilter{})
	if err != nil {
		return b, err
	}
	dormant := c.dormant(fp.dormant, b.Sessions, b.moved)
	if s, ok := c.Launches.(interface{ Saw([]Live) }); ok {
		s.Saw(b.Sessions)
	}
	b.Launched = slices.Clone(fp.launched)
	if c.ShowRemotes {
		b.addRemotes(c.remoteBoards(), parents, &items, &dormant)
	}
	for _, d := range dormant {
		if _, ok := parents[d.ID()]; !ok && d.Parent != "" && d.Parent != d.ID() {
			parents[d.ID()] = d.Parent
		}
	}
	b.parents, b.lin = parents, lin
	b.Projects = group(b.Sessions, items, parents, lin, dormant)
	vibes, err := c.Store.Vibes()
	if err != nil {
		return b, err
	}
	phases, err := c.Store.AllPhases()
	if err != nil {
		return b, err
	}
	for i := range b.Projects {
		p := &b.Projects[i]
		p.Phases = phases[p.Key]
		p.Vibes = b.VibesOf(vibes, p.Key)
		for j := range p.Sessions {
			p.Sessions[j].Project = p.Key
		}
	}
	for i := range b.Sessions {
		b.Sessions[i].Project = projectOf(b.Sessions[i].Key(), parents, lin)
	}
	for i := range b.Projects {
		if b.Projects[i].Repo == "" {
			b.Projects[i].Repo, b.Projects[i].RepoName, b.Projects[i].RepoDir = c.crewRepo(b.Projects[i], b.At)
		}
	}
	if c.Meter != nil {
		b.Fleet = MergeFleet(c.Meter.Fleet(b.At), b.Remotes)
		for i := range b.Projects {
			b.Projects[i].Spend = projectSpend(b.Projects[i], b.Fleet)
		}
	}
	if err := c.projectSessionStops(&b); err != nil {
		return b, err
	}
	b.indexSessions()
	if b.PRPace, err = c.Store.PRPace(); err != nil {
		return b, err
	}
	if b.Org, err = c.LoadOrg(); err != nil {
		return b, err
	}
	feeds, err := c.Store.Feeds()
	if err != nil {
		return b, err
	}
	if c.ShowRemotes {
		b.remoteStanding = c.mergeRemoteOrg(&b.Org, b.Remotes, feeds)
	}
	if err := c.tree(&b, b.Org, feeds); err != nil {
		return b, err
	}
	b.Repos = repos(b.Projects)
	b.Needs, b.Stale = needs(b)
	wait, err := c.waiting(b, b.Needs)
	if err != nil {
		return b, err
	}
	b.Needs = collapse(append(append(b.Needs, wait...), b.remoteWaiting()...), b.Org, b)
	if err := c.queue(&b, b.Org); err != nil {
		return b, err
	}
	c.reuse.put(fp, reusable, &b)
	c.machine(&b, procs)
	return b, nil
}

// gitReaders bounds how many directories place reads at once, so a thousand stale checkouts do not start a thousand threads.
const gitReaders = 8

// place fills each session's repository and its crew's worktrees, reading each directory's git files at most gitReaders at a time.
func (c *Core) place(found []Session, now time.Time) {
	var want []string
	wanted := map[string]bool{}
	for i := range found {
		s := &found[i]
		// a harness may hand back the crew it keeps between reads, so filling it in starts from a copy
		s.Crew = slices.Clone(s.Crew)
		if !wanted[s.Cwd] {
			wanted[s.Cwd] = true
			want = append(want, s.Cwd)
		}
		for j := range s.Crew {
			if s.Crew[j].Worktree == "" {
				s.Crew[j].Worktree = c.crewWorktree(s.Key(), s.Crew[j])
			}
			if w := s.Crew[j].Worktree; w != "" && !wanted[w] {
				wanted[w] = true
				want = append(want, w)
			}
		}
	}
	dirs := make(map[string]GitInfo, len(want))
	var stale []string
	for _, d := range want {
		if g, ok := c.gitFresh(d, now); ok {
			dirs[d] = g
		} else {
			stale = append(stale, d)
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, gitReaders)
	for _, d := range stale {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			g := c.gitFor(d, now)
			mu.Lock()
			dirs[d] = g
			mu.Unlock()
		}()
	}
	wg.Wait()
	for i := range found {
		s := &found[i]
		if g := dirs[s.Cwd]; g.Common != "" {
			s.Repo, s.RepoKey, s.RepoID, s.Worktree = g.Name(), g.Common, g.ID(), g.Top
			if g.Branch != "" && g.Branch != "HEAD" {
				s.Branch, s.Ahead, s.Default = g.Branch, g.Ahead, g.Default
			}
		}
		for j := range s.Crew {
			if g := dirs[s.Crew[j].Worktree]; g.Branch != "" && g.Branch != "HEAD" {
				s.Crew[j].Branch, s.Crew[j].Ahead = g.Branch, g.Ahead
				s.Crew[j].RepoKey, s.Crew[j].Default = g.Common, g.Default
			}
		}
	}
}

func (c *Core) attachPRs(sessions []Live) {
	known, err := c.Store.PRStatuses()
	if err != nil || len(known) == 0 {
		return
	}
	now := c.now()
	set := func(prs []PR, worktree, branch string, ahead int) []PR {
		var out []PR
		for _, pr := range prs {
			if st, ok := known[pr.URL]; ok {
				if st.State == PRGone {
					continue
				}
				if !ownsPR(worktree, branch, ahead, st, now) {
					continue
				}
				pr.Status = &st
			}
			out = append(out, pr)
		}
		return out
	}
	for i := range sessions {
		s := &sessions[i]
		s.PRs = set(s.PRs, s.Worktree, s.Branch, s.Ahead)
		crew := append([]Member(nil), s.Crew...)
		for j := range crew {
			crew[j].PRs = set(crew[j].PRs, crew[j].Worktree, crew[j].Branch, crew[j].Ahead)
		}
		sessions[i].Crew = crew
	}
}

// crewRepo is the repository most of a mission's crew wrote files in, for a mission whose lead works outside any repo.
func (c *Core) crewRepo(p Project, now time.Time) (string, string, string) {
	count := map[string]int{}
	names := map[string][]string{}
	best := ""
	for _, s := range p.Sessions {
		for _, m := range s.Crew {
			if m.Worktree == "" {
				continue
			}
			g := c.gitFor(m.Worktree, now)
			if g.Common == "" {
				continue
			}
			id := g.ID()
			count[id]++
			if names[id] == nil {
				names[id] = []string{g.Name(), g.Common}
			}
			if count[id] > count[best] {
				best = id
			}
		}
	}
	if best == "" {
		return "", "", ""
	}
	return best, names[best][0], names[best][1]
}

// repos groups missions by their root session's repository, busiest repos first.
func repos(projects []Project) []Repo {
	byKey := map[string]*Repo{}
	var order []string
	for _, p := range projects {
		if !p.Shown() {
			continue
		}
		r := byKey[p.Repo]
		if r == nil {
			r = &Repo{Key: p.Repo, Name: p.RepoName, Dir: p.RepoDir}
			if r.Name == "" {
				r.Name = "(no repo)"
			}
			byKey[p.Repo] = r
			order = append(order, p.Repo)
		}
		if filepath.IsAbs(p.RepoDir) && !filepath.IsAbs(r.Dir) {
			r.Dir = p.RepoDir
		}
		r.Missions = append(r.Missions, p)
	}
	out := make([]Repo, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	rank := func(r Repo) int {
		best := 0
		for _, p := range r.Missions {
			best = max(best, map[Status]int{Waiting: 3, Busy: 2, Idle: 1}[p.Status])
		}
		return best
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Key == "") != (out[j].Key == "") {
			return out[j].Key == ""
		}
		if rank(out[i]) != rank(out[j]) {
			return rank(out[i]) > rank(out[j])
		}
		return out[i].Name < out[j].Name
	})
	return out
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

func (c *Core) harness(name string) Harness {
	for _, h := range c.Harnesses {
		if h.Name() == name {
			return h
		}
	}
	return nil
}

func (c *Core) host(name string) Host {
	for _, h := range c.Hosts {
		if h.Name() == name {
			return h
		}
	}
	return nil
}

// appOf names the macOS app among the process's ancestors, if any.
func appOf(table proc.Table, pid int) string {
	for _, a := range table.Ancestors(pid) {
		if len(a.Args) > 0 {
			if i := strings.Index(a.Args[0], ".app/Contents/MacOS/"); i >= 0 {
				return a.Args[0][strings.LastIndex(a.Args[0][:i], "/")+1 : i]
			}
		}
	}
	return ""
}

// muxed reports whether a tmux server is among the process's ancestors.
func muxed(table proc.Table, pid int) bool {
	for _, a := range table.Ancestors(pid) {
		if len(a.Args) > 0 && filepath.Base(a.Args[0]) == "tmux" {
			return true
		}
	}
	return false
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

// dormant lists launched sessions that exited and run nowhere else, placed in their repos.
func (c *Core) dormant(all []Dormant, live []Live, moved map[string]string) []Dormant {
	running := map[string]bool{}
	for _, s := range live {
		running[s.Key()] = true
	}
	for old := range moved {
		running[old] = true
	}
	var out []Dormant
	for _, d := range all {
		if !running[d.ID()] {
			out = append(out, d)
		}
	}
	return out
}

func group(sessions []Live, items []Item, parents map[string]string, lin Lineage, dormant []Dormant) []Project {
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
	for _, d := range dormant {
		p := get(projectOf(d.ID(), parents, lin))
		p.Dormant = append(p.Dormant, d)
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
		for _, s := range p.Sessions {
			if s.RepoKey != "" {
				p.Repo, p.RepoName, p.RepoDir = s.RepoID, s.Repo, s.RepoKey
				if p.Repo == "" {
					p.Repo = s.RepoKey
				}
				break
			}
		}
		for _, d := range p.Dormant {
			if p.Repo == "" && d.RepoKey != "" {
				p.Repo, p.RepoDir = d.RepoID, d.RepoKey
				if p.Repo == "" {
					p.Repo = RepoID(d.RepoKey)
				}
				p.RepoName = RepoLabel(p.Repo)
			}
		}
		out = append(out, *p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Shown() != out[j].Shown() {
			return out[i].Shown()
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
	for _, d := range p.Dormant {
		if d.Title != "" {
			return d.Title
		}
	}
	return Short(p.Key)
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
// Items whose mission has no live session are stale and listed apart.
func needs(b Board) (live, stale []Need) {
	asked := map[string]bool{}
	for i := range b.Sessions {
		s := &b.Sessions[i]
		if asked[s.Key()] {
			continue
		}
		if s.Prompt != nil || (s.Status == Waiting && strings.Contains(strings.ToLower(s.WaitingFor), "permission")) {
			live = append(live, Need{Session: s, Project: s.Project})
			asked[s.Key()] = true
		}
	}
	var items []Need
	for i := range b.Projects {
		p := &b.Projects[i]
		for j := range p.Items {
			if p.Items[j].State == ItemOpen && p.Items[j].Recipient == Owner {
				n := Need{Item: &p.Items[j], Project: p.Key}
				if len(p.Sessions) > 0 {
					items = append(items, n)
				} else {
					stale = append(stale, n)
				}
			}
		}
	}
	order := func(ns []Need) {
		sort.SliceStable(ns, func(i, j int) bool {
			a, c := ns[i].Item, ns[j].Item
			if a.Blocks != c.Blocks {
				return a.Blocks > c.Blocks
			}
			return a.CreatedAt.Before(c.CreatedAt)
		})
	}
	order(items)
	order(stale)
	return append(live, items...), stale
}
