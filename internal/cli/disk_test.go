package cli

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
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
	if it.Kind != core.KindChore || it.Recipient != core.Owner || !strings.Contains(it.Rec, "/home/me/dev 137.0 GiB") || !strings.Contains(it.Rec, "builds may fail") {
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
	w := &diskWatcher{dir: dir, paths: []string{"/x"}, stat: usageAt(&free), now: time.Now, log: &strings.Builder{}, start: func() error { return nil }, dismiss: diskDismiss(c.Store)}
	if err := w.tick(); err != nil {
		t.Fatal(err)
	}
	if readDiskState(dir).Filed == "" {
		t.Fatal("15% free ended the episode; only above 20% does")
	}
	open := func() int {
		n := 0
		for _, it := range ownerItems(t, c) {
			if it.State == core.ItemOpen {
				n++
			}
		}
		return n
	}
	if open() != 1 {
		t.Fatalf("%d open during the episode", open())
	}
	free = 0.21
	if err := w.tick(); err != nil {
		t.Fatal(err)
	}
	if readDiskState(dir).Filed != "" {
		t.Fatal("21% free did not end the episode")
	}
	if open() != 0 {
		t.Fatal("the item outlived the episode")
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
		{"no pane yet, as right after a restart", false, false, launch.KeepRecord},
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

func TestAReapLeavesAWorktreeOnAFilesystemWithEnoughFree(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
	git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
	repo := filepath.Join(base, "repo")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
	git(t, repo, "push", "-q", "origin", "main")
	git(t, repo, "fetch", "-q", "origin")
	wt := launch.WorktreePath(repo, "feature")
	git(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
	old := time.Now().Add(-2 * launch.StripIdle)
	admin := filepath.Join(repo, ".git", "worktrees", filepath.Base(wt))
	os.Remove(filepath.Join(admin, "logs", "HEAD"))
	for _, p := range []string{wt, filepath.Join(admin, "HEAD")} {
		os.Chtimes(p, old, old)
	}
	state := t.TempDir()
	for _, c := range []struct {
		name string
		free float64
		kept bool
	}{{"healthy filesystem", 0.9, true}, {"above tier 2 itself while another is under it", 0.22, true}, {"low filesystem", 0.05, false}} {
		t.Run(c.name, func(t *testing.T) {
			l := launcher{srv: &tmux.Host{Socket: "ymtest-unused", Kind: tmux.Server}, records: launch.Records{Dir: filepath.Join(state, "launches")}, everywhere: true}
			low := 0.05
			e := disk.Env{Paths: []string{"/small"}, Now: time.Now, Stat: func(p string) (disk.Usage, error) {
				if p == wt {
					u, err := usageAt(&c.free)(p)
					u.Dev = 2
					return u, err
				}
				return usageAt(&low)(p)
			}}
			l.worktreeSteps([]string{repo}, e)[0].Run(context.Background())
			if _, err := os.Stat(wt); (err == nil) != c.kept {
				t.Fatalf("on disk %v, want %v", err == nil, c.kept)
			}
		})
	}
}

func TestStrippingLeavesAWorktreeOnAFilesystemWithEnoughFree(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	git(t, base, "init", "-q", "-b", "main", "repo")
	repo := filepath.Join(base, "repo")
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules\n"), 0o644)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "root")
	wt := launch.WorktreePath(repo, "feature")
	git(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
	git(t, wt, "commit", "-q", "--allow-empty", "-m", "local only")
	dep := filepath.Join(wt, "node_modules", "a.js")
	os.MkdirAll(filepath.Dir(dep), 0o755)
	os.WriteFile(dep, []byte("x"), 0o644)
	old := time.Now().Add(-2 * launch.StripIdle)
	admin := filepath.Join(repo, ".git", "worktrees", filepath.Base(wt))
	os.Remove(filepath.Join(admin, "logs", "HEAD"))
	for _, p := range []string{wt, filepath.Join(admin, "HEAD")} {
		os.Chtimes(p, old, old)
	}
	state := t.TempDir()
	for _, c := range []struct {
		name string
		free float64
		kept bool
	}{{"healthy filesystem", 0.9, true}, {"above tier 2 itself while another is under it", 0.22, true}, {"low filesystem", 0.05, false}} {
		t.Run(c.name, func(t *testing.T) {
			l := launcher{srv: &tmux.Host{Socket: "ymtest-unused", Kind: tmux.Server}, records: launch.Records{Dir: filepath.Join(state, "launches")}, everywhere: true}
			low := 0.05
			e := disk.Env{Paths: []string{"/small"}, Now: time.Now, Stat: func(p string) (disk.Usage, error) {
				if p == wt {
					u, err := usageAt(&c.free)(p)
					u.Dev = 2
					return u, err
				}
				return usageAt(&low)(p)
			}}
			steps := l.worktreeSteps([]string{repo}, e)
			steps[0].Run(context.Background())
			if r := steps[1].Run(context.Background()); r.Err != nil {
				t.Fatal(r.Err)
			}
			if _, err := os.Stat(dep); (err == nil) != c.kept {
				t.Fatalf("on disk %v, want %v", err == nil, c.kept)
			}
		})
	}
}

func TestGCRemovesNothingWithoutTheProcessTable(t *testing.T) {
	old := listProcs
	listProcs = func() ([]proc.Proc, error) { return nil, os.ErrPermission }
	defer func() { listProcs = old }()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
	git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
	repo := filepath.Join(base, "repo")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
	git(t, repo, "push", "-q", "origin", "main")
	git(t, repo, "fetch", "-q", "origin")
	wt := launch.WorktreePath(repo, "feature")
	git(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
	state := t.TempDir()
	l := launcher{srv: &tmux.Host{Socket: "ymtest-unused", Kind: tmux.Server}, records: launch.Records{Dir: filepath.Join(state, "launches")}, everywhere: true}
	all, _ := l.gcWith([]string{repo}, false, false, launch.Policy{Idle: time.Nanosecond, Ignored: true})
	if len(all) != 1 || len(all[0].Errors) != 1 || len(all[0].Trees) != 0 {
		t.Fatalf("swept %+v", all)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatal("removed a worktree with no way to know it is unused")
	}
}

func TestACacheAPassCouldNotFreeShowsInTheStateAndTheOwnerItem(t *testing.T) {
	dir := t.TempDir()
	c := diskCore(t, dir)
	free := 0.08
	blocked := true
	env := disk.Env{Paths: []string{"/x"}, Stat: usageAt(&free), Now: time.Now}
	step := disk.Step{Name: "uv clean", Tier: 1, Run: func(context.Context) disk.Report {
		if !blocked {
			return disk.Report{Did: "cleaned"}
		}
		return disk.Report{Skipped: "another uv held the cache lock throughout", Blocked: true}
	}}
	r := reclaimer{core: c, dir: dir, env: env, steps: []disk.Step{step}, log: func(string, string) error { return nil },
		measure: func(disk.Usage) ([]disk.Consumer, bool) { return nil, true }}
	if err := r.run(context.Background(), 0, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	st := readDiskState(dir)
	if len(st.Blocked) != 1 || !strings.Contains(st.Blocked[0], "uv clean") {
		t.Fatalf("state %+v", st)
	}
	items := ownerItems(t, c)
	if len(items) != 1 {
		t.Fatalf("items %+v", items)
	}
	it, err := c.Store.Item(items[0].Key())
	if err != nil || !strings.Contains(it.Rec, "uv clean") {
		t.Fatalf("item %+v %v", it, err)
	}
	blocked = false
	if err := r.run(context.Background(), 0, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if st := readDiskState(dir); len(st.Blocked) != 0 {
		t.Fatalf("a pass that got through left %+v", st.Blocked)
	}
}

func TestTheWatcherRemembersItsLastStartWhenTheStateCannotBeSaved(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(diskStatePath(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	free := 0.15
	started := 0
	w := &diskWatcher{dir: dir, paths: []string{"/x"}, stat: usageAt(&free), now: func() time.Time { return now }, log: &strings.Builder{},
		start: func() error { started++; return nil }}
	for range 3 {
		if err := w.tick(); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	if started != 1 {
		t.Fatalf("started %d passes in 3 minutes", started)
	}
	now = now.Add(diskRetryAfter)
	if err := w.tick(); err != nil || started != 2 {
		t.Fatalf("started %d, %v", started, err)
	}
}

func TestYmDiskNamesWhatAPassCouldNotFreeOnlyWhileTheDiskIsLow(t *testing.T) {
	st := diskState{Blocked: []string{"uv clean: another uv held the cache lock throughout"}}
	for _, c := range []struct {
		name string
		free uint64
		want bool
	}{{"low", 150, true}, {"exactly at the tier", 200, false}, {"recovered", 400, false}} {
		t.Run(c.name, func(t *testing.T) {
			var b strings.Builder
			printDisk(&b, []disk.Usage{{Path: "/x", Total: 1000, Avail: c.free}}, st)
			if got := strings.Contains(b.String(), "could not free uv clean"); got != c.want {
				t.Fatalf("printed %q, want the line %v", b.String(), c.want)
			}
		})
	}
}

func TestYmDiskSaysWhatTheLastPassRiskedOnlyWhileTheDiskIsLow(t *testing.T) {
	dir := t.TempDir()
	c := diskCore(t, dir)
	free := 0.04
	env := disk.Env{Paths: []string{"/x"}, Stat: usageAt(&free), Now: time.Now}
	step := disk.Step{Name: "go", Tier: 3, Run: func(context.Context) disk.Report {
		free = 0.15
		return disk.Report{Did: "trimmed", Risky: "builds may have failed"}
	}}
	r := reclaimer{core: c, dir: dir, env: env, steps: []disk.Step{step}, log: func(string, string) error { return nil }}
	if err := r.run(context.Background(), 0, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if got := ownerItems(t, c); len(got) != 0 {
		t.Fatalf("recovered enough yet filed %+v", got)
	}
	for _, x := range []struct {
		free uint64
		want bool
	}{{150, true}, {400, false}} {
		var b strings.Builder
		printDisk(&b, []disk.Usage{{Path: "/x", Total: 1000, Avail: x.free}}, readDiskState(dir))
		if got := strings.Contains(b.String(), "builds may have failed"); got != x.want {
			t.Errorf("free %d printed %q, want %v", x.free, b.String(), x.want)
		}
	}
}

func TestOnlyTheOwnersOwnStateDirWatchesTheDiskUnlessADrillOptsIn(t *testing.T) {
	u, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Skip("no passwd entry here: ", err)
	}
	real := filepath.Join(u.HomeDir, ".local", "state", "yamnaya")
	// t.TempDir lies under the test run's own rehearsal root, so what must be outside one goes elsewhere
	outside, err := os.MkdirTemp("/tmp", "ym-disk-watch")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outside)
	fake, _ := filepath.EvalSymlinks(outside)
	marked := filepath.Join(fake, "drill")
	os.MkdirAll(filepath.Join(marked, "state"), 0o755)
	os.WriteFile(filepath.Join(marked, core.RehearsalMark), nil, 0o644)
	for _, c := range []struct {
		name  string
		home  string
		xdg   string
		dir   string
		optIn bool
		want  bool
	}{
		{"the real state dir", "", "", real, false, true},
		{"the real state dir with a private XDG_STATE_HOME", "", fake, real, false, true},
		{"a private state dir with a private XDG_STATE_HOME", "", fake, filepath.Join(fake, "state"), false, false},
		{"a default-looking state dir under an overridden HOME", fake, "", filepath.Join(fake, ".local", "state", "yamnaya"), false, false},
		{"a rehearsal without the opt-in", "", "", filepath.Join(marked, "state"), false, false},
		{"a rehearsal with the opt-in", "", "", filepath.Join(marked, "state"), true, true},
		{"the opt-in outside a rehearsal", "", "", filepath.Join(fake, "state"), true, false},
		{"the opt-in under an overridden HOME outside a rehearsal", fake, "", filepath.Join(fake, ".local", "state", "yamnaya"), true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.home != "" {
				t.Setenv("HOME", c.home)
			}
			if c.xdg != "" {
				t.Setenv("XDG_STATE_HOME", c.xdg)
			}
			t.Setenv(core.DiskWatchEnv, map[bool]string{true: "1"}[c.optIn])
			if got := core.WatchesDisk(c.dir); got != c.want {
				t.Fatalf("WatchesDisk(%s) = %v, want %v", c.dir, got, c.want)
			}
		})
	}
}

func TestAYmWhoseHomeIsOverriddenReclaimsNothingReal(t *testing.T) {
	fake, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", fake)
	state := filepath.Join(fake, ".local", "state", "yamnaya")
	os.MkdirAll(state, 0o755)
	if !core.DefaultStateDir(state) {
		t.Fatal("the fixture should look like the default state dir")
	}
	l, err := newLauncher(state)
	if err != nil {
		t.Fatal(err)
	}
	if l.everywhere {
		t.Fatal("swept every repository")
	}
	may := diskMay(l.everywhere, state)
	if may == nil || may("") || may("/home/someone/.cache/go-build") {
		t.Fatal("may reach a real cache")
	}
}

func TestAWatcherThatCannotStartTellsTheOwnerOnce(t *testing.T) {
	dir := t.TempDir()
	var filed []string
	file := func(why string) error { filed = append(filed, why); return nil }
	var log strings.Builder
	for _, why := range []string{"no such user", "no such user", "no such user"} {
		if err := noteWatchOff(dir, why, &log, file); err != nil {
			t.Fatal(err)
		}
	}
	if len(filed) != 1 || strings.Count(log.String(), "the watcher is off") != 1 {
		t.Fatalf("filed %v, log %q", filed, log.String())
	}
	if err := noteWatchOff(dir, "", &log, file); err != nil {
		t.Fatal(err)
	}
	if err := noteWatchOff(dir, "no such user", &log, file); err != nil {
		t.Fatal(err)
	}
	if len(filed) != 2 {
		t.Fatalf("a fresh failure after recovery filed %v", filed)
	}
}

func TestGCForceRemovesProvenSubmodulesAndSaysSo(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
	git(t, base, "init", "-q", "--bare", "-b", "main", "dep.git")
	git(t, base, "clone", "-q", filepath.Join(base, "dep.git"), "dep")
	git(t, filepath.Join(base, "dep"), "commit", "-q", "--allow-empty", "-m", "first")
	git(t, filepath.Join(base, "dep"), "push", "-q", "origin", "HEAD:main")
	git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
	repo := filepath.Join(base, "repo")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
	git(t, repo, "push", "-q", "origin", "main")
	wt := launch.WorktreePath(repo, "feature")
	git(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
	git(t, wt, "-c", "protocol.file.allow=always", "submodule", "add", "-q", filepath.Join(base, "dep.git"), "dep")
	git(t, wt, "commit", "-q", "-m", "add dep")
	git(t, wt, "push", "-q", "origin", "HEAD")
	old := time.Now().Add(-launch.Idle - time.Hour)
	admin := filepath.Join(repo, ".git", "worktrees", filepath.Base(wt))
	os.Remove(filepath.Join(admin, "logs", "HEAD"))
	for _, p := range []string{wt, filepath.Join(admin, "HEAD")} {
		os.Chtimes(p, old, old)
	}
	state := t.TempDir()
	l := launcher{srv: &tmux.Host{Socket: "ymtest-unused", Kind: tmux.Server}, records: launch.Records{Dir: filepath.Join(state, "launches")},
		removals: filepath.Join(state, "gc.log"), everywhere: true}
	all, pruned := l.gc([]string{repo}, true, false)
	var out strings.Builder
	printSwept(&out, all, pruned, true)
	if !strings.Contains(out.String(), "submodules proven") {
		t.Fatalf("dry run said %q", out.String())
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatal("a dry run removed it")
	}
	l.gc([]string{repo}, false, false)
	if _, err := os.Stat(wt); err == nil {
		t.Fatal("kept a worktree whose submodules lose nothing")
	}
	logged, _ := os.ReadFile(l.removals)
	if !strings.Contains(string(logged), "force-removed worktree") {
		t.Fatalf("gc.log %q", logged)
	}
}

func TestAYmOffTheOwnersStateTouchesOnlyWorktreesInsideItsRehearsalRoot(t *testing.T) {
	outside, err := os.MkdirTemp("/tmp", "ym-gc-scope")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outside)
	outside, _ = filepath.EvalSymlinks(outside)
	worktree := func(t *testing.T, base string) (repo, wt string) {
		t.Helper()
		os.MkdirAll(base, 0o755)
		git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
		git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
		repo = filepath.Join(base, "repo")
		git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
		git(t, repo, "push", "-q", "origin", "main")
		git(t, repo, "fetch", "-q", "origin")
		wt = launch.WorktreePath(repo, "feature")
		git(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
		old := time.Now().Add(-launch.Idle - time.Hour)
		admin := filepath.Join(repo, ".git", "worktrees", filepath.Base(wt))
		os.Remove(filepath.Join(admin, "logs", "HEAD"))
		for _, p := range []string{wt, filepath.Join(admin, "HEAD")} {
			os.Chtimes(p, old, old)
		}
		return repo, wt
	}
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, root string) (state, repo, wt, recPath, recGit string)
		gone  bool
	}{
		{"a record inside the root", func(t *testing.T, root string) (string, string, string, string, string) {
			repo, wt := worktree(t, filepath.Join(root, "r"))
			return filepath.Join(root, "state"), repo, wt, wt, filepath.Join(repo, ".git")
		}, true},
		{"a record outside the root", func(t *testing.T, root string) (string, string, string, string, string) {
			repo, wt := worktree(t, filepath.Join(outside, "decoy1"))
			return filepath.Join(root, "state"), repo, wt, wt, filepath.Join(repo, ".git")
		}, false},
		{"a symlink in the root pointing out of it", func(t *testing.T, root string) (string, string, string, string, string) {
			repo, wt := worktree(t, filepath.Join(outside, "decoy2"))
			os.Symlink(repo, filepath.Join(root, "repo-link"))
			os.Symlink(wt, filepath.Join(root, "wt-link"))
			return filepath.Join(root, "state"), repo, wt, filepath.Join(root, "wt-link"), filepath.Join(root, "repo-link", ".git")
		}, false},
		{"no rehearsal root at all", func(t *testing.T, root string) (string, string, string, string, string) {
			repo, wt := worktree(t, filepath.Join(outside, "decoy3"))
			return filepath.Join(outside, "state3"), repo, wt, wt, filepath.Join(repo, ".git")
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			os.WriteFile(filepath.Join(root, core.RehearsalMark), nil, 0o644)
			state, repo, wt, recPath, recGit := c.setup(t, root)
			os.MkdirAll(state, 0o755)
			l := launcher{srv: &tmux.Host{Socket: "ymtest-unused", Kind: tmux.Server}, records: launch.Records{Dir: filepath.Join(state, "launches")},
				removals: filepath.Join(state, "gc.log")}
			if err := l.records.Save(launch.Record{Key: "k", Harness: "claude", Session: "s", Started: time.Now(), Landed: time.Now(),
				Worktree: launch.Worktree{Path: recPath, Branch: "feature", GitDir: recGit}}); err != nil {
				t.Fatal(err)
			}
			l.gcWith(nil, false, false, launch.Policy{Idle: time.Hour, Ignored: true})
			if _, err := os.Stat(wt); (err != nil) != c.gone {
				t.Fatalf("worktree gone %v, want %v", err != nil, c.gone)
			}
			hasBranch := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/feature").Run() == nil
			if hasBranch == c.gone {
				t.Fatalf("branch kept %v, want gone %v", hasBranch, c.gone)
			}
		})
	}
}

func TestTheWatcherOffItemIsReadyForTheOwner(t *testing.T) {
	var args []string
	old := detach
	detach = func(a ...string) error { args = a; return nil }
	defer func() { detach = old }()
	if err := fileWatchOff("no such user"); err != nil {
		t.Fatal(err)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("YAMNAYA_STATE_DIR", dir)
	root := NewRoot()
	root.SetArgs(args)
	var out strings.Builder
	root.SetOut(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("ym %v: %v", args, err)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("printed no item id")
	}
}

func TestNoBackgroundGCStartsSoonAfterThisYmStarts(t *testing.T) {
	for _, c := range []struct {
		name  string
		up    time.Duration
		stale bool
		want  bool
	}{
		{"just started, last gc long ago", time.Minute, true, false},
		{"up a while, last gc long ago", sweepAfterStart + time.Minute, true, true},
		{"up a while, gc just ran", sweepAfterStart + time.Minute, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			oldStart, oldDetach := processStart, detach
			defer func() { processStart, detach = oldStart, oldDetach }()
			processStart = time.Now().Add(-c.up)
			started := false
			detach = func(...string) error { started = true; return nil }
			stamp := filepath.Join(t.TempDir(), "gc.stamp")
			os.WriteFile(stamp, nil, 0o644)
			if c.stale {
				old := time.Now().Add(-2 * sweepEvery)
				os.Chtimes(stamp, old, old)
			}
			launcher{stamp: stamp}.sweepSoon()
			if started != c.want {
				t.Fatalf("started %v, want %v", started, c.want)
			}
		})
	}
}
