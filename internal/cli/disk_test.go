package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/disk"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux/tmuxtest"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func usageAt(free *float64) func(string) (disk.Usage, error) {
	return func(path string) (disk.Usage, error) {
		return disk.Usage{Path: path, Dev: 1, Total: 1000, Avail: uint64(*free * 1000)}, nil
	}
}

func TestTheWatcherStartsAPassOnlyWhenLowAndNotJustTried(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name  string
		free  float64
		state diskState
		start bool
	}{
		{"healthy", 0.30, diskState{}, false},
		{"exactly the threshold", 0.20, diskState{}, false},
		{"tier 1", 0.19, diskState{}, true},
		{"same tier tried a moment ago", 0.19, diskState{LastPass: now.Add(-time.Minute), LastTier: 1}, false},
		{"same tier tried long ago", 0.19, diskState{LastPass: now.Add(-diskRetryAfter), LastTier: 1}, true},
		{"worse than the last try", 0.09, diskState{LastPass: now.Add(-time.Minute), LastTier: 1}, true},
		{"better than the last try", 0.15, diskState{LastPass: now.Add(-time.Minute), LastTier: 2}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := c.state.save(dir); err != nil {
				t.Fatal(err)
			}
			started := 0
			w := &diskWatcher{dir: dir, paths: []string{"/x"}, stat: usageAt(&c.free), now: func() time.Time { return now }, log: &strings.Builder{},
				start: func() error { started++; return nil }}
			if err := w.tick(); err != nil {
				t.Fatal(err)
			}
			if (started == 1) != c.start {
				t.Fatalf("started %d, want %v", started, c.start)
			}
		})
	}
}

func TestTheWatcherStartsNothingWhileAPassRuns(t *testing.T) {
	dir := t.TempDir()
	unlock, ok, err := lockDisk(dir)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer unlock()
	free := 0.01
	w := &diskWatcher{dir: dir, paths: []string{"/x"}, stat: usageAt(&free), now: time.Now, log: &strings.Builder{},
		start: func() error { t.Error("started a second pass"); return nil }}
	if err := w.tick(); err != nil {
		t.Fatal(err)
	}
}

func diskCore(t *testing.T, dir string) *core.Core {
	t.Helper()
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
}

