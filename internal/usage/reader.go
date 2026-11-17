package usage

import (
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

// NewReader observes persisted accounting; only the collector reads transcripts.
func NewReader(stateDir string) *Meter {
	return &Meter{StateDir: stateDir, Every: 5 * time.Second, reader: true}
}

var errNoUsageData = errors.New("no collected usage")

func openLedgerReader(path string) (*ledger, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, errNoUsageData
	} else if err != nil {
		return nil, err
	}
	q := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(5000)", "query_only(ON)", "cache_size(-8192)"}}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var slices int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='ym_usage_slices'`).Scan(&slices); err != nil {
		db.Close()
		return nil, err
	}
	if slices == 0 {
		db.Close()
		return nil, errNoUsageData
	}
	return &ledger{db: db, reader: true}, nil
}

func (m *Meter) readLedger(now time.Time) error {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	if m.closing.Load() {
		return ErrClosed
	}
	before, loaded := m.revision, m.ledger != nil
	if !loaded {
		l, err := openLedgerReader(filepath.Join(m.StateDir, "ym.db"))
		if errors.Is(err, errNoUsageData) {
			m.mu.Lock()
			m.last, m.read = now, true
			m.mu.Unlock()
			return nil
		}
		if err != nil {
			return err
		}
		m.ledger, m.prices = l, loadPrices(m.StateDir)
	}
	if _, err := m.ledger.db.Exec(`BEGIN`); err != nil {
		return err
	}
	defer m.ledger.db.Exec(`ROLLBACK`)
	revision, err := m.ledger.revision()
	if err != nil {
		return err
	}
	if !loaded || revision != before {
		if err = m.reload(now); err != nil {
			return err
		}
		m.revision = revision
	}
	if _, err = m.ledger.db.Exec(`COMMIT`); err != nil {
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
	f.Limits = current(append(readClaudeLimits(m.StateDir), f.Limits...), now)
	return f, nil
}
