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

const ymPackage = "github.com/project-kikkuli/yamnaya/cmd/ym"

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

func updateCmd(run goRunner) *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Install the latest ym with go install; a running board restarts into it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if err := run(out, "install", ymPackage+"@latest"); err != nil {
				return fmt.Errorf("go install %s@latest: %w", ymPackage, err)
			}
			var env bytes.Buffer
			if err := run(&env, "env", "GOBIN", "GOPATH"); err != nil {
				return err
			}
			path := installedPath(env.String())
			var info bytes.Buffer
			if err := run(&info, "version", "-m", path); err != nil {
				return err
			}
			fmt.Fprintf(out, "installed ym %s at %s\n", modVersion(info.String()), path)
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

// modVersion is the main module's version in go version -m output.
func modVersion(info string) string {
	for _, l := range strings.Split(info, "\n") {
		if f := strings.Fields(l); len(f) >= 3 && f[0] == "mod" {
			return f[2]
		}
	}
	return "(unknown version)"
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
