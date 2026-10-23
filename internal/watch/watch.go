// Package watch tells which files changed since they were last looked at, from the kernel's own notifications, so a reader
// polling thousands of files stats only the ones that moved.
package watch

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Stats keeps the last stat of each path looked at and serves it again while the kernel reports no change to the path, or,
// for a path that was missing, to its directory. Without kernel notifications every call stats.
type Stats struct {
	mu    sync.Mutex
	k     *kernel
	known map[string]look
	// drained is when Refresh last took in the kernel's reports.
	drained time.Time
	// changes counts the changes reported and the looks no notification could cover; touched is the count at each path's last.
	changes uint64
	touched map[string]uint64
}

func (s *Stats) touch(path string) {
	s.changes++
	if s.touched == nil {
		s.touched = map[string]uint64{}
	}
	s.touched[path] = s.changes
}

// Quiet reports whether every path was stat'd through the watch and none changed since Changes said stamp.
func (s *Stats) Quiet(paths []string, stamp uint64) bool {
	if s == nil || s.k == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range paths {
		l, ok := s.known[p]
		if !ok || s.touched[p] > stamp || s.touched[l.on] > stamp {
			return false
		}
	}
	return true
}

// Changes counts every change the kernel reported and every stat it could not watch, so a reader whose files all went
// unreported since a pass, with this count the same, knows none of them changed.
func (s *Stats) Changes() uint64 {
	if s == nil || s.k == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changes
}

// Watching reports whether Stat answers from the kernel's notifications on this platform.
func (s *Stats) Watching() bool { return s != nil && s.k != nil }

type look struct {
	info os.FileInfo
	err  error
	// on is the path whose notifications cover this one, and gen its change count when it was stat'd.
	on  string
	gen uint64
}

// New is a Stats on this platform's notifications; without them it just stats.
func New() *Stats { return &Stats{k: newKernel(), known: map[string]look{}} }

// Refresh takes in what the kernel reported since the last Refresh; Stat answers from it. A reader refreshes once per pass over
// its files, which then sees every change made before the pass began.
func (s *Stats) Refresh() {
	if s == nil || s.k == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.k.drain(s.touch)
	s.drained = time.Now()
}

// Lag is how old the kernel's reports Stat answers from may be when the caller did not Refresh.
const Lag = 50 * time.Millisecond

// Stat is os.Stat of path, answered from the last stat while nothing has touched the path as of the last Refresh, or within Lag. A watch starts before the
// stat it vouches for, so a change landing between the two is never missed.
func (s *Stats) Stat(path string) (os.FileInfo, error) {
	if s == nil || s.k == nil {
		return os.Stat(path)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.drained) > Lag {
		s.k.drain(s.touch)
		s.drained = time.Now()
	}
	if l, ok := s.known[path]; ok && s.k.quiet(l.on, l.gen) {
		return l.info, l.err
	}
	on := path
	if !s.k.watch(on) {
		on = filepath.Dir(path)
		if !s.k.watch(on) {
			delete(s.known, path)
			s.touch(path)
			return os.Stat(path)
		}
	}
	gen := s.k.gen[on]
	info, err := os.Stat(path)
	if err == nil && on != path || err != nil && on == path {
		delete(s.known, path)
		s.touch(path)
		return info, err
	}
	s.known[path] = look{info: info, err: err, on: on, gen: gen}
	return info, err
}
