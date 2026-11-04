package disk

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func fixed(frees map[string]float64) func(string) (Usage, error) {
	devs := map[string]uint64{}
	for p := range frees {
		devs[p] = uint64(len(devs) + 1)
	}
	return func(path string) (Usage, error) {
		f, ok := frees[path]
		if !ok {
			return Usage{}, os.ErrNotExist
		}
		return Usage{Path: path, Dev: devs[path], Total: 1000, Avail: uint64(f * 1000)}, nil
	}
}

func TestTiersAreFractionsOfTheDisk(t *testing.T) {
	for _, c := range []struct {
		free float64
		want int
	}{{0.5, 0}, {0.20, 0}, {0.199, 1}, {0.10, 1}, {0.099, 2}, {0.05, 2}, {0.049, 3}, {0, 3}} {
		if got := TierOf(c.free); got != c.want {
			t.Errorf("TierOf(%v) = %d, want %d", c.free, got, c.want)
		}
	}
}

func TestLowestReadsEachFilesystemOnceAndSkipsWhatIsMissing(t *testing.T) {
	stat := fixed(map[string]float64{"/a": 0.5, "/b": 0.07})
	low, ok := Lowest(stat, []string{"/gone", "/a", "/b", "/a"})
	if !ok || low.Path != "/b" {
		t.Fatalf("Lowest = %+v %v", low, ok)
	}
	if _, ok := Lowest(stat, []string{"/gone"}); ok {
		t.Fatal("found a lowest among missing paths")
	}
	if got := Each(stat, []string{"/a", "/a", "/b"}); len(got) != 2 {
		t.Fatalf("Each = %+v", got)
	}
}

func TestADrillOverridesFreeSpaceOnlyUnderARehearsalRoot(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "free")
	os.WriteFile(file, []byte("0.04\n"), 0o644)
	t.Setenv(FreeFileEnv, file)
	for _, c := range []struct {
		name      string
		mark      bool
		wantFaked bool
	}{{"no mark", false, false}, {"marked", true, true}} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, "state")
			os.MkdirAll(state, 0o755)
			if c.mark {
				os.WriteFile(filepath.Join(root, rehearsalMark), nil, 0o644)
			}
			t.Setenv("YAMNAYA_STATE_DIR", state)
			u, err := Stat(base)
			if err != nil {
				t.Fatal(err)
			}
			if faked := u.Free() < 0.05 && u.Free() > 0.03; faked != c.wantFaked {
				t.Fatalf("free %v, faked %v, want %v", u.Free(), faked, c.wantFaked)
			}
			if _, held := Held(base); held != c.wantFaked && c.wantFaked {
				t.Fatal("a drill at 4% free does not hold new slots")
			}
		})
	}
}

func TestAPassStopsOnceEnoughIsFreeAndRunsOnlyItsTier(t *testing.T) {
	for _, c := range []struct {
		name     string
		tier     int
		enoughAt int
		want     []string
		complete bool
	}{
		{"tier 1 skips tier 2", 1, 99, []string{"a", "b"}, true},
		{"tier 2 runs everything", 2, 99, []string{"a", "b", "c"}, true},
		{"tier 3 is tier 2's steps", 3, 99, []string{"a", "b", "c"}, true},
		{"stops at the target", 2, 1, []string{"a"}, false},
		{"already enough", 2, 0, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var ran []string
			step := func(name string, tier int) Step {
				return Step{Name: name, Tier: tier, Run: func(context.Context) Report { ran = append(ran, name); return Report{} }}
			}
			complete := Run(context.Background(), []Step{step("a", 1), step("b", 1), step("c", 2)}, c.tier,
				func() bool { return len(ran) >= c.enoughAt }, func(Report) {})
			if !slices.Equal(ran, c.want) || complete != c.complete {
				t.Fatalf("ran %v complete %v, want %v %v", ran, complete, c.want, c.complete)
			}
		})
	}
}

// goCache is a Go build cache holding n entries of size bytes each, the i'th aged age(i).
func goCache(t *testing.T, n, size int, age func(int) time.Duration) (string, []string) {
	dir := t.TempDir()
	var files []string
	for i := range n {
		sub := filepath.Join(dir, fmt.Sprintf("%02x", i))
		os.MkdirAll(sub, 0o755)
		p := filepath.Join(sub, fmt.Sprintf("%064x-d", i))
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age(i))
		os.Chtimes(p, at, at)
		files = append(files, p)
	}
	os.WriteFile(filepath.Join(dir, "trim.txt"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(dir, "README"), []byte("1"), 0o644)
	return dir, files
}

