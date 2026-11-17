package usage

import (
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

// NewReader observes persisted accounting; only the collector reads transcripts.
func NewReader(stateDir string) *Meter {
	m := New(stateDir)
	m.reader, m.Fetch = true, nil
	return m
}

func (m *Meter) readLedger(now time.Time) error {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	if m.closing.Load() {
		return ErrClosed
	}
	before, loaded := m.revision, m.ledger != nil
	if err := m.open(now); err != nil {
		return err
	}
	m.mu.Lock()
	last := m.fleet.At
	m.mu.Unlock()
	if loaded && before == m.revision && now.Sub(last) < time.Minute {
		m.mu.Lock()
		m.last, m.read = now, true
		m.mu.Unlock()
		return nil
	}
	m.edges = map[string]string{}
	profiles, err := profile.Load(m.StateDir)
	if err != nil {
		return err
	}
	for _, p := range profiles {
		if p.Harness != codexName {
			continue
		}
		for child, parent := range codexEdges(p.Root) {
			m.edges[profile.SessionKey(codexName, p.ID, child)] = profile.SessionKey(codexName, p.ID, parent)
		}
	}
	f := m.build(now)
	m.mu.Lock()
	m.fleet, m.last, m.read = f, now, true
	m.mu.Unlock()
	return nil
}

func (m *Meter) Read(now time.Time) (core.Fleet, error) {
	if err := m.readLedger(now); err != nil {
		return core.Fleet{}, err
	}
	m.mu.Lock()
	f := m.fleet
	f.Read = m.read
	m.mu.Unlock()
	return f, nil
}
