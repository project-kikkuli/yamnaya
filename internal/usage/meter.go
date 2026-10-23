package usage

import (
	"encoding/json"
	"maps"

	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

// Stats describes one scan.
type Stats struct {
	Files     int
	Changed   int
	Bytes     int64
	Responses int
	Took      time.Duration
	Parse     time.Duration
	Commit    time.Duration
}

// Meter keeps usage of every Claude Code and Codex transcript current, reading only what was appended since its last scan.
type Meter struct {
	ClaudeDir string
	CodexHome string
	Profiles  []profile.Profile
	StateDir  string
	// Every is the least time between scans that Fleet starts.
	Every time.Duration
	// Fetch downloads a trimmed price file; nil keeps prices offline.
	Fetch func() ([]byte, error)

	scanMu      sync.Mutex
	mu          sync.Mutex
	ledger      *ledger
	files       map[string]*file
	byID        map[int64]*file
	nextID      int64
	sums        map[sumKey]core.Tokens
	slices      map[sliceKey]core.Tokens
	exhaustions map[string]core.Exhaustion
	limits      map[string]core.Limit
	edges       map[string]string
	prices      *Prices
	fleet       core.Fleet
	running     bool
	last        time.Time
	err         error
}

func New(stateDir string) *Meter {
	claude := os.Getenv("CLAUDE_CONFIG_DIR")
	home, _ := os.UserHomeDir()
	if claude == "" {
		claude = filepath.Join(home, ".claude")
	}
	codex := os.Getenv("CODEX_HOME")
	if codex == "" {
		codex = filepath.Join(home, ".codex")
	}
	if ps, err := profile.Load(stateDir); err == nil {
		for _, p := range ps {
			if p.ID == "default" {
				if p.Harness == claudeName {
					claude = p.Root
				} else {
					codex = p.Root
				}
			}
		}
	}
	return &Meter{ClaudeDir: claude, CodexHome: codex, StateDir: stateDir, Every: 5 * time.Second, Fetch: FetchPrices}
}

func (m *Meter) Close() error {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	if m.ledger == nil {
		return nil
	}
	return m.ledger.close()
}

// Err is the last scan's error.
func (m *Meter) Err() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// Fleet returns the latest scan's view and starts a new scan in the background when the last is older than Every.
func (m *Meter) Fleet(now time.Time) core.Fleet {
	m.mu.Lock()
	if !m.running && now.Sub(m.last) >= m.Every {
		m.running = true
		go func() {
			_, err := m.Scan(time.Now())
			m.mu.Lock()
			m.running, m.err = false, err
			m.mu.Unlock()
		}()
	}
	f := m.fleet
	m.mu.Unlock()
	f.Limits = current(append(readClaudeLimits(m.StateDir), f.Limits...), now)
	return f
}

func (m *Meter) open(now time.Time) error {
	if m.ledger != nil {
		return m.reload(now)
	}
	l, err := openLedger(filepath.Join(m.StateDir, "ym.db"))
	if err != nil {
		return err
	}
	m.ledger = l
	if err := m.reload(now); err != nil {
		l.close()
		m.ledger = nil
		return err
	}
	m.prices = loadPrices(m.StateDir)
	if m.Fetch != nil {
		go m.refreshPrices()
	}
	return nil
}

func (m *Meter) reload(now time.Time) error {
	files, sums, limits, err := m.ledger.load()
	if err != nil {
		return err
	}
	slices, err := m.ledger.loadSlices(now)
	if err != nil {
		return err
	}
	m.files, m.sums, m.slices, m.limits = files, sums, slices, limits
	m.byID = map[int64]*file{}
	m.nextID = 0
	for _, f := range files {
		m.byID[f.ID] = f
		m.nextID = max(m.nextID, f.ID)
	}
	m.exhaustions = map[string]core.Exhaustion{}
	rows, err := m.ledger.db.Query(`SELECT key,body FROM ym_usage_exhaustions`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key, body string
		var e core.Exhaustion
		if rows.Scan(&key, &body) == nil && json.Unmarshal([]byte(body), &e) == nil {
			m.exhaustions[key] = e
		}
	}
	defer rows.Close()
	return rows.Err()
}

// parsed is what reading one file's new lines produced.
type parsed struct {
	f           *file
	reset       bool
	claude      claudeChunk
	codex       codexChunk
	bytes       int64
	imports     []profile.Import
	err         error
	exhaustions []core.Exhaustion
}

// Scan reads every new transcript line, stores what it adds and rebuilds the fleet view.
func (m *Meter) Scan(now time.Time) (Stats, error) {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	start := time.Now()
	var st Stats
	if _, err := os.Stat(m.StateDir); err != nil {
		return st, err
	}
	lock, err := os.OpenFile(filepath.Join(m.StateDir, "usage-scan.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return st, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return st, err
	}
	if err := m.open(now); err != nil {
		return st, err
	}
	owners := map[string]string{}
	paths, _ := filepath.Glob(filepath.Join(m.StateDir, "launches", "*.json"))
	for _, path := range paths {
		var r struct {
			Session   string `json:"session"`
			Harness   string `json:"harness"`
			Profile   string `json:"profile"`
			Account   string `json:"account"`
			Key       string `json:"key"`
			NativePID int    `json:"native_pid"`
		}
		data, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(data, &r) == nil && r.Key != "" && r.Session != "" && r.NativePID > 0 && proc.ConfigRoot(r.NativePID, "YAMNAYA_LAUNCH_KEY") == r.Key {
			owners[r.Harness+"/"+profile.ID(r.Profile)+"/"+r.Session] = r.Account
		}
	}
	ps := m.Profiles
	if ps == nil {
		var err error
		ps, err = profile.Load(m.StateDir)
		if err != nil {
			return st, err
		}
		for i := range ps {
			if ps[i].ID == "default" {
				if ps[i].Harness == claudeName {
					ps[i].Root = m.ClaudeDir
				} else {
					ps[i].Root = m.CodexHome
				}
			}
		}
	}
	var srcs []source
	for _, p := range ps {
		var sources []source
		if p.Harness == claudeName {
			sources = claudeSources(p.Root)
		} else {
			sources = codexSources(p.Root)
		}
		for _, s := range sources {
			s.ConfigRoot = p.Root
			s.Harness = p.Harness
			s.Profile = p.ID
			s.ProfileLabel = p.Label
			s.Account = p.Account
			if account, known := owners[s.Harness+"/"+profile.ID(s.Profile)+"/"+s.Session]; known {
				s.Account = account
			}
			epochs, err := m.accountSources(s)
			if err != nil {
				return st, err
			}
			srcs = append(srcs, epochs...)
		}
	}
	st.Files = len(srcs)
	var work []*parsed
	for _, s := range srcs {
		old := m.files[s.Path]
		if old != nil && old.Size == s.Size && old.MTime == s.MTime && old.Profile == s.Profile && old.Account == s.Account {
			continue
		}
		f := &file{source: s}
		p := &parsed{f: f}
		var err error
		p.imports, err = profile.Imports(m.StateDir, s.physical())
		if err != nil {
			return st, err
		}
		if err = profile.ValidateImports(s.physical(), p.imports); err != nil {
			return st, err
		}
		switch {
		case old == nil:
			m.nextID++
			f.ID = m.nextID
			f.Offset = f.EpochStart
		case s.Size >= old.Offset:
			f.ID, f.Offset, f.Cursor, f.Cost = old.ID, old.Offset, old.Cursor, old.Cost
		default:
			f.ID, p.reset = old.ID, true
		}
		work = append(work, p)
	}
	st.Changed = len(work)
	t0 := time.Now()
	sort.Slice(work, func(i, j int) bool { return work[i].f.Size-work[i].f.Offset > work[j].f.Size-work[j].f.Offset })
	m.parse(work)
	for _, p := range work {
		if p.err != nil {
			return st, p.err
		}
	}
	st.Parse = time.Since(t0)
	sort.Slice(work, func(i, j int) bool { return work[i].f.Path < work[j].f.Path })
	b, err := m.merge(work, bucketOf(now.Add(-keepSlices).Unix()))
	if err != nil {
		return st, err
	}
	for _, p := range work {
		st.Bytes += p.bytes
		st.Responses += len(p.claude.responses) + len(p.codex.turns)
	}
	t0 = time.Now()
	if len(work) > 0 {
		if err := m.observeEpochs(work); err != nil {
			return st, err
		}
		if err := m.ledger.commit(b); err != nil {
			return st, err
		}
	}
	st.Commit = time.Since(t0)
	m.apply(b)
	if err = m.saveExhaustions(work); err != nil {
		return st, err
	}
	m.edges = map[string]string{}
	for _, p := range ps {
		if p.Harness == codexName {
			for child, parent := range codexEdges(p.Root) {
				m.edges[profile.SessionKey(codexName, p.ID, child)] = profile.SessionKey(codexName, p.ID, parent)
			}
		}
	}
	f := m.build(now)
	m.mu.Lock()
	m.fleet, m.last = f, now
	m.mu.Unlock()
	st.Took = time.Since(start)
	return st, nil
}

func (m *Meter) parse(work []*parsed) {
	jobs := make(chan *parsed)
	var wg sync.WaitGroup
	for range max(1, runtime.NumCPU()-1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				from := p.f.Offset
				p.codex.limits = map[string]core.Limit{}
				if from == p.f.EpochStart && p.f.Harness == codexName {
					seed, err := rolloutSeed(p.f.ConfigRoot, p.f.physical(), from, map[string]bool{})
					if err != nil {
						p.err = err
						continue
					}
					p.f.Cursor = seed
				}
				position := from
				p.f.Offset, p.err = readLines(p.f.physical(), from, func(line []byte) {
					start := position
					position += int64(len(line))
					if position > p.f.Size {
						return
					}
					imported := false
					for _, r := range p.imports {
						if start >= r.Start && position <= r.End {
							imported = true
							if position == r.End && len(r.Seed) > 0 {
								if err := json.Unmarshal(r.Seed, &p.f.Cursor); err != nil {
									p.err = err
								}
							}
							break
						}
					}
					if imported {
						return
					}
					if e := exhaustionEvent(line, p.f.Harness, p.f.Cursor.Model); e != nil {
						p.exhaustions = append(p.exhaustions, *e)
					}
					if p.f.Harness == claudeName {
						parseClaude(line, &p.claude)
					} else {
						parseCodex(line, &p.f.Cursor, &p.codex)
					}
				})
				p.f.Offset = min(p.f.Offset, p.f.Size)
				p.f.Cost = max(p.f.Cost, p.claude.cost)

				p.bytes = p.f.Offset - from
			}
		}()
	}
	for _, p := range work {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
}

// merge turns parsed lines into ledger writes, counting each Claude response once across all files at its largest reported usage.
// Usage logged before bucket from adds to the sums but not to the slices.
func (m *Meter) merge(work []*parsed, from int64) (batch, error) {
	b := batch{responses: map[string]*stored{}, sums: map[sumKey]core.Tokens{}, slices: map[sliceKey]core.Tokens{}, limits: map[string]core.Limit{}, from: from}
	var ids []string
	for _, p := range work {
		if p.reset {
			b.reset = append(b.reset, p.f.ID)
		}
		for _, r := range p.claude.responses {
			ids = append(ids, r.ID)
		}
		for _, t := range p.codex.turns {
			ids = append(ids, "codex/"+p.f.Session+"/"+t.ID)
		}
	}
	known, err := m.ledger.responses(ids)
	if err != nil {
		return b, err
	}
	reset := map[int64]bool{}
	for _, id := range b.reset {
		reset[id] = true
	}
	for id, s := range known {
		if reset[s.File] {
			delete(known, id)
		}
	}
	add := func(k sumKey, at int64, t core.Tokens) {
		b.sums[k] = b.sums[k].Plus(t)
		if bk := bucketOf(at); bk >= from {
			sk := sliceKey{File: k.File, Model: k.Model, Class: k.Class, Bucket: bk}
			b.slices[sk] = b.slices[sk].Plus(t)
		}
	}
	for _, p := range work {
		b.files = append(b.files, p.f)
		for _, r := range p.claude.responses {
			if s, ok := known[r.ID]; ok {
				grown := most(s.Tokens, r.Tokens)
				if grown != s.Tokens {
					add(s.sumKey, r.At, minus(grown, s.Tokens))
					s.Tokens = grown
					b.responses[r.ID] = s
				}
				continue
			}
			s := &stored{sumKey: sumKey{File: p.f.ID, Model: r.Model, Day: r.Day, Class: r.Class}, Tokens: r.Tokens}
			known[r.ID] = s
			b.responses[r.ID] = s
			add(s.sumKey, r.At, r.Tokens)
		}
		for _, t := range p.codex.turns {
			id := "codex/" + p.f.Session + "/" + t.ID
			if _, ok := known[id]; ok {
				continue
			}
			value := &stored{sumKey: sumKey{File: p.f.ID, Model: t.Model, Day: t.Day, Class: t.Class}, Tokens: t.Tokens}
			known[id] = value
			b.responses[id] = value
			add(value.sumKey, t.At, t.Tokens)
		}
		for _, l := range p.codex.limits {
			l.Profile = p.f.Profile
			l.ProfileLabel = p.f.ProfileLabel
			l.Account = p.f.Account
			key := l.Key()
			if old, ok := m.limits[key]; ok && old.Seen.After(l.Seen) {
				continue
			}
			if old, ok := b.limits[key]; !ok || !old.Seen.After(l.Seen) {
				b.limits[key] = l
			}
		}
	}
	return b, nil
}

func (m *Meter) apply(b batch) {
	for _, id := range b.reset {
		for k := range m.sums {
			if k.File == id {
				delete(m.sums, k)
			}
		}
		for k := range m.slices {
			if k.File == id {
				delete(m.slices, k)
			}
		}
	}
	for k := range m.slices {
		if k.Bucket < b.from {
			delete(m.slices, k)
		}
	}
	for k, t := range b.sums {
		m.sums[k] = m.sums[k].Plus(t)
	}
	for k, t := range b.slices {
		m.slices[k] = m.slices[k].Plus(t)
	}
	for _, f := range b.files {
		m.files[f.Path] = f
		m.byID[f.ID] = f
	}
	for _, l := range b.limits {
		key := l.Key()
		if old, ok := m.limits[key]; !ok || !old.Seen.After(l.Seen) {
			m.limits[key] = l
		}
	}
}

// build prices every sum and rolls subagents and child threads into their sessions.
func (m *Meter) build(now time.Time) core.Fleet {
	f := core.Fleet{Ready: true, At: now, Sessions: map[string]core.SessionSpend{}, Agents: map[string]core.Spend{}, Windows: map[string]time.Time{}}
	for _, e := range m.exhaustions {
		if !e.Recovered {
			f.Exhaustions = append(f.Exhaustions, e)
		}
	}
	today := day(now)
	weekStart := day(now.AddDate(0, 0, -6))
	sessions := map[string]*core.SessionSpend{}
	get := func(fl *file) *core.SessionSpend {
		s := sessions[profile.SessionKey(fl.Harness, fl.Profile, fl.Session)]
		if s == nil {
			s = &core.SessionSpend{Harness: fl.Harness, ID: fl.Session, Profile: fl.Profile, ProfileLabel: fl.ProfileLabel, Account: fl.Account, ByAccount: map[string]core.Spend{}, ByModel: map[string]core.Spend{}}
			sessions[profile.SessionKey(fl.Harness, fl.Profile, fl.Session)] = s
		}
		if s.Account != fl.Account {
			s.Account = ""
		}
		return s
	}
	for _, fl := range m.files {
		s := get(fl)
		if fl.Root {
			s.Reported = max(s.Reported, fl.Cost)
			if fl.Cursor.Parent != "" {
				s.Parent = profile.SessionKey(fl.Harness, fl.Profile, fl.Cursor.Parent)
			}
		}
		if t := time.Unix(0, fl.MTime); t.After(s.Last) {
			s.Last = t
		}
	}
	for k, t := range m.sums {
		fl := m.byID[k.File]
		if fl == nil {
			continue
		}
		sp := core.Spend{Tokens: t}
		if usd, ok := m.prices.Cost(k.Model, t, k.Class); ok {
			sp.USD = usd
		} else {
			sp.Unpriced = t.Total()
		}
		s := get(fl)
		s.Total = s.Total.Plus(sp)
		s.ByAccount[fl.Account] = s.ByAccount[fl.Account].Plus(sp)
		if fl.Root {
			s.Own = s.Own.Plus(sp)
		}
		s.ByModel[k.Model] = s.ByModel[k.Model].Plus(sp)
		if !fl.Root && fl.Harness == claudeName {
			agent := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(fl.physical()), "agent-"), ".jsonl")
			key := profile.SessionKey(fl.Harness, fl.Profile, fl.Session) + "/" + agent
			f.Agents[key] = f.Agents[key].Plus(sp)
		}
		if k.Day == today {
			f.Today = f.Today.Plus(sp)
		}
		if k.Day >= weekStart {
			f.Week = f.Week.Plus(sp)
		}
	}
	var windows []core.Limit
	for _, l := range current(append(readClaudeLimits(m.StateDir), slices.Collect(maps.Values(m.limits))...), now) {
		windows = append(windows, l)
		f.Windows[l.Key()] = l.Start()
	}
	for id, share := range m.shares(windows, now) {
		fl := m.byID[id]
		sp := core.Spend{Share: share}
		s := get(fl)
		s.Total = s.Total.Plus(sp)
		if fl.Root {
			s.Own = s.Own.Plus(sp)
		} else if fl.Harness == claudeName {
			agent := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(fl.physical()), "agent-"), ".jsonl")
			key := profile.SessionKey(fl.Harness, fl.Profile, fl.Session) + "/" + agent
			f.Agents[key] = f.Agents[key].Plus(sp)
		}
	}
	for child, parent := range m.edges {
		if s := sessions[child]; s != nil && s.Parent == "" {
			s.Parent = parent
		}
	}
	own := map[string]core.Spend{}
	for id, s := range sessions {
		own[id] = s.Total
	}
	for id, s := range sessions {
		seen := map[string]bool{id: true}
		for at := s.Parent; at != "" && !seen[at]; at = sessions[at].Parent {
			seen[at] = true
			p := sessions[at]
			if p == nil {
				break
			}
			p.Total = p.Total.Plus(own[id])
		}
	}
	for id, s := range sessions {
		f.Sessions[id] = *s
	}
	for _, l := range m.limits {
		f.Limits = append(f.Limits, l)
	}
	sort.Slice(f.Limits, func(i, j int) bool { return f.Limits[i].Name < f.Limits[j].Name })
	return f
}