func goEnvFor(dir string, frees map[string]float64) Env {
	return Env{Home: "/nowhere", Stat: fixed(frees), Now: time.Now, Force: true,
		Find: func(n string) string {
			if n == "go" {
				return "go"
			}
			return ""
		},
		Exec: func(_ context.Context, _ string, _ time.Duration, argv ...string) (string, error) {
			if argv[1] == "env" && argv[2] == "GOCACHE" {
				return dir, nil
			}
			return "", nil
		}}
}

func TestGoTrimRemovesOldestFirstAndSparesWhatARunningBuildMayHold(t *testing.T) {
	for _, c := range []struct {
		name  string
		floor int64
		dry   bool
		kept  func(i int) bool
	}{
		{"tier 1 trims all but the recent", 0, false, func(i int) bool { return i >= 4 }},
		{"a floor keeps the newest of the old", 4 << 10, false, func(i int) bool { return i >= 2 }},
		{"dry run changes nothing", 0, true, func(int) bool { return true }},
	} {
		t.Run(c.name, func(t *testing.T) {
			// entries 0..3 are old, oldest first; 4 and 5 are in reach of a running build
			dir, files := goCache(t, 6, 1<<10, func(i int) time.Duration {
				if i >= 4 {
					return time.Minute
				}
				return time.Duration(10-i) * time.Hour
			})
			e := goEnvFor(dir, map[string]float64{dir: 0.01})
			e.Dry = c.dry
			r := goTrim(context.Background(), e, c.floor)
			if r.Err != nil || r.Skipped != "" {
				t.Fatalf("report %+v", r)
			}
			for i, f := range files {
				_, err := os.Stat(f)
				if (err == nil) != c.kept(i) {
					t.Errorf("entry %d exists %v, want %v", i, err == nil, c.kept(i))
				}
			}
			for _, name := range []string{"trim.txt", "README"} {
				if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
					t.Errorf("%s removed", name)
				}
			}
		})
	}
}

func TestGoTrimStopsWhenTheDiskHasEnoughFree(t *testing.T) {
	dir, files := goCache(t, 4, enoughEvery, func(i int) time.Duration { return time.Duration(10-i) * time.Hour })
	calls := 0
	e := goEnvFor(dir, map[string]float64{dir: 0.01})
	e.Force, e.Paths = false, []string{dir}
	e.Stat = func(string) (Usage, error) {
		calls++
		avail := uint64(10)
		if calls > 1 {
			avail = 500
		}
		return Usage{Path: dir, Dev: 1, Total: 1000, Avail: avail}, nil
	}
	goTrim(context.Background(), e, 0)
	left := 0
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			left++
		}
	}
	if left != 3 {
		t.Fatalf("%d entries left, want the trim to stop after the first once free read enough", left)
	}
}

func TestCacheStepsLeaveAloneWhatTheyMayNotTouchOrWhatIsNotInstalled(t *testing.T) {
	missing := func(string) string { return "" }
	found := func(n string) string { return n }
	exec := func(_ context.Context, _ string, _ time.Duration, argv ...string) (string, error) {
		return "/real/cache", nil
	}
	refuse := func(string) bool { return false }
	stat := fixed(map[string]float64{"/real/cache": 0.01})
	for _, c := range []struct {
		name string
		run  func(context.Context, Env) Report
	}{
		{"go", func(ctx context.Context, e Env) Report { return goTrim(ctx, e, 0) }},
		{"uv", func(ctx context.Context, e Env) Report { return uvCache(ctx, e, "prune") }},
		{"pnpm", pnpmPrune},
		{"brew", brewCleanup},
		{"docker", dockerPrune},
	} {
		t.Run(c.name+"/not installed", func(t *testing.T) {
			if r := c.run(context.Background(), Env{Find: missing, Exec: exec, Stat: stat, Now: time.Now}); r.Skipped == "" {
				t.Fatalf("report %+v", r)
			}
		})
		t.Run(c.name+"/not this ym's", func(t *testing.T) {
			var ran []string
			e := Env{Find: found, Stat: stat, Now: time.Now, May: refuse, Exec: func(ctx context.Context, d string, l time.Duration, argv ...string) (string, error) {
				ran = append(ran, strings.Join(argv, " "))
				return exec(ctx, d, l, argv...)
			}}
			if r := c.run(context.Background(), e); r.Skipped == "" {
				t.Fatalf("report %+v", r)
			}
			if slices.ContainsFunc(ran, func(s string) bool {
				return strings.Contains(s, "prune") || strings.Contains(s, "cleanup") || strings.Contains(s, "clean")
			}) {
				t.Fatalf("ran %v", ran)
			}
		})
	}
}

