package disk

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

const (
	// goMinAge spares entries a running build may hold: go refreshes an entry's mtime at most once an hour when it uses
	// one, so anything older went unused for at least an hour.
	goMinAge = 2 * time.Hour
	// goFloorMinAge is what tier 2 spares: only what a build just wrote, since a busy machine's whole cache can be under an hour old.
	goFloorMinAge = 10 * time.Minute
	// goFloor is what the Go build cache keeps at tier 2, enough to not rebuild the standard library from nothing.
	goFloor = 1 << 30
	// staleAge is how long a temp build dir or DerivedData entry goes unchanged before it counts as abandoned.
	staleAge = 6 * time.Hour
	// uvWait bounds how long uv waits for its cache lock, which every running uv process holds shared for its whole life.
	uvWait = 2 * time.Minute
	// toolLimit bounds any other tool's run.
	toolLimit = 10 * time.Minute
	// enoughEvery is how many bytes a trim frees between reading free space.
	enoughEvery = 256 << 20
)

// CacheSteps are the regenerable caches of tier 1, cheapest for everyone first.
func CacheSteps(e Env) []Step {
	step := func(name string, run func(context.Context, Env) Report) Step {
		return Step{Name: name, Tier: 1, Run: func(ctx context.Context) Report { return run(ctx, e) }}
	}
	return []Step{
		step("go temp dirs", staleBuildDirs),
		step("uv prune", func(ctx context.Context, e Env) Report { return uvCache(ctx, e, "prune") }),
		step("pnpm prune", pnpmPrune),
		step("brew cleanup", brewCleanup),
		step("docker builder prune", dockerPrune),
		step("xcode deriveddata", derivedData),
		step("go cache trim", func(ctx context.Context, e Env) Report { return goTrim(ctx, e, 0, goMinAge) }),
	}
}

// HardSteps are the caches tier 2 empties after its worktree steps.
func HardSteps(e Env) []Step {
	return []Step{
		{Name: "uv clean", Tier: 2, Run: func(ctx context.Context) Report { return uvCache(ctx, e, "clean") }},
		{Name: "go cache floor", Tier: 2, Run: func(ctx context.Context) Report { return goTrim(ctx, e, goFloor, goFloorMinAge) }},
	}
}

func goEnv(ctx context.Context, e Env, name string) string {
	bin := e.Find("go")
	if bin == "" {
		return ""
	}
	out, err := e.Exec(ctx, "", time.Minute, bin, "env", name)
	if err != nil {
		return ""
	}
	return out
}

var goEntry = regexp.MustCompile(`^[0-9a-f]{64}-[ad]$`)

type goFile struct {
	path string
	at   time.Time
	size int64
}

// goTrim removes Go build cache entries oldest first until the disk has enough free or the cache is down to floor,
// sparing minAge. It reads each entry's mtime again just before removing it, as go's own trim does, and leaves the
// cache's bookkeeping files alone; go treats a missing entry as a miss and rebuilds it.
func goTrim(ctx context.Context, e Env, floor int64, minAge time.Duration) Report {
	dir := goEnv(ctx, e, "GOCACHE")
	switch {
	case dir == "" || dir == "off":
		return Report{Skipped: "go not installed or its cache is off"}
	case !e.may(dir):
		return Report{Skipped: "not this ym's cache: " + dir}
	case !e.Needed(dir):
		return Report{Skipped: dir + " has enough free"}
	}
	var files []goFile
	var total int64
	subdirs, err := os.ReadDir(dir)
	if err != nil {
		return Report{Err: err}
	}
	for _, sub := range subdirs {
		if !sub.IsDir() || len(sub.Name()) != 2 {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(dir, sub.Name()))
		if err != nil {
			continue
		}
		for _, f := range entries {
			if !goEntry.MatchString(f.Name()) {
				continue
			}
			if info, err := f.Info(); err == nil {
				files = append(files, goFile{filepath.Join(dir, sub.Name(), f.Name()), info.ModTime(), info.Size()})
				total += info.Size()
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].at.Before(files[j].at) })
	cutoff := e.Now().Add(-minAge)
	var freed, since int64
	removed := 0
	for _, f := range files {
		if ctx.Err() != nil || total-freed <= floor || !f.at.Before(cutoff) {
			break
		}
		if !e.Dry {
			if since >= enoughEvery {
				if e.Enough() {
					break
				}
				since = 0
			}
			info, err := os.Stat(f.path)
			if err != nil || !info.ModTime().Before(cutoff) || os.Remove(f.path) != nil {
				continue
			}
		}
		freed += f.size
		since += f.size
		removed++
	}
	verb := "removed"
	if e.Dry {
		verb = "would remove"
	}
	return Report{Did: fmt.Sprintf("%s %d of %d entries in %s", verb, removed, len(files), dir), Freed: freed, Quiet: removed == 0}
}