func ownerItems(t *testing.T, c *core.Core) []core.Item {
	t.Helper()
	items, err := c.Store.Items(core.ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestAnEpisodeFilesOneOwnerItemAcrossRestartsAndAFreshEpisodeFilesAnother(t *testing.T) {
	dir := t.TempDir()
	free := 0.08
	pass := func(c *core.Core, tier int) {
		t.Helper()
		env := disk.Env{Paths: []string{"/x"}, Stat: usageAt(&free), Now: time.Now, Home: t.TempDir(), Force: tier > 0}
		r := reclaimer{core: c, dir: dir, env: env, log: func(string, string) error { return nil },
			measure: func(disk.Usage) ([]disk.Consumer, bool) {
				return []disk.Consumer{{Path: "/home/me/dev", Size: 137 << 30}}, true
			}}
		if err := r.run(context.Background(), tier, &strings.Builder{}); err != nil {
			t.Fatal(err)
		}
	}
	// a serve restart is a new core and a new process over the same state dir
	pass(diskCore(t, dir), 0)
	c := diskCore(t, dir)
	items := ownerItems(t, c)
	if len(items) != 1 {
		t.Fatalf("after the first pass: %+v", items)
	}
	it, err := c.Store.Item(items[0].Key())
	if err != nil {
		t.Fatal(err)
	}
	if it.Kind != core.KindChore || it.Recipient != core.Owner || !strings.Contains(it.Rec, "/home/me/dev 137.0 GiB") {
		t.Fatalf("item %+v", it)
	}
	pass(c, 0)
	pass(diskCore(t, dir), 2)
	free = 0.04
	pass(diskCore(t, dir), 0)
	if got := ownerItems(t, c); len(got) != 1 {
		t.Fatalf("the same episode filed again: %+v", got)
	}
	free = 0.15
	w := &diskWatcher{dir: dir, paths: []string{"/x"}, stat: usageAt(&free), now: time.Now, log: &strings.Builder{}, start: func() error { return nil }}
	if err := w.tick(); err != nil {
		t.Fatal(err)
	}
	if readDiskState(dir).Filed == "" {
		t.Fatal("15% free ended the episode; only above 20% does")
	}
	free = 0.21
	if err := w.tick(); err != nil {
		t.Fatal(err)
	}
	if readDiskState(dir).Filed != "" {
		t.Fatal("21% free did not end the episode")
	}
	free = 0.08
	pass(diskCore(t, dir), 0)
	if got := ownerItems(t, c); len(got) != 2 {
		t.Fatalf("a new episode did not file: %+v", got)
	}
}

func TestAPassFilesNothingWhenItWasNotFull(t *testing.T) {
	for _, c := range []struct {
		name string
		free float64
		tier int
	}{
		{"tier 1 pass leaves free above tier 2", 0.15, 0},
		{"forced tier 1 on a low disk has not tried everything", 0.08, 1},
		{"a forced tier 2 on a healthy disk", 0.40, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			core := diskCore(t, dir)
			free := c.free
			env := disk.Env{Paths: []string{"/x"}, Stat: usageAt(&free), Now: time.Now, Home: t.TempDir(), Force: c.tier > 0}
			r := reclaimer{core: core, dir: dir, env: env, log: func(string, string) error { return nil }, measure: func(disk.Usage) ([]disk.Consumer, bool) { return nil, true }}
			if err := r.run(context.Background(), c.tier, &strings.Builder{}); err != nil {
				t.Fatal(err)
			}
			if got := ownerItems(t, core); len(got) != 0 {
				t.Fatalf("filed %+v", got)
			}
		})
	}
}

func TestADryRunChangesNoState(t *testing.T) {
	dir := t.TempDir()
	c := diskCore(t, dir)
	free := 0.04
	ran := false
	env := disk.Env{Dry: true, Paths: []string{"/x"}, Stat: usageAt(&free), Now: time.Now}
	r := reclaimer{core: c, dir: dir, env: env, log: func(string, string) error { t.Error("logged"); return nil },
		steps: []disk.Step{{Name: "s", Tier: 1, Run: func(context.Context) disk.Report { ran = true; return disk.Report{Did: "would"} }}}}
	if err := r.run(context.Background(), 0, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if !ran || len(ownerItems(t, c)) != 0 {
		t.Fatalf("ran %v items %+v", ran, ownerItems(t, c))
	}
	if _, err := os.Stat(diskStatePath(dir)); err == nil {
		t.Fatal("a dry run wrote state")
	}
}

func TestAScopedYMReclaimsOnlyInsideItsRehearsalRoot(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	os.WriteFile(filepath.Join(root, core.RehearsalMark), nil, 0o644)
	state := filepath.Join(root, "state")
	os.MkdirAll(state, 0o755)
	for _, c := range []struct {
		name       string
		everywhere bool
		state      string
		path       string
		want       bool
	}{
		{"owner's ym, anything", true, "", "/any", true},
		{"rehearsal, inside", false, state, filepath.Join(root, "cache"), true},
		{"rehearsal, outside", false, state, "/home/me/.cache/go-build", false},
		{"rehearsal, a like-named sibling", false, state, root + "-other/cache", false},
		{"rehearsal, no path to check", false, state, "", false},
		{"no rehearsal root at all", false, t.TempDir(), "/home/me/.cache", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			may := diskMay(c.everywhere, c.state)
			if got := may == nil || may(c.path); got != c.want {
				t.Fatalf("may(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

func TestALiveSessionKeepsItsWorktreeWhereverItsCwdMoved(t *testing.T) {
	for _, c := range []struct {
		name   string
		pane   bool
		landed bool
		want   string
	}{
		{"a live pane with its cwd elsewhere", true, false, launch.KeepInUse},
		{"no pane", false, false, ""},
		{"landed record with a pane of the same name", true, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
			git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
			repo := filepath.Join(base, "repo")
			git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
			git(t, repo, "push", "-q", "origin", "main")
			git(t, repo, "fetch", "-q", "origin")
			wt := launch.WorktreePath(repo, "feature")
			git(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
			old := time.Now().Add(-launch.Idle - time.Hour)
			admin := filepath.Join(repo, ".git", "worktrees", filepath.Base(wt))
			for _, p := range []string{wt, filepath.Join(admin, "HEAD"), filepath.Join(admin, "logs", "HEAD")} {
				os.Chtimes(p, old, old)
			}
			os.Remove(filepath.Join(admin, "logs", "HEAD"))
			state := t.TempDir()
			srv := &tmux.Host{Socket: tmuxtest.Socket(t), Kind: tmux.Server}
			l := launcher{srv: srv, records: launch.Records{Dir: filepath.Join(state, "launches")}, removals: filepath.Join(state, "gc.log")}
			rec := launch.Record{Key: "repo-wt-feature", Harness: "claude", Session: "s1", Started: old,
				Worktree: launch.Worktree{Path: wt, Branch: "feature", GitDir: filepath.Join(repo, ".git")}}
			if c.landed {
				rec.Landed = time.Now()
			}
			if err := l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			if c.pane {
				conf := filepath.Join(state, "tmux.conf")
				os.WriteFile(conf, []byte(tmux.Conf()), 0o644)
				if _, err := srv.Start(conf, tmux.SessionName(rec.Key), base, []string{"sleep", "600"}); err != nil {
					t.Fatal(err)
				}
			}
			all, _ := l.gcWith([]string{repo}, true, false, launch.Policy{Idle: time.Hour, Ignored: true})
			if len(all) != 1 || len(all[0].Trees) != 1 || all[0].Trees[0].Keep != c.want {
				t.Fatalf("decided %+v, want keep %q", all, c.want)
			}
		})
	}
}