func TestUvGivesUpForThisPassWhenAnotherUvHoldsItsCache(t *testing.T) {
	var ran []string
	e := Env{Force: true, Find: func(n string) string { return n }, Stat: fixed(nil), Exec: func(_ context.Context, _ string, _ time.Duration, argv ...string) (string, error) {
		ran = append(ran, strings.Join(argv, " "))
		if argv[2] == "dir" {
			return "/c", nil
		}
		return "error: Timeout (300s) when waiting for lock on `/c` at `/c/.lock`, is another uv process running?", errors.New("exit status 2")
	}}
	r := uvCache(context.Background(), e, "clean")
	if r.Err != nil || !r.Blocked || !strings.Contains(r.Skipped, "lock") {
		t.Fatalf("report %+v", r)
	}
	if !slices.Contains(ran, "uv cache clean") {
		t.Fatalf("ran %v", ran)
	}
}

func TestStaleBuildDirsGoOnlyWhenOldAndUnheld(t *testing.T) {
	old := time.Now().Add(-2 * staleAge)
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, dir string)
		held  func(dir string) []string
		kept  bool
	}{
		{"old, unheld", nil, nil, false},
		{"old, a process runs a binary from it", nil, func(d string) []string { return []string{d + "/b001/x.test"} }, true},
		{"old, the process has it as cwd", nil, func(d string) []string { return []string{d} }, true},
		{"a child changed recently", func(t *testing.T, d string) {
			os.Chtimes(filepath.Join(d, "b001"), time.Now(), time.Now())
		}, nil, true},
		{"another dir with a like name is held, not this", nil, func(d string) []string { return []string{d + "0"} }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("TMPDIR", root)
			dir := filepath.Join(root, "go-build123")
			os.MkdirAll(filepath.Join(dir, "b001"), 0o755)
			os.WriteFile(filepath.Join(dir, "b001", "x"), []byte("1"), 0o644)
			os.Chtimes(filepath.Join(dir, "b001"), old, old)
			os.Chtimes(dir, old, old)
			if c.setup != nil {
				c.setup(t, dir)
			}
			other := filepath.Join(root, "go-buildx")
			os.MkdirAll(other, 0o755)
			os.Chtimes(other, old, old)
			e := Env{Force: true, Now: time.Now, Stat: fixed(nil), Find: func(string) string { return "" },
				May:     func(p string) bool { return p == root },
				Holders: func() ([]string, error) { return nil, nil }}
			if c.held != nil {
				e.Holders = func() ([]string, error) { return c.held(dir), nil }
			}
			staleBuildDirs(context.Background(), e)
			if _, err := os.Stat(dir); (err == nil) != c.kept {
				t.Fatalf("exists %v, want %v", err == nil, c.kept)
			}
			if _, err := os.Stat(other); err != nil {
				t.Fatal("removed a dir not named as go names its build dirs")
			}
		})
	}
}

func TestDerivedDataGoesOldestFirstAndKeepsRecentProjects(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Developer", "Xcode", "DerivedData")
	project := func(name string, at time.Time) string {
		p := filepath.Join(dir, name)
		os.MkdirAll(p, 0o755)
		os.Chtimes(p, at, at)
		return p
	}
	old := project("old-abc", time.Now().Add(-2*staleAge))
	fresh := project("fresh-def", time.Now())
	r := derivedData(context.Background(), Env{Home: home, Force: true, Now: time.Now, Stat: fixed(nil)})
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old project kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh project removed")
	}
}

func TestMeasureIsBoundedAndGroupsByTwoLevels(t *testing.T) {
	root := t.TempDir()
	write := func(rel string, size int) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, make([]byte, size), 0o644)
	}
	write("big/one/a", 1<<20)
	write("big/one/deep/b", 1<<20)
	write("small/two/c", 1<<12)
	u, err := Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	out, complete := Measure(context.Background(), []string{root, filepath.Join(root, "big")}, u.Dev, 1000, 1)
	if !complete || len(out) != 1 || out[0].Path != filepath.Join(root, "big", "one") {
		t.Fatalf("Measure = %+v %v", out, complete)
	}
	if _, complete := Measure(context.Background(), []string{root}, u.Dev, 2, 5); complete {
		t.Fatal("a measurement past its budget says it finished")
	}
}

