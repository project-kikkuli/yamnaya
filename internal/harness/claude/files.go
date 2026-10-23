package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/watch"
)

type listing struct {
	modified time.Time
	paths    []string
}

type jsonFile struct {
	size     int64
	modified time.Time
	info     os.FileInfo
	raw      []byte
	ok       bool
}

// glob lists dir's entries matching pattern, reading the directory again only after its modification time changes.
func (a *Adapter) glob(dir, pattern string) []string {
	info, err := a.stat(dir)
	a.files.Lock()
	defer a.files.Unlock()
	if err != nil || !info.IsDir() {
		if _, ok := a.listings[dir+"\x00"+pattern]; ok {
			a.gen.Add(1)
			delete(a.listings, dir+"\x00"+pattern)
		}
		return nil
	}
	key := dir + "\x00" + pattern
	if l, ok := a.listings[key]; ok && l.modified.Equal(info.ModTime()) {
		return l.paths
	}
	entries, _ := os.ReadDir(dir)
	var paths []string
	for _, e := range entries {
		if ok, _ := filepath.Match(pattern, e.Name()); ok {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	if a.listings == nil {
		a.listings = map[string]listing{}
	}
	a.gen.Add(1)
	a.listings[key] = listing{modified: info.ModTime(), paths: paths}
	return paths
}

// meta reads a subagent's meta file.
func (a *Adapter) meta(path string) (agentMeta, bool) {
	var m agentMeta
	_, ok := a.parsed(path, &m)
	return m, ok
}

// parsed decodes the JSON file at path into v, opening it again only after its size, modification time or identity changes:
// a stat costs far less than an open on a machine that scans every file opened.
func (a *Adapter) parsed(path string, v any) (modified time.Time, ok bool) {
	info, err := a.stat(path)
	a.files.Lock()
	defer a.files.Unlock()
	if err != nil {
		if _, ok := a.jsons[path]; ok {
			a.gen.Add(1)
			delete(a.jsons, path)
		}
		return time.Time{}, false
	}
	if f, ok := a.jsons[path]; ok && f.size == info.Size() && f.modified.Equal(info.ModTime()) && os.SameFile(f.info, info) {
		return f.modified, f.ok && json.Unmarshal(f.raw, v) == nil
	}
	f := jsonFile{size: info.Size(), modified: info.ModTime(), info: info}
	if b, err := os.ReadFile(path); err == nil && json.Valid(b) {
		f.raw, f.ok = b, true
	}
	if a.jsons == nil {
		a.jsons = map[string]jsonFile{}
	}
	a.gen.Add(1)
	a.jsons[path] = f
	return f.modified, f.ok && json.Unmarshal(f.raw, v) == nil
}

// Generation counts every change the adapter read in Claude Code's files: a state or meta file rewritten, a directory listing
// changed, a transcript grown, replaced or gone.
func (a *Adapter) Generation() uint64 { return a.gen.Load() }

// stat is os.Stat answered from the kernel's notifications where it can: a file nothing touched since the last Discover began
// costs no system call.
func (a *Adapter) stat(path string) (os.FileInfo, error) {
	a.watchOnce.Do(func() { a.watch = watch.New() })
	a.collecting.Lock()
	if a.collect != nil {
		a.collect = append(a.collect, path)
	}
	a.collecting.Unlock()
	return a.watch.Stat(path)
}
