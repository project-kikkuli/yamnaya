package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/disk"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

const (
	// diskCheckEvery is how often serve reads free space, which costs one statfs per filesystem.
	diskCheckEvery = time.Minute
	// diskRetryAfter is how long serve waits before another pass at the same tier: a pass that fell short walks whole caches.
	diskRetryAfter = 15 * time.Minute
	// sweepAfterStart is how long serve waits before its first hourly gc.
	sweepAfterStart = 10 * time.Minute
	// passLimit ends a pass whatever hangs in it, so disk.lock cannot be held for good.
	passLimit = 90 * time.Minute
	// reapIdle replaces gc's idle time at tier 2.
	reapIdle = time.Hour
	// measureBudget and measureLimit bound the one measurement of what still fills a disk.
	measureBudget = 5_000_000
	measureLimit  = 2 * time.Minute
)

// diskItemSession is the source of items ym files by itself.
const diskItemSession = "ym"

// diskState is what survives a serve restart: the last automatic pass, and the owner item of the current low-disk episode.
type diskState struct {
	LastPass time.Time `json:"last_pass,omitzero"`
	LastTier int       `json:"last_tier,omitempty"`
	Filed    string    `json:"filed,omitempty"`
	FiledAt  time.Time `json:"filed_at,omitzero"`
	// Blocked names what the last pass could not free because something running stopped it.
	Blocked []string `json:"blocked,omitempty"`
}

// endEpisode closes the episode's owner item if it is still open, so a relapse starts from none, and forgets it.
func (st *diskState) endEpisode(dir string, dismiss func(string) error) error {
	err := dismiss(st.Filed)
	st.Filed, st.FiledAt = "", time.Time{}
	return errors.Join(err, st.save(dir))
}

func diskDismiss(s *core.Store) func(string) error {
	return func(key string) error {
		it, err := s.Item(key)
		if err != nil || it.State != core.ItemOpen {
			return err
		}
		_, err = s.Transition(key, core.ItemDismissed, "free space recovered")
		return err
	}
}

func diskStatePath(dir string) string { return filepath.Join(dir, "disk.json") }

func readDiskState(dir string) diskState {
	var st diskState
	if raw, err := os.ReadFile(diskStatePath(dir)); err == nil {
		json.Unmarshal(raw, &st)
	}
	return st
}

func (st diskState) save(dir string) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := diskStatePath(dir) + ".new"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, diskStatePath(dir))
}