// shares gives each file's fraction of the API-priced usage that fell inside every window between its start and now,
// counting Claude usage toward Claude windows and Codex usage toward Codex windows.
func (m *Meter) shares(windows []core.Limit, now time.Time) map[int64]map[string]float64 {
	if len(windows) == 0 {
		return nil
	}
	upTo := bucketOf(now.Unix())
	byFile := map[int64]map[string]float64{}
	totals := map[string]float64{}
	for k, t := range m.slices {
		fl := m.byID[k.File]
		if fl == nil || k.Bucket > upTo {
			continue
		}
		usd, priced := 0.0, false
		for _, w := range windows {
			if w.Harness != fl.Harness || (w.Account != "" && w.Account != fl.Account) || (w.Account == "" && profile.ID(w.Profile) != profile.ID(fl.Profile)) || (k.Bucket+1)*bucketSecs <= w.Start().Unix() {
				continue
			}
			if !priced {
				usd, _ = m.prices.Cost(k.Model, t, k.Class)
				priced = true
			}
			if usd <= 0 {
				break
			}
			sh := byFile[k.File]
			if sh == nil {
				sh = map[string]float64{}
				byFile[k.File] = sh
			}
			sh[w.Key()] += usd
			totals[w.Key()] += usd
		}
	}
	for _, sh := range byFile {
		for key := range sh {
			sh[key] /= totals[key]
		}
	}
	return byFile
}
