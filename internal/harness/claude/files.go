package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
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
	info, err := os.Stat(dir)
	a.files.Lock()
	defer a.files.Unlock()
	if err != nil || !info.IsDir() {
		delete(a.listings, dir+"\x00"+pattern)
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
	info, err := os.Stat(path)
	a.files.Lock()
	defer a.files.Unlock()
	if err != nil {
		delete(a.jsons, path)
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
	a.jsons[path] = f
	return f.modified, f.ok && json.Unmarshal(f.raw, v) == nil
}
