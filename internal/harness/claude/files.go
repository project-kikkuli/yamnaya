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

type metaFile struct {
	size     int64
	modified time.Time
	meta     agentMeta
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

// meta reads a subagent's meta file, parsing it again only after its size or modification time changes.
func (a *Adapter) meta(path string) (agentMeta, bool) {
	info, err := os.Stat(path)
	a.files.Lock()
	defer a.files.Unlock()
	if err != nil {
		delete(a.metas, path)
		return agentMeta{}, false
	}
	if m, ok := a.metas[path]; ok && m.size == info.Size() && m.modified.Equal(info.ModTime()) {
		return m.meta, m.ok
	}
	m := metaFile{size: info.Size(), modified: info.ModTime()}
	if b, err := os.ReadFile(path); err == nil {
		m.ok = json.Unmarshal(b, &m.meta) == nil
	}
	if a.metas == nil {
		a.metas = map[string]metaFile{}
	}
	a.metas[path] = m
	return m.meta, m.ok
}
