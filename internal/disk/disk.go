// Package disk keeps a machine from running out of disk: it reads free space with statfs, names how low it is, and runs
// reclaim passes over regenerable caches.
package disk

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// How low a filesystem is, as fractions of its size. A pass stops at TargetFree so it does not thrash, and an episode
// ends above ClearFree.
const (
	Tier1Free  = 0.20
	Tier2Free  = 0.10
	Tier3Free  = 0.05
	TargetFree = 0.25
	ClearFree  = 0.20
)

// FreeFileEnv names a file holding a fraction that replaces every filesystem's free space, for a drill under a rehearsal root.
const FreeFileEnv = "YAMNAYA_DISK_FREE"

const rehearsalMark = ".ym-rehearsal"

// Usage is one filesystem as seen from Path.
type Usage struct {
	Path  string `json:"path"`
	Dev   uint64 `json:"dev"`
	Total uint64 `json:"total"`
	Avail uint64 `json:"avail"`
}

// Free is the fraction of the filesystem an unprivileged process can still write.
func (u Usage) Free() float64 {
	if u.Total == 0 {
		return 1
	}
	return float64(u.Avail) / float64(u.Total)
}

// TierOf is how low free is: 0 while healthy, else 1 to 3.
func TierOf(free float64) int {
	switch {
	case free < Tier3Free:
		return 3
	case free < Tier2Free:
		return 2
	case free < Tier1Free:
		return 1
	}
	return 0
}

// Stat reads the filesystem holding path.
func Stat(path string) (Usage, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return Usage{}, err
	}
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return Usage{}, err
	}
	u := Usage{Path: path, Dev: uint64(st.Dev), Total: fs.Blocks * uint64(fs.Bsize), Avail: fs.Bavail * uint64(fs.Bsize)}
	if free, ok := drillFree(); ok {
		u.Avail = uint64(free * float64(u.Total))
	}
	return u, nil
}

func drillFree() (float64, bool) {
	file := os.Getenv(FreeFileEnv)
	if file == "" || !rehearsing(os.Getenv("YAMNAYA_STATE_DIR")) {
		return 0, false
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return 0, false
	}
	free, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	return free, err == nil && !math.IsNaN(free)
}

func rehearsing(dir string) bool {
	if dir == "" {
		return false
	}
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, rehearsalMark)); err == nil {
			return true
		}
		if filepath.Dir(d) == d {
			return false
		}
	}
}

// Each is the distinct filesystems holding paths, skipping paths that do not exist and pseudo filesystems with no size.
func Each(stat func(string) (Usage, error), paths []string) []Usage {
	var out []Usage
	seen := map[uint64]bool{}
	for _, p := range paths {
		u, err := stat(p)
		if err != nil || u.Total == 0 || seen[u.Dev] {
			continue
		}
		seen[u.Dev] = true
		out = append(out, u)
	}
	return out
}

// Lowest is the filesystem with the least free among paths; ok is false when none could be read.
func Lowest(stat func(string) (Usage, error), paths []string) (low Usage, ok bool) {
	for _, u := range Each(stat, paths) {
		if !ok || u.Free() < low.Free() {
			low, ok = u, true
		}
	}
	return low, ok
}

// Watched are the paths whose filesystems admission reads with no setup: home, the temp dir and the Go build cache.
func Watched() []string {
	paths := []string{os.TempDir()}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, home)
	}
	if cache := os.Getenv("GOCACHE"); cache != "" && cache != "off" {
		paths = append(paths, cache)
	} else if dir, err := os.UserCacheDir(); err == nil {
		paths = append(paths, filepath.Join(dir, "go-build"))
	}
	return paths
}

// Held is the filesystem below Tier3Free among Watched and extra, the reason new slots wait.
func Held(extra ...string) (Usage, bool) {
	low, ok := Lowest(Stat, append(Watched(), extra...))
	return low, ok && low.Free() < Tier3Free
}