// lockDisk takes the lock one pass holds for its whole run; ok is false while another holds it.
func lockDisk(dir string) (unlock func(), ok bool, err error) {
	f, err := os.OpenFile(filepath.Join(dir, "disk.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false, nil
	}
	return func() { f.Close() }, true, nil
}

// diskPaths are every path whose filesystem serve and a pass watch: home, the temp dir, the Go cache and the repo roots.
func diskPaths(ctx context.Context) []string {
	paths := append(disk.Watched(), core.RepoRoots()...)
	if home, err := os.UserHomeDir(); err == nil {
		if bin := disk.Find(home, "go"); bin != "" {
			if out, err := disk.Command(ctx, "", time.Minute, bin, "env", "GOCACHE"); err == nil && out != "" && out != "off" {
				paths = append(paths, out)
			}
		}
	}
	return paths
}

// diskWatcher decides, once a minute, whether a pass should start.
type diskWatcher struct {
	dir   string
	paths []string
	stat  func(string) (disk.Usage, error)
	now   func() time.Time
	start func() error
	log   io.Writer
	// dismiss closes the ended episode's owner item.
	dismiss func(key string) error
	tier    int
	// started and startedTier repeat the persisted last pass in memory, for when a full disk stops it being saved.
	started     time.Time
	startedTier int
}

// tick reads free space and starts a pass when the disk is low and none ran lately at this tier or worse. It ends the
// current episode once free is back above disk.ClearFree. A running pass holds the lock, which leaves all of this to it.
func (w *diskWatcher) tick() error {
	low, ok := disk.Lowest(w.stat, w.paths)
	if !ok {
		return nil
	}
	tier := disk.TierOf(low.Free())
	if tier != w.tier {
		fmt.Fprintf(w.log, "disk: %s has %.1f%% free, tier %d\n", low.Path, low.Free()*100, tier)
		w.tier = tier
	}
	unlock, free, err := lockDisk(w.dir)
	if err != nil || !free {
		return err
	}
	st := readDiskState(w.dir)
	if st.Filed != "" && low.Free() > disk.ClearFree {
		err = st.endEpisode(w.dir, w.dismiss)
	}
	unlock()
	if err != nil {
		return err
	}
	recent := func(at time.Time, was int) bool { return tier <= was && w.now().Sub(at) < diskRetryAfter }
	if tier == 0 || recent(st.LastPass, st.LastTier) || recent(w.started, w.startedTier) {
		return nil
	}
	w.started, w.startedTier = w.now(), tier
	return w.start()
}

// watchDisk reads free space every minute and starts the ordinary hourly gc, so neither depends on a launch happening.
func watchDisk(ctx context.Context, l launcher, dir string, log io.Writer) {
	w := &diskWatcher{dir: dir, paths: diskPaths(ctx), stat: disk.Stat, now: time.Now, log: log, dismiss: diskDismiss(l.core.Store),
		start: func() error { return detach("disk", "reclaim", "--quiet") }}
	tick := time.NewTicker(diskCheckEvery)
	defer tick.Stop()
	began := time.Now()
	for {
		// a restart's resumes come first: an ordinary gc would land a session whose pane is not back yet
		if time.Since(began) >= sweepAfterStart {
			l.sweepSoon()
		}
		if err := w.tick(); err != nil && ctx.Err() == nil {
			fmt.Fprintln(log, "disk:", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// diskMay is what a ym on this state dir may reclaim: everything on the owner's own, and on any other only paths inside its
// rehearsal root, never a cache with no path.
func diskMay(everywhere bool, stateDir string) func(string) bool {
	if everywhere {
		return nil
	}
	root := core.RehearsalRoot(stateDir)
	return func(path string) bool {
		return root != "" && path != "" && (path == root || strings.HasPrefix(filepath.Clean(path), root+string(filepath.Separator)))
	}
}

// worktreeSteps are tier 2's worktree steps: gc at a short idle that spares ignored files outside the allowlist, then stripping
// the allowlisted ignored files out of the worktrees gc kept.
func (l launcher) worktreeSteps(repos []string, e disk.Env) []disk.Step {
	var all []launch.Swept
	var reapable func(string) bool
	return []disk.Step{
		{Name: "worktree gc", Tier: 2, Run: func(context.Context) disk.Report {
			reapable = e.Reapable()
			swept, pruned := l.gcWith(repos, e.Dry, e.Dry, launch.Policy{Idle: reapIdle, Ignored: true, Enough: func(p string) bool { return !reapable(p) }})
			all = swept
			var b strings.Builder
			printSwept(&b, swept, pruned, e.Dry)
			return disk.Report{Did: strings.TrimRight(b.String(), "\n"), Quiet: true}
		}},
		{Name: "strip ignored", Tier: 2, Run: func(ctx context.Context) disk.Report {
			var total int64
			worktrees, entries := 0, 0
			var errs []error
			for _, s := range all {
				for _, t := range s.Trees {
					if !launch.Strippable(t) || ctx.Err() != nil || !reapable(t.Path) {
						continue
					}
					if !e.Dry {
						now, err := cwdsNow()
						if err != nil {
							errs = append(errs, err)
							continue
						}
						if launch.Inside(t.Path, now) != "" {
							continue
						}
					}
					got := launch.StripIgnored(t, e.Dry)
					errs = append(errs, got.Err)
					if len(got.Entries) == 0 {
						continue
					}
					worktrees++
					total += got.Size
					for _, n := range got.Entries {
						entries += n
					}
					if !e.Dry {
						errs = append(errs, l.logRemoval("stripped ignored files", s.Root, t.Path, t.Branch, t.Head, got.Why()))
					}
				}
			}
			verb := "stripped"
			if e.Dry {
				verb = "would strip"
			}
			return disk.Report{Did: fmt.Sprintf("%s %d ignored entries from %d worktrees", verb, entries, worktrees), Freed: total, Err: errors.Join(errs...), Quiet: true}
		}},
	}
}

// reclaimer runs one pass and owns what it leaves behind: its log lines, the persisted state and the episode's owner item.
type reclaimer struct {
	core    *core.Core
	dir     string
	env     disk.Env
	steps   []disk.Step
	log     func(what, why string) error
	measure func(disk.Usage) ([]disk.Consumer, bool)
}

// run passes over tier, or with no tier the one free space calls for. A forced pass ignores the target and records no pass,
// so the next automatic one still happens on time.
func (r reclaimer) run(ctx context.Context, tier int, w io.Writer) error {
	low, ok := disk.Lowest(r.env.Stat, r.env.Paths)
	if !ok {
		return errors.New("no watched filesystem could be read")
	}
	forced := tier > 0
	if !forced {
		tier = disk.TierOf(low.Free())
	}
	if tier == 0 {
		fmt.Fprintf(w, "%s has %.1f%% free, nothing to do\n", low.Path, low.Free()*100)
		if st := readDiskState(r.dir); st.Filed != "" && low.Free() > disk.ClearFree {
			return st.endEpisode(r.dir, diskDismiss(r.core.Store))
		}
		return nil
	}
	fmt.Fprintf(w, "tier %d pass: %s has %.1f%% free\n", tier, low.Path, low.Free()*100)
	var errs []error
	var blocked []string
	complete := disk.Run(ctx, r.steps, tier, r.env.Enough, func(rep disk.Report) {
		switch {
		case rep.Err != nil:
			fmt.Fprintf(w, "%s: %v\n", rep.Step, rep.Err)
			blocked = append(blocked, rep.Step+": it failed")
			errs = append(errs, fmt.Errorf("%s: %w", rep.Step, rep.Err))
		case rep.Skipped != "":
			fmt.Fprintf(w, "%s: skipped, %s\n", rep.Step, rep.Skipped)
			if rep.Blocked {
				blocked = append(blocked, rep.Step+": "+rep.Skipped)
			}
		default:
			line := rep.Did
			if rep.Freed > 0 {
				line += " (" + disk.Bytes(rep.Freed) + ")"
			}
			fmt.Fprintf(w, "%s: %s\n", rep.Step, line)
			if rep.Blocked {
				blocked = append(blocked, rep.Step+": "+rep.Did)
			}
			if !r.env.Dry && !rep.Quiet && rep.Did != "" {
				errs = append(errs, r.log("reclaimed "+rep.Step, line))
			}
		}
	})
	if r.env.Dry {
		fmt.Fprintln(w, "dry run: nothing changed; a real pass stops as soon as free reaches 25%")
		return nil
	}
	st := readDiskState(r.dir)
	st.Blocked = blocked
	if !forced {
		st.LastPass, st.LastTier = r.env.Now(), tier
	}
	after, ok := disk.Lowest(r.env.Stat, r.env.Paths)
	if ok {
		fmt.Fprintf(w, "%s has %.1f%% free after the pass\n", after.Path, after.Free()*100)
		if st.Filed != "" && after.Free() > disk.ClearFree {
			errs = append(errs, st.endEpisode(r.dir, diskDismiss(r.core.Store)))
		}
		if complete && tier >= 2 && after.Free() < disk.Tier2Free && st.Filed == "" {
			added, err := r.fileOwnerItem(ctx, after, blocked)
			if err != nil {
				errs = append(errs, fmt.Errorf("owner item: %w", err))
			} else {
				st.Filed, st.FiledAt = added.Key(), r.env.Now()
				fmt.Fprintln(w, "filed", added.ID)
			}
		}
	}
	return errors.Join(append(errs, st.save(r.dir))...)
}

func (r reclaimer) fileOwnerItem(ctx context.Context, low disk.Usage, blocked []string) (core.Item, error) {
	consumers, complete := r.measure(low)
	return r.core.AddOwnerItem(diskItem(r.core.SelfName(), low, consumers, complete, blocked, filepath.Join(r.dir, "gc.log")))
}

// diskItem is the one owner item of a low-disk episode: what is low, and what still fills it.
func diskItem(host string, low disk.Usage, consumers []disk.Consumer, complete bool, blocked []string, log string) core.Item {
	names := make([]string, len(consumers))
	for i, c := range consumers {
		names[i] = c.String()
	}
	largest := "Largest remaining: " + strings.Join(names, ", ")
	if !complete {
		largest = "Largest remaining from a partial measurement: " + strings.Join(names, ", ")
	}
	if len(consumers) == 0 {
		largest = "Nothing measured"
	}
	if len(blocked) > 0 {
		largest += ". Not freed, ym retries each pass: " + strings.Join(blocked, "; ")
	}
	outcome := "ym cannot free more by itself"
	if len(blocked) > 0 {
		outcome = "ym could not run every step"
	}
	return core.Item{Kind: core.KindChore, Session: diskItemSession,
		Question: fmt.Sprintf("Disk on %s has %.1f%% free on %s after a reclaim pass; %s", host, low.Free()*100, low.Path, outcome),
		Rec:      fmt.Sprintf("Remove what you no longer need. %s. ym keeps worktrees holding unsaved work; new ym slot admissions wait below %.0f%% free.", largest, disk.Tier3Free*100),
		Evidence: []string{log},
		Tried:    "ym disk reclaim: trimmed regenerable caches, reaped worktrees idle over an hour, stripped ignored regenerable dirs from idle worktrees"}
}

// printDisk shows each filesystem and the episode. What the last pass could not free matters only while a filesystem is still low.
func printDisk(w io.Writer, usages []disk.Usage, st diskState) {
	low := false
	for _, u := range usages {
		fmt.Fprintf(w, "%s  %.1f%% free  %s of %s  tier %d\n", u.Path, u.Free()*100, disk.Bytes(int64(u.Avail)), disk.Bytes(int64(u.Total)), disk.TierOf(u.Free()))
		low = low || disk.TierOf(u.Free()) > 0
	}
	if !st.LastPass.IsZero() {
		fmt.Fprintf(w, "last automatic pass: tier %d, %s\n", st.LastTier, st.LastPass.Format(time.RFC3339))
	}
	for _, b := range st.Blocked {
		if low {
			fmt.Fprintln(w, "last pass could not free", b)
		}
	}
	if st.Filed != "" {
		fmt.Fprintf(w, "this episode's owner item: %s, filed %s\n", st.Filed, st.FiledAt.Format(time.RFC3339))
	}
}

func diskCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	cmd := &cobra.Command{Use: "disk", Short: "Show free space on the filesystems ym watches, the tier each is in and the current low-disk episode", Args: cobra.NoArgs}
	cmd.RunE = with(func(e *env, cmd *cobra.Command, args []string) error {
		printDisk(cmd.OutOrStdout(), disk.Each(disk.Stat, diskPaths(cmd.Context())), readDiskState(e.core.StateDir))
		return nil
	})
	reclaim := &cobra.Command{
		Use:   "reclaim [--tier N] [--dry-run]",
		Short: "Run one reclaim pass now, at low priority: free space picks the tier, or --tier forces one and ignores the 25% target",
		Long: "A pass takes tier 1's regenerable caches, then at tier 2 and below reaps worktrees idle over an hour, strips git-ignored regenerable dirs from " +
			"kept worktrees idle over 6 hours, cleans uv's cache and trims Go's to a small floor. Commits, dirty files and files outside the allowlist are never touched. " +
			"ym serve runs this by itself. One pass runs at a time. --dry-run changes nothing and prints what each step would do.",
		Args: cobra.NoArgs,
	}
	tier := reclaim.Flags().Int("tier", 0, "force this tier (1, 2 or 3) whatever free space says")
	dry := reclaim.Flags().Bool("dry-run", false, "print what the pass would do, changing nothing")
	quiet := reclaim.Flags().Bool("quiet", false, "print nothing")
	reclaim.RunE = with(func(e *env, cmd *cobra.Command, args []string) error {
		if *tier < 0 || *tier > 3 {
			return fmt.Errorf("--tier must be 1, 2 or 3")
		}
		disk.Lower()
		launch.GitLimit = 5 * time.Minute
		time.AfterFunc(passLimit, func() { os.Exit(1) })
		w := cmd.OutOrStdout()
		if *quiet {
			w = io.Discard
		}
		l, err := newLauncher(e.core.StateDir)
		if err != nil {
			return err
		}
		repos, err := e.core.KnownRepos()
		if err != nil {
			return err
		}
		unlock, free, err := lockDisk(e.core.StateDir)
		if err != nil {
			return err
		}
		if !free {
			fmt.Fprintln(w, "another pass is running")
			return nil
		}
		defer unlock()
		home, _ := os.UserHomeDir()
		ctx := cmd.Context()
		paths := diskPaths(ctx)
		env := disk.Env{Dry: *dry, Force: *tier > 0, Home: home, Paths: paths, Dirs: repos, May: diskMay(l.everywhere, e.core.StateDir)}.Machine()
		steps := append(disk.CacheSteps(env), l.worktreeSteps(repos, env)...)
		steps = append(steps, disk.HardSteps(env)...)
		r := reclaimer{core: e.core, dir: e.core.StateDir, env: env, steps: steps,
			log: func(what, why string) error { return l.logRemoval(what, "", "", "", "", why) },
			measure: func(low disk.Usage) ([]disk.Consumer, bool) {
				bounded, cancel := context.WithTimeout(ctx, measureLimit)
				defer cancel()
				roots := paths
				if env.May != nil {
					roots = slices.DeleteFunc(slices.Clone(paths), func(p string) bool { return !env.May(p) })
				}
				return disk.Measure(bounded, roots, low.Dev, measureBudget, 6)
			}}
		return r.run(ctx, *tier, w)
	})
	cmd.AddCommand(reclaim)
	return cmd
}
