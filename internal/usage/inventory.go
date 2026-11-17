package usage

import (
	"os"
	"path/filepath"
	"slices"
	"time"
)

type sourceInventory struct {
	harness, root string
	sources       []source
	dirs          map[string]os.FileInfo
	refreshed     time.Time
}

func (i *sourceInventory) scan(now time.Time, force bool) []source {
	discover := force || i.dirs == nil || now.Sub(i.refreshed) >= coldEvery
	before := map[string]os.FileInfo{}
	if !discover {
		for path, old := range i.dirs {
			info, err := os.Stat(path)
			before[path] = info
			if err != nil && old != nil || err == nil && (old == nil || !os.SameFile(old, info) || !info.ModTime().Equal(old.ModTime())) {
				discover = true
				break
			}
		}
	}
	if discover {
		lookMu.Lock()
		for path := range i.dirs {
			delete(looks, path)
		}
		lookMu.Unlock()
		if i.harness == claudeName {
			i.sources = claudeSources(i.root)
		} else {
			i.sources = codexSources(i.root)
		}
		i.dirs = map[string]os.FileInfo{i.root: nil}
		if i.harness == claudeName {
			i.dirs[filepath.Join(i.root, "projects")] = nil
		} else {
			i.dirs[filepath.Join(i.root, "sessions")] = nil
			i.dirs[filepath.Join(i.root, "archived_sessions")] = nil
		}
		for _, s := range i.sources {
			for dir := filepath.Dir(s.Path); dir != i.root && dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
				i.dirs[dir] = nil
			}
		}
		for path := range i.dirs {
			if info, ok := before[path]; ok {
				i.dirs[path] = info
			} else {
				i.dirs[path], _ = os.Stat(path)
			}
		}
		i.refreshed = now
	}
	for n, s := range i.sources {
		if !force && !discover && now.Sub(time.Unix(0, s.MTime)) >= coldAfter {
			continue
		}
		if force || discover {
			lookMu.Lock()
			delete(looks, s.Path)
			lookMu.Unlock()
		}
		if fresh, ok := stat(s.Path, s.Session, s.Root); ok {
			i.sources[n] = fresh
		} else {
			i.sources[n].Path = ""
		}
	}
	i.sources = slices.DeleteFunc(i.sources, func(s source) bool { return s.Path == "" })
	return i.sources
}

type cachedEpochs struct {
	source source
	epochs []source
	at     time.Time
}

func (m *Meter) cachedAccountSources(s source, now time.Time, incremental bool) ([]source, error) {
	if m.epochCache == nil {
		m.epochCache = map[string]cachedEpochs{}
	}
	if cached, ok := m.epochCache[s.Path]; incremental && ok && cached.source == s && now.Sub(cached.at) < coldEvery {
		return cached.epochs, nil
	}
	epochs, err := m.accountSources(s)
	if err == nil {
		m.epochCache[s.Path] = cachedEpochs{source: s, epochs: epochs, at: now}
	}
	return epochs, err
}