func TestPnpmPruneWaitsOutARunningPnpm(t *testing.T) {
	for _, running := range []bool{false, true} {
		var pruned bool
		e := Env{Force: true, Home: t.TempDir(), Stat: fixed(nil), Find: func(n string) string { return n },
			Running: func(string) bool { return running },
			Exec: func(_ context.Context, _ string, _ time.Duration, argv ...string) (string, error) {
				pruned = pruned || argv[2] == "prune"
				return "/store", nil
			}}
		if pnpmPrune(context.Background(), e); pruned == running {
			t.Errorf("running %v: pruned %v", running, pruned)
		}
	}
}

func TestWaitingOnUvHoldsUpNoOtherStepAndCleanReplacesPrune(t *testing.T) {
	e := Env{Force: true, Stat: fixed(nil), Find: func(string) string { return "" }, Now: time.Now, Home: t.TempDir(), May: func(string) bool { return false }}
	steps := append(CacheSteps(e), HardSteps(e)...)
	for _, c := range []struct {
		tier int
		want string
	}{{1, "uv prune"}, {2, "uv clean"}, {3, "uv clean"}} {
		var ran []string
		Run(context.Background(), steps, c.tier, func() bool { return false }, func(r Report) { ran = append(ran, r.Step) })
		if last := ran[len(ran)-1]; last != c.want {
			t.Errorf("tier %d ends with %q, want %q: %v", c.tier, last, c.want, ran)
		}
		if n := len(slices.DeleteFunc(slices.Clone(ran), func(s string) bool { return !strings.HasPrefix(s, "uv ") })); n != 1 {
			t.Errorf("tier %d runs %d uv steps: %v", c.tier, n, ran)
		}
	}
}

func TestAUvKilledWhileRemovingIsAFailureNotALockWait(t *testing.T) {
	e := Env{Force: true, Find: func(n string) string { return n }, Stat: fixed(nil), Exec: func(_ context.Context, _ string, _ time.Duration, argv ...string) (string, error) {
		if argv[2] == "dir" {
			return "/c", nil
		}
		return "Clearing cache at: /c", context.DeadlineExceeded
	}}
	if r := uvCache(context.Background(), e, "clean"); r.Err == nil || r.Blocked {
		t.Fatalf("report %+v", r)
	}
}

func TestTheFloorSaysWhenAgeHeldItBack(t *testing.T) {
	for _, c := range []struct {
		name    string
		age     time.Duration
		blocked bool
	}{{"all old", 3 * time.Hour, false}, {"under an hour and a bit, in reach of a running build", 90 * time.Minute, true}} {
		t.Run(c.name, func(t *testing.T) {
			dir, files := goCache(t, 3, 1<<10, func(int) time.Duration { return c.age })
			e := goEnvFor(dir, map[string]float64{dir: 0.01})
			r := goTrim(context.Background(), e, 1<<10)
			if r.Blocked != c.blocked {
				t.Fatalf("report %+v", r)
			}
			left := 0
			for _, f := range files {
				if _, err := os.Stat(f); err == nil {
					left++
				}
			}
			if want := map[bool]int{false: 1, true: 3}[c.blocked]; left != want {
				t.Fatalf("%d left, want %d", left, want)
			}
		})
	}
}

func TestStaleBuildDirsStayWhenTheProcessTableCannotBeRead(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	old := time.Now().Add(-2 * staleAge)
	dir := filepath.Join(root, "go-build123")
	os.MkdirAll(dir, 0o755)
	os.Chtimes(dir, old, old)
	e := Env{Force: true, Now: time.Now, Stat: fixed(nil), Find: func(string) string { return "" }, May: func(p string) bool { return p == root },
		Holders: func() ([]string, error) { return nil, os.ErrPermission }}
	if r := staleBuildDirs(context.Background(), e); !r.Blocked {
		t.Fatalf("report %+v", r)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("removed a dir with no way to know it is unheld")
	}
}

func TestWorktreesGoOnlyFromAFilesystemItselfUnderTier2(t *testing.T) {
	frees := map[string]float64{"/tmp": 0.08, "/home": 0.22, "/data": 0.09, "/fine": 0.5}
	stat := fixed(frees)
	for _, c := range []struct {
		path  string
		force bool
		want  bool
	}{
		{"/home", false, false}, {"/data", false, true}, {"/fine", false, false}, {"/gone", false, false}, {"/fine", true, true},
	} {
		e := Env{Stat: stat, Paths: []string{"/tmp", "/home"}, Force: c.force}
		if got := e.Reapable()(c.path); got != c.want {
			t.Errorf("Reapable(%s) force %v = %v, want %v", c.path, c.force, got, c.want)
		}
	}
}
