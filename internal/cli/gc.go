package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// sweepEvery is how often the board starts a background ym gc.
const sweepEvery = time.Hour

// sweepSoon starts ym gc in the background when the last one began over sweepEvery ago.
func (l launcher) sweepSoon() {
	if l.stamp == "" {
		return
	}
	if fi, err := os.Stat(l.stamp); err == nil && time.Since(fi.ModTime()) < sweepEvery {
		return
	}
	if os.WriteFile(l.stamp, nil, 0o644) == nil {
		detach("gc", "--quiet")
	}
}

// cwds are the working directories of every process.
func cwds() []string {
	procs, _ := proc.List()
	var out []string
	for _, p := range procs {
		if d := proc.Cwd(p.PID); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// gc sweeps every repository in repos, those of launch records and those with linked worktrees under ~/dev, dropping the
// records of worktrees it removes.
func (l launcher) gc(repos []string, dry, sizes bool) []launch.Swept {
	seen := map[string]bool{}
	if home, err := os.UserHomeDir(); err == nil && l.repos != "" {
		repos = append(repos, launch.Discovered(filepath.Join(home, "dev"), l.repos, time.Now())...)
	}
	recs, _ := l.records.All()
	for _, rec := range recs {
		if rec.Worktree.GitDir != "" {
			repos = append(repos, filepath.Dir(rec.Worktree.GitDir))
		}
	}
	dirs, now := cwds(), time.Now()
	var out []launch.Swept
	for _, root := range repos {
		if seen[root] {
			continue
		}
		seen[root] = true
		if _, err := os.Stat(root); err != nil {
			continue
		}
		s, err := launch.GC(root, now, dirs, l.squash, dry, sizes)
		if err != nil {
			s.Errors = append(s.Errors, err.Error())
		}
		if !dry {
			for _, t := range s.Trees {
				for _, rec := range recs {
					if t.Keep == "" && rec.Worktree.Path == t.Path {
						l.records.Delete(rec.Key)
					}
				}
			}
		}
		out = append(out, s)
	}
	return out
}

func gb(n int64) string { return fmt.Sprintf("%.2f GB", float64(n)/1e9) }

func printSwept(w io.Writer, all []launch.Swept, dry bool) {
	verb, del := "removed", "deleted"
	if dry {
		verb, del = "would remove", "would delete"
	}
	var removed, branches int
	var freed int64
	kept := map[string]int{}
	var held []launch.Sweep
	for _, s := range all {
		for _, t := range s.Trees {
			switch t.Keep {
			case "":
				removed++
				freed += t.Size
				how := ""
				if t.PR > 0 {
					how = fmt.Sprintf(", squash-merged in #%d", t.PR)
				}
				fmt.Fprintf(w, "%s %s (%s%s, %s)\n", verb, t.Path, t.Branch, how, gb(t.Size))
			case launch.KeepDirty, launch.KeepLocal:
				held = append(held, t)
			}
			if t.Keep != "" {
				kept[t.Keep]++
			}
		}
		for _, b := range s.Branches {
			branches++
			fmt.Fprintf(w, "%s branch %s in %s\n", del, b, s.Root)
		}
		for _, e := range s.Errors {
			fmt.Fprintf(w, "error in %s: %s\n", s.Root, e)
		}
	}
	if len(held) > 0 {
		fmt.Fprintln(w, "never touched:")
		for _, t := range held {
			fmt.Fprintf(w, "  %-18s %10s  %s (%s)\n", t.Keep, gb(t.Size), t.Path, t.Branch)
		}
	}
	fmt.Fprintf(w, "%s %d worktrees (%s) and %d merged branches across %d repositories; kept %d dirty, %d with local-only commits, %d active, %d in use, %d locked or missing, %d unreadable\n",
		verb, removed, gb(freed), branches, len(all), kept[launch.KeepDirty], kept[launch.KeepLocal], kept[launch.KeepActive],
		kept[launch.KeepInUse], kept[launch.KeepLocked], kept[launch.KeepFailing])
}

func gcCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	cmd := &cobra.Command{
		Use: "gc [--dry-run]",
		Short: ("Remove worktrees of known repositories and those under ~/dev that are clean, on a remote or squash-merged, " +
			"untouched for 3 days and unused, then delete merged or squash-merged branches that no worktree uses"),
		Args: cobra.NoArgs,
	}
	dry := cmd.Flags().Bool("dry-run", false, "print what gc would remove and delete, changing nothing")
	quiet := cmd.Flags().Bool("quiet", false, "print nothing and skip measuring sizes")
	cmd.RunE = with(func(e *env, cmd *cobra.Command, args []string) error {
		l, err := newLauncher()
		if err != nil {
			return err
		}
		repos, err := e.core.KnownRepos()
		if err != nil {
			return err
		}
		all := l.gc(repos, *dry, !*quiet)
		if !*quiet {
			printSwept(cmd.OutOrStdout(), all, *dry)
		}
		return nil
	})
	return cmd
}

// GC sweeps the repositories ym knows and says what it did.
func (l launcher) GC() (string, error) {
	repos, err := l.core.KnownRepos()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	printSwept(&b, l.gc(repos, false, true), false)
	return strings.TrimRight(b.String(), "\n"), nil
}
