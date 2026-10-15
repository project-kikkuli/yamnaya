package usage

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	pricesFile  = "prices.json"
	pricesEvery = 24 * time.Hour
)

// loadPrices prefers the copy refreshed into stateDir and falls back to the embedded snapshot.
func loadPrices(stateDir string) *Prices {
	if b, err := os.ReadFile(filepath.Join(stateDir, pricesFile)); err == nil {
		if p, err := ParsePrices(b); err == nil {
			return p
		}
	}
	p, err := ParsePrices(embeddedPrices)
	if err != nil {
		panic("embedded prices: " + err.Error())
	}
	return p
}

// refreshPrices fetches LiteLLM's price file when the local copy is a day old, and uses it from the next scan on.
func (m *Meter) refreshPrices() {
	path := filepath.Join(m.StateDir, pricesFile)
	if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) < pricesEvery {
		return
	}
	b, err := m.Fetch()
	if err != nil {
		return
	}
	if err := writeFile(path, b); err != nil {
		return
	}
	if p, err := ParsePrices(b); err == nil {
		m.scanMu.Lock()
		m.prices = p
		m.scanMu.Unlock()
		m.mu.Lock()
		m.last = time.Time{}
		m.mu.Unlock()
	}
}

// FetchPrices downloads LiteLLM's price file and trims it.
func FetchPrices() ([]byte, error) {
	c := http.Client{Timeout: 20 * time.Second}
	resp, err := c.Get(pricesURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch prices: %s", resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return Trim(raw)
}

func writeFile(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
