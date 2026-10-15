package usage

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
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
	StateDir  string
	// Every is the least time between scans that Fleet starts.
	Every time.Duration
	// Fetch downloads a trimmed price file; nil keeps prices offline.
	Fetch func() ([]byte, error)

	scanMu  sync.Mutex
	mu      sync.Mutex
	ledger  *ledger
	files   map[string]*file
	byID    map[int64]*file
	nextID  int64
	sums    map[sumKey]core.Tokens
	limits  map[string]core.Limit
	edges   map[string]string
	prices  *Prices
	fleet   core.Fleet
	running bool
	last    time.Time
	err     error
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

func (m *Meter) open() error {
	if m.ledger != nil {
		return nil
	}
	l, err := openLedger(filepath.Join(m.StateDir, "ym.db"))
	if err != nil {
		return err
	}
	files, sums, limits, err := l.load()
	if err != nil {
		l.close()
		return err
	}
	m.ledger, m.files, m.sums, m.limits = l, files, sums, limits
	m.byID = map[int64]*file{}
	for _, f := range files {
		m.byID[f.ID] = f
		m.nextID = max(m.nextID, f.ID)
	}
	m.prices = loadPrices(m.StateDir)
	if m.Fetch != nil {
		go m.refreshPrices()
	}
	return nil
}

// parsed is what reading one file's new lines produced.
type parsed struct {
	f      *file
	reset  bool
	claude claudeChunk
	codex  codexChunk
	bytes  int64
}

// Scan reads every new transcript line, stores what it adds and rebuilds the fleet view.
func (m *Meter) Scan(now time.Time) (Stats, error) {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	start := time.Now()
	var st Stats
	if err := m.open(); err != nil {
		return st, err
	}
	var srcs []source
	for _, s := range claudeSources(m.ClaudeDir) {
		s.Harness = claudeName
		srcs = append(srcs, s)
	}
	for _, s := range codexSources(m.CodexHome) {
		s.Harness = codexName
		srcs = append(srcs, s)
	}
	st.Files = len(srcs)
	var work []*parsed
	for _, s := range srcs {
		old := m.files[s.Path]
		if old != nil && old.Size == s.Size && old.MTime == s.MTime {
			continue
		}
		f := &file{source: s}
		p := &parsed{f: f}
		switch {
		case old == nil:
			m.nextID++
			f.ID = m.nextID
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
	st.Parse = time.Since(t0)
	sort.Slice(work, func(i, j int) bool { return work[i].f.Path < work[j].f.Path })
	b, err := m.merge(work)
	if err != nil {
		return st, err
	}
	for _, p := range work {
		st.Bytes += p.bytes
		st.Responses += len(p.claude.responses) + len(p.codex.turns)
	}
	t0 = time.Now()
	if len(work) > 0 {
		if err := m.ledger.commit(b); err != nil {
			return st, err
		}
	}
	st.Commit = time.Since(t0)
	m.apply(b)
	m.edges = codexEdges(m.CodexHome)
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
				if p.f.Harness == claudeName {
					p.f.Offset, _ = readLines(p.f.Path, from, func(line []byte) { parseClaude(line, &p.claude) })
					p.f.Cost = max(p.f.Cost, p.claude.cost)
				} else {
					p.codex.limits = map[string]core.Limit{}
					p.f.Offset, _ = readLines(p.f.Path, from, func(line []byte) { parseCodex(line, &p.f.Cursor, &p.codex) })
				}
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
func (m *Meter) merge(work []*parsed) (batch, error) {
	b := batch{responses: map[string]*stored{}, sums: map[sumKey]core.Tokens{}, limits: map[string]core.Limit{}}
	var ids []string
	for _, p := range work {
		if p.reset {
			b.reset = append(b.reset, p.f.ID)
		}
		for _, r := range p.claude.responses {
			ids = append(ids, r.ID)
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
	add := func(k sumKey, t core.Tokens) {
		b.sums[k] = b.sums[k].Plus(t)
	}
	for _, p := range work {
		b.files = append(b.files, p.f)
		for _, r := range p.claude.responses {
			if s, ok := known[r.ID]; ok {
				grown := most(s.Tokens, r.Tokens)
				if grown != s.Tokens {
					add(s.sumKey, minus(grown, s.Tokens))
					s.Tokens = grown
					b.responses[r.ID] = s
				}
				continue
			}
			s := &stored{sumKey: sumKey{File: p.f.ID, Model: r.Model, Day: r.Day, Class: r.Class}, Tokens: r.Tokens}
			known[r.ID] = s
			b.responses[r.ID] = s
			add(s.sumKey, r.Tokens)
		}
		for _, t := range p.codex.turns {
			add(sumKey{File: p.f.ID, Model: t.Model, Day: t.Day, Class: t.Class}, t.Tokens)
		}
		for _, l := range p.codex.limits {
			key := l.Harness + "/" + l.Name
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
	}
	for k, t := range b.sums {
		m.sums[k] = m.sums[k].Plus(t)
	}
	for _, f := range b.files {
		m.files[f.Path] = f
		m.byID[f.ID] = f
	}
	for _, l := range b.limits {
		key := l.Harness + "/" + l.Name
		if old, ok := m.limits[key]; !ok || !old.Seen.After(l.Seen) {
			m.limits[key] = l
		}
	}
}

// build prices every sum and rolls subagents and child threads into their sessions.
func (m *Meter) build(now time.Time) core.Fleet {
	f := core.Fleet{Ready: true, At: now, Sessions: map[string]core.SessionSpend{}}
	today := day(now)
	weekStart := day(now.AddDate(0, 0, -6))
	sessions := map[string]*core.SessionSpend{}
	get := func(fl *file) *core.SessionSpend {
		s := sessions[fl.Session]
		if s == nil {
			s = &core.SessionSpend{Harness: fl.Harness, ID: fl.Session, ByModel: map[string]core.Spend{}}
			sessions[fl.Session] = s
		}
		return s
	}
	for _, fl := range m.files {
		s := get(fl)
		if fl.Root {
			s.Reported = max(s.Reported, fl.Cost)
			if fl.Cursor.Parent != "" {
				s.Parent = fl.Cursor.Parent
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
		if fl.Root {
			s.Own = s.Own.Plus(sp)
		}
		s.ByModel[k.Model] = s.ByModel[k.Model].Plus(sp)
		if k.Day == today {
			f.Today = f.Today.Plus(sp)
		}
		if k.Day >= weekStart {
			f.Week = f.Week.Plus(sp)
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
