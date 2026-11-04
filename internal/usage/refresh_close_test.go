package usage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAPriceRefreshWritesNothingOnceTheMeterIsClosed(t *testing.T) {
	for _, c := range []struct {
		name        string
		closeFirst  bool
		wantWritten bool
	}{
		{"the fetch returns while the meter is open", false, true},
		{"the fetch returns after the meter closed", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			fetching, answer, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			m := &Meter{StateDir: dir, Fetch: func() ([]byte, error) {
				close(fetching)
				<-answer
				return embeddedPrices, nil
			}}
			go func() {
				defer close(done)
				m.refreshPrices()
			}()
			<-fetching
			if c.closeFirst {
				if err := m.Close(); err != nil {
					t.Fatal(err)
				}
			}
			close(answer)
			<-done
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(filepath.Join(dir, pricesFile))
			if written := statErr == nil; written != c.wantWritten || len(entries) != map[bool]int{true: 1, false: 0}[c.wantWritten] {
				t.Fatalf("written %v, want %v; state dir holds %v", written, c.wantWritten, entries)
			}
		})
	}
}
