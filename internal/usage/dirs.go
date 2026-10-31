package usage

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type listing struct {
	modified, at time.Time
	entries      []fs.DirEntry
}

// racy is longer than any filesystem's timestamp tick, so a listing taken this long after its directory last changed holds every
// entry made in that tick.
const racy = 2 * time.Second

var (
	listMu   sync.Mutex
	listings = map[string]listing{}
)

// A file or directory unchanged for coldAfter is looked at again only every coldEvery: transcripts pile up with history, and only
// the few a running session writes change, so a resumed old one counts within coldEvery while a scan stats only what is warm.
const (
	coldAfter = time.Hour
	coldEvery = 2 * time.Minute
)

type looked struct {
	info fs.FileInfo
	err  error
	at   time.Time
}

var (
	clock  = time.Now
	lookMu sync.Mutex
	looks  = map[string]looked{}
)

// lstat is os.Stat of path, or what it said last when path has been cold since then and was looked at within coldEvery.
func lstat(path string) (fs.FileInfo, error) {
	now := clock()
	lookMu.Lock()
	l, ok := looks[path]
	lookMu.Unlock()
	if ok && l.err == nil && now.Sub(l.info.ModTime()) >= coldAfter && now.Sub(l.at) < coldEvery {
		return l.info, nil
	}
	info, err := os.Stat(path)
	lookMu.Lock()
	if err != nil {
		delete(looks, path)
	} else {
		looks[path] = looked{info: info, at: now}
	}
	lookMu.Unlock()
	return info, err
}

// readDir lists dir, reading it again only after its modification time changes; nil when dir is missing.
func readDir(dir string) []fs.DirEntry {
	info, err := lstat(dir)
	listMu.Lock()
	defer listMu.Unlock()
	if err != nil || !info.IsDir() {
		delete(listings, dir)
		return nil
	}
	if l, ok := listings[dir]; ok && l.modified.Equal(info.ModTime()) && l.at.Sub(l.modified) >= racy {
		return l.entries
	}
	at := clock()
	entries, _ := os.ReadDir(dir)
	listings[dir] = listing{modified: info.ModTime(), at: at, entries: entries}
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
