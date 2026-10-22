package usage

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type listing struct {
	modified time.Time
	entries  []fs.DirEntry
}

var (
	listMu   sync.Mutex
	listings = map[string]listing{}
)

// readDir lists dir, reading it again only after its modification time changes; nil when dir is missing.
func readDir(dir string) []fs.DirEntry {
	info, err := os.Stat(dir)
	listMu.Lock()
	defer listMu.Unlock()
	if err != nil || !info.IsDir() {
		delete(listings, dir)
		return nil
	}
	if l, ok := listings[dir]; ok && l.modified.Equal(info.ModTime()) {
		return l.entries
	}
	entries, _ := os.ReadDir(dir)
	listings[dir] = listing{modified: info.ModTime(), entries: entries}
	return entries
}

// walk calls fn for every file under dir in lexical order, like filepath.WalkDir without following links.
func walk(dir string, fn func(path string, d fs.DirEntry)) {
	for _, e := range readDir(dir) {
		path := filepath.Join(dir, e.Name())
		if e.IsDir() {
			walk(path, fn)
			continue
		}
		fn(path, e)
	}
}
