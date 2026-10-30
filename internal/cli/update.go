package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const (
	ymModule  = "github.com/project-kikkuli/yamnaya"
	ymPackage = ymModule + "/cmd/ym"
)

// goRunner runs the go tool with args, writing its output to out.
type goRunner func(out io.Writer, args ...string) error

func runGo(out io.Writer, args ...string) error {
	gobin, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("ym update needs the go toolchain on PATH: %w", err)
	}
	c := exec.Command(gobin, args...)
	c.Stdout, c.Stderr = out, out
	return c.Run()
}

// ymRepo is where ym update fetches ym's source; a variable so tests can serve their own.
var ymRepo = "https://" + ymModule + ".git"

// updateSource brings a clone of ymRepo under the cache directory to ref, origin's default branch when ref is empty, and
// returns its directory and the commit it checked out. go install ym@version cannot build ym: its go.mod replaces a module
// with a directory in the repository.
func updateSource(ref string) (string, string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", "", err
	}
	src := filepath.Join(cache, "yamnaya", "update")
	git := func(args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", src}, args...)...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := os.Stat(filepath.Join(src, ".git")); err != nil {
		if err := os.RemoveAll(src); err != nil {
			return "", "", err
		}
		if out, err := exec.Command("git", "clone", "-q", "--filter=blob:none", "--no-checkout", ymRepo, src).CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("git clone %s: %w: %s", ymRepo, err, strings.TrimSpace(string(out)))
		}
	} else if _, err := git("fetch", "-q", "--prune", ymRepo, "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return "", "", err
	}
	if _, err := git("remote", "set-head", "origin", "--auto"); err != nil {
		return "", "", err
	}
	target := "origin/HEAD"
	if ref != "" {
		target = ref
		if _, err := git("rev-parse", "--verify", "-q", "origin/"+ref+"^{commit}"); err == nil {
			target = "origin/" + ref
		}
	}
	sha, err := git("rev-parse", "--verify", target+"^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("%s is not a branch or commit of %s", ref, ymRepo)
	}
	if _, err := git("checkout", "-q", "--force", "--detach", sha); err != nil {
		return "", "", err
	}
	if _, err := git("clean", "-q", "-fdx"); err != nil {
		return "", "", err
	}
	return src, sha, nil
}

// restarter runs the ym at bin to bring the board's skill and statusline up to date and ym serve back up on it.
type restarter func(out io.Writer, bin string) error

// restartInto does on this host what ym remote install does on its host once the new ym is in place: ym install --statusline,
// then ym service keep, or ym service ensure where keep fails. A running ym serve and board notice their executable changed and
// exec the new one by themselves.
func restartInto(out io.Writer, bin string) error {
	ym := func(args ...string) error {
		c := exec.Command(bin, args...)
		c.Stdout, c.Stderr = out, out
		if err := c.Run(); err != nil {
			return fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), err)
		}
		return nil
	}
	if err := ym("install", "--statusline"); err != nil {
		return err
	}
	if err := ym("service", "keep"); err != nil {
		fmt.Fprintln(out, err)
		return ym("service", "ensure")
	}
	return nil
}

func updateCmd(run goRunner, self func() (string, error), restart restarter) *cobra.Command {
	return &cobra.Command{
		Use:   "update [branch-or-commit]",
		Short: "Build ym from its repository's default branch, or the given branch or commit, and replace the running ym with it; a running board and ym serve restart into it",
		Long: `Build ym from its repository's default branch, or the given branch or commit, and replace the ym that runs this command with it.

The new ym replaces the running executable, symlinks resolved, in place: it is written to <path>.new beside it and renamed over
it, and the ym it replaces stays as <path>.bak, one update deep; mv <path>.bak <path> goes back. When go install would put ym
elsewhere (GOBIN, or GOPATH/bin), that copy is replaced too, without a backup, so no older ym is left behind there. ym update
fails, changing nothing, when it cannot write the running executable's directory.

Then the new ym runs ym install --statusline and ym service keep (ym service ensure where keep fails), as ym remote install
does on a host; a running board and ym serve notice their executable changed and restart into the new one.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			exe, err := self()
			if err != nil {
				return fmt.Errorf("ym update cannot find the running ym to replace: %w", err)
			}
			target, err := filepath.EvalSymlinks(exe)
			if err != nil {
				return fmt.Errorf("ym update cannot resolve the running ym %s: %w", exe, err)
			}
			if err := writable(target); err != nil {
				return err
			}
			src, sha, err := updateSource(ref)
			if err != nil {
				return err
			}
			build, err := os.MkdirTemp("", "ym-update-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(build)
			bin := filepath.Join(build, "ym")
			if err := run(out, "-C", src, "build", "-o", bin, "./cmd/ym"); err != nil {
				return fmt.Errorf("go build ./cmd/ym at %s: %w", sha, err)
			}
			short := sha[:min(12, len(sha))]
			if err := replaceBinary(bin, target, true); err != nil {
				return err
			}
			fmt.Fprintf(out, "installed ym %s at %s (the ym it replaced is %s.bak)\n", short, target, target)
			var env bytes.Buffer
			if err := run(&env, "env", "GOBIN", "GOPATH"); err == nil {
				if gobin := installedPath(env.String()); !samePath(gobin, target) {
					if err := replaceBinary(bin, gobin, false); err != nil {
						fmt.Fprintf(out, "warning: %s still holds an older ym: %v\n", gobin, err)
					} else {
						fmt.Fprintf(out, "installed ym %s at %s\n", short, gobin)
					}
				}
			}
			if err := restart(out, target); err != nil {
				return fmt.Errorf("ym %s is installed at %s, but restarting into it failed: %w", short, target, err)
			}
			return nil
		},
	}
}

// writable fails, naming the path, unless ym update can put a new file beside target and rename it over target.
func writable(target string) error {
	probe := target + ".new"
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("ym update cannot replace the running ym %s: its directory %s is not writable: %w", target, filepath.Dir(target), err)
	}
	f.Close()
	return os.Remove(probe)
}

// replaceBinary copies bin to path.new beside path and renames it over path, so path is always a whole ym; with backup, the ym
// at path is first linked, or else copied, to path.bak. A missing path is simply created.
func replaceBinary(bin, path string, backup bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	next := path + ".new"
	if err := copyFile(bin, next); err != nil {
		os.Remove(next)
		return fmt.Errorf("cannot write %s: %w", next, err)
	}
	if backup {
		bak := path + ".bak"
		if err := os.Remove(bak); err != nil && !os.IsNotExist(err) {
			os.Remove(next)
			return fmt.Errorf("cannot replace the backup %s: %w", bak, err)
		}
		if _, err := os.Stat(path); err == nil {
			if err := os.Link(path, bak); err != nil {
				if err := copyFile(path, bak); err != nil {
					os.Remove(next)
					return fmt.Errorf("cannot back up %s to %s: %w", path, bak, err)
				}
			}
		}
	}
	if err := os.Rename(next, path); err != nil {
		os.Remove(next)
		return fmt.Errorf("cannot replace %s: %w", path, err)
	}
	return nil
}

// copyFile writes src's bytes to dst, mode 755, synced to disk.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, in); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o755); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// installedPath is where go install puts ym, given go env GOBIN GOPATH.
func installedPath(env string) string {
	lines := strings.Split(env, "\n")
	if gobin := strings.TrimSpace(lines[0]); gobin != "" {
		return filepath.Join(gobin, "ym")
	}
	gopath := ""
	if len(lines) > 1 {
		gopath = strings.TrimSpace(lines[1])
	}
	return filepath.Join(filepath.SplitList(gopath)[0], "bin", "ym")
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
