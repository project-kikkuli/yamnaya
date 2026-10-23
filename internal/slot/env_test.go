package slot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerSharesLeaveHostCoresAvailable(t *testing.T) {
	for _, c := range []struct{ cores, slots, want int }{{12, 4, 2}, {8, 3, 2}, {64, 3, 20}, {2, 1, 1}, {1, 1, 1}} {
		if got := (Pool{Cores: c.cores, Capacity: c.slots}).Workers(); got != c.want {
			t.Errorf("%d cores, %d slots: %d workers, want %d", c.cores, c.slots, got, c.want)
		}
	}
}

func TestCommandsInheritBoundedWorkersAndKeepSmallerLimits(t *testing.T) {
	for _, c := range []struct {
		name string
		env  []string
		want map[string]string
	}{
		{"defaults", []string{"PATH=" + os.Getenv("PATH"), "OTHER=kept"}, map[string]string{
			"GOMAXPROCS": "2", "GOFLAGS": "-p=2", "MAKEFLAGS": "-j2", "CARGO_BUILD_JOBS": "2",
			"PYTEST_XDIST_AUTO_NUM_WORKERS": "2", "UV_THREADPOOL_SIZE": "2", "OTHER": "kept",
			"NODE_OPTIONS": "--v8-pool-size=2",
		}},
		{"large", []string{"GOMAXPROCS=12", "GOFLAGS=-tags=integration -p=12", "MAKEFLAGS=-k -j12 --jobserver-auth=3,4", "CARGO_BUILD_JOBS=12"}, map[string]string{
			"GOMAXPROCS": "2", "GOFLAGS": "-tags=integration -p=12 -p=2", "MAKEFLAGS": "-k -j12 -j2", "CARGO_BUILD_JOBS": "2",
		}},
		{"smaller", []string{"GOMAXPROCS=1", "GOFLAGS=-p=8 '-p=1' -tags=foo", "MAKEFLAGS=--jobs=1 -k", "RAYON_NUM_THREADS=1", "NODE_OPTIONS=--no-warnings --v8-pool-size=1"}, map[string]string{
			"GOMAXPROCS": "1", "GOFLAGS": "-p=8 '-p=1' -tags=foo -p=1", "MAKEFLAGS": "--jobs=1 -k -j1", "RAYON_NUM_THREADS": "1",
			"NODE_OPTIONS": "--no-warnings --v8-pool-size=1 --v8-pool-size=1",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "env")
			code, err := Run([]string{"sh", "-c", "env > \"$1\"", "sh", path}, 0, Environment(c.env, 2))
			if err != nil || code != 0 {
				t.Fatalf("run: %d, %v", code, err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, line := range strings.Split(string(raw), "\n") {
				key, value, _ := strings.Cut(line, "=")
				got[key] = value
			}
			for key, want := range c.want {
				if got[key] != want {
					t.Errorf("%s=%q, want %q", key, got[key], want)
				}
			}
		})
	}
}