var goBuildDir = regexp.MustCompile(`^go-build[0-9]+$`)

// staleBuildDirs removes go-build temp dirs a killed build left: unchanged for staleAge and held by no live process.
func staleBuildDirs(ctx context.Context, e Env) Report {
	roots := []string{os.TempDir(), "/tmp", goEnv(ctx, e, "GOTMPDIR")}
	seen := map[string]bool{}
	var held []string
	var names []string
	var freed int64
	cutoff := e.Now().Add(-staleAge)
	for _, root := range roots {
		if root == "" || seen[root] || !e.may(root) || !e.Needed(root) {
			continue
		}
		seen[root] = true
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, d := range entries {
			if !d.IsDir() || !goBuildDir.MatchString(d.Name()) {
				continue
			}
			path := filepath.Join(root, d.Name())
			if !unchangedSince(path, cutoff) {
				continue
			}
			if held == nil {
				held = append(e.Holders(), "")
			}
			if slices.ContainsFunc(held, func(h string) bool { return h == path || strings.HasPrefix(h, path+"/") }) {
				continue
			}
			size := dirSize(path)
			if !e.Dry && os.RemoveAll(path) != nil {
				continue
			}
			freed += size
			names = append(names, path)
		}
	}
	verb := "removed"
	if e.Dry {
		verb = "would remove"
	}
	if len(names) == 0 {
		return Report{Did: "no stale go-build dirs", Quiet: true}
	}
	return Report{Did: fmt.Sprintf("%s %d go-build dirs: %s", verb, len(names), strings.Join(names, " ")), Freed: freed}
}

// unchangedSince reports whether path and its direct children last changed before cutoff.
func unchangedSince(path string, cutoff time.Time) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.ModTime().Before(cutoff) {
		return false
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, c := range entries {
		info, err := c.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			return false
		}
	}
	return true
}

