package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAServeThatWaitedForTheLockStillUpgradesFromTheYmItStartedAs(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(t.TempDir(), "ym")
	if err := os.WriteFile(older, []byte("the ym this serve started as"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name     string
		started  string
		upgraded bool
	}{
		{"a newer ym was installed while it waited", older, true},
		{"it runs the ym on disk", self, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			started, err := os.Stat(c.started)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3500*time.Millisecond)
			defer cancel()
			upgraded := make(chan bool, 1)
			monitorWorkService(ctx, cancel, t.TempDir(), started, upgraded)
			select {
			case got := <-upgraded:
				if got != c.upgraded {
					t.Fatalf("upgraded = %v, want %v", got, c.upgraded)
				}
			default:
				if c.upgraded {
					t.Fatal("serve never noticed it runs an older ym than the one on disk")
				}
			}
		})
	}
}
