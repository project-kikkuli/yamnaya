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

func updateCmd(run goRunner) *cobra.Command {
	return &cobra.Command{
		Use:   "update [branch-or-commit]",
		Short: "Build and install ym from its repository's default branch, or the given branch or commit; a running board restarts into it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			src, sha, err := updateSource(ref)
			if err != nil {
				return err
			}
			if err := run(out, "-C", src, "install", "./cmd/ym"); err != nil {
				return fmt.Errorf("go install ./cmd/ym at %s: %w", sha, err)
			}
			var env bytes.Buffer
			if err := run(&env, "env", "GOBIN", "GOPATH"); err != nil {
				return err
			}
			path := installedPath(env.String())
			fmt.Fprintf(out, "installed ym %s at %s\n", sha[:min(12, len(sha))], path)
			if self, err := os.Executable(); err == nil && !samePath(self, path) {
				fmt.Fprintf(out, "this ym runs from %s, which the update did not change\n", self)
			}
			return nil
		},
	}
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