func dirSize(path string) int64 {
	var n int64
	filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// Holders are the paths every live process holds: its working directory, open files and absolute arguments.
func Holders() []string {
	procs, _ := proc.List()
	var out []string
	for _, p := range procs {
		if d := proc.Cwd(p.PID); d != "" {
			out = append(out, d)
		}
		out = append(out, proc.Files(p.PID)...)
		for _, a := range p.Args {
			if _, after, ok := strings.Cut(a, "/"); ok {
				out = append(out, "/"+after)
			}
		}
	}
	return out
}

// Running reports whether a process runs the program name, looking past a node launcher.
func Running(name string) bool {
	procs, _ := proc.List()
	return slices.ContainsFunc(procs, func(p proc.Proc) bool { return p.Command() == name })
}

// uvCache runs uv cache prune or clean. uv takes its cache lock exclusively and every running uv holds it shared, so a
// busy machine can make it wait; past uvWait the step gives up for this pass.
func uvCache(ctx context.Context, e Env, verb string) Report {
	uv := e.Find("uv")
	if uv == "" {
		return Report{Skipped: "uv not installed"}
	}
	dir, err := e.Exec(ctx, "", time.Minute, uv, "cache", "dir")
	if err != nil {
		return Report{Err: err}
	}
	switch {
	case !e.may(dir):
		return Report{Skipped: "not this ym's cache: " + dir}
	case !e.Needed(dir):
		return Report{Skipped: dir + " has enough free"}
	case e.Dry:
		return Report{Did: "would run uv cache " + verb + " on " + dir}
	}
	out, err := e.Exec(ctx, "", uvWait, uv, "cache", verb)
	if errors.Is(err, context.DeadlineExceeded) {
		return Report{Skipped: "another uv holds the cache lock"}
	}
	return Report{Did: lastLine(out), Err: err, Quiet: strings.HasPrefix(lastLine(out), "No ")}
}

// pnpmPrune runs pnpm store prune, from a repository using pnpm when a version manager's shim needs one to pick a version.
func pnpmPrune(ctx context.Context, e Env) Report {
	pnpm := e.Find("pnpm")
	if pnpm == "" {
		return Report{Skipped: "pnpm not installed"}
	}
	if e.Running != nil && e.Running("pnpm") {
		return Report{Skipped: "a pnpm is running, and an install racing a prune can fail"}
	}
	dirs := []string{e.Home}
	for _, d := range e.Dirs {
		if _, err := os.Stat(filepath.Join(d, "pnpm-lock.yaml")); err == nil {
			dirs = append([]string{d}, dirs...)
		}
	}
	for _, dir := range dirs {
		store, err := e.Exec(ctx, dir, time.Minute, pnpm, "store", "path")
		if err != nil {
			continue
		}
		switch {
		case !e.may(store):
			return Report{Skipped: "not this ym's store: " + store}
		case !e.Needed(store):
			return Report{Skipped: store + " has enough free"}
		case e.Dry:
			return Report{Did: "would run pnpm store prune on " + store}
		}
		out, err := e.Exec(ctx, dir, toolLimit, pnpm, "store", "prune")
		return Report{Did: lastLine(out), Err: err}
	}
	return Report{Skipped: "pnpm has no version outside a project using it"}
}

func brewCleanup(ctx context.Context, e Env) Report {
	brew := e.Find("brew")
	if brew == "" {
		return Report{Skipped: "brew not installed"}
	}
	cache, err := e.Exec(ctx, "", time.Minute, brew, "--cache")
	if err != nil {
		return Report{Err: err}
	}
	switch {
	case !e.may(cache):
		return Report{Skipped: "not this ym's cache: " + cache}
	case !e.Needed(cache):
		return Report{Skipped: cache + " has enough free"}
	case e.Dry:
		return Report{Did: "would run brew cleanup --prune=all on " + cache}
	}
	out, err := e.Exec(ctx, "", toolLimit, brew, "cleanup", "--prune=all")
	return Report{Did: lastLine(out), Err: err}
}

// dockerPrune drops build cache unused for goMinAge. Docker's data may sit in a VM image no statfs here sees through, so
// it runs whichever filesystem is low.
func dockerPrune(ctx context.Context, e Env) Report {
	docker := e.Find("docker")
	switch {
	case docker == "":
		return Report{Skipped: "docker not installed"}
	case !e.may(""):
		return Report{Skipped: "not this ym's docker"}
	case e.Dry:
		return Report{Did: "would run docker builder prune --all --force --filter until=2h"}
	}
	out, err := e.Exec(ctx, "", toolLimit, docker, "builder", "prune", "--all", "--force", "--filter", "until=2h")
	return Report{Did: lastLine(out), Err: err}
}

// derivedData removes Xcode's per-project build dirs unchanged for staleAge, oldest first.
func derivedData(ctx context.Context, e Env) Report {
	dir := filepath.Join(e.Home, "Library", "Developer", "Xcode", "DerivedData")
	entries, err := os.ReadDir(dir)
	switch {
	case err != nil:
		return Report{Skipped: "no DerivedData"}
	case !e.may(dir):
		return Report{Skipped: "not this ym's cache: " + dir}
	case !e.Needed(dir):
		return Report{Skipped: dir + " has enough free"}
	}
	type project struct {
		path string
		at   time.Time
	}
	var old []project
	cutoff := e.Now().Add(-staleAge)
	for _, d := range entries {
		path := filepath.Join(dir, d.Name())
		if info, err := d.Info(); err == nil && d.IsDir() && unchangedSince(path, cutoff) {
			old = append(old, project{path, info.ModTime()})
		}
	}
	sort.Slice(old, func(i, j int) bool { return old[i].at.Before(old[j].at) })
	var names []string
	var freed int64
	for _, p := range old {
		if ctx.Err() != nil || !e.Dry && e.Enough() {
			break
		}
		size := dirSize(p.path)
		if !e.Dry && os.RemoveAll(p.path) != nil {
			continue
		}
		freed += size
		names = append(names, filepath.Base(p.path))
	}
	verb := "removed"
	if e.Dry {
		verb = "would remove"
	}
	return Report{Did: fmt.Sprintf("%s %d of %d projects: %s", verb, len(names), len(entries), strings.Join(names, " ")), Freed: freed, Quiet: len(names) == 0}
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return lines[len(lines)-1]
}
