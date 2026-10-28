package cli

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

// remoteBin is where ym lives on a remote host, so nothing outside the user's home changes.
const remoteBin = "~/.local/bin/ym"

func remoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "remote <host> [ym args...]",
		Short:              "Run ym on a host over ssh: the board by default, or any ym command",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] == "-h" || args[0] == "--help" {
				return cmd.Help()
			}
			c := exec.Command(sshBin(), append(sshArgs(args[0]), remoteCommand(args[1:]))...)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			if term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd()) {
				c = (&remoteHosts{ssh: sshBin()}).Attach(args[0], args[1:]...)
			}
			err := c.Run()
			exit, exited := err.(*exec.ExitError)
			if exited && exit.ExitCode() == 255 {
				(&remoteHosts{ssh: sshBin()}).drop(args[0])
				os.Exit(255)
			}
			if err != nil && !exited {
				return err
			}
			if err := rememberHost(args[0]); err != nil {
				return err
			}
			if exited {
				os.Exit(exit.ExitCode())
			}
			return nil
		},
	}
	cmd.AddCommand(remoteInstallCmd(), remoteForgetCmd())
	return cmd
}

// rememberHost records host so this host's board reads its sessions from now on.
func rememberHost(host string) error {
	path, err := core.DefaultStorePath()
	if err != nil {
		return err
	}
	st, err := core.OpenStore(path)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.RecordHost(host, time.Now())
}

func remoteForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget <host>",
		Short: "Stop showing a host's sessions on this board until ym remote reaches it again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := core.DefaultStorePath()
			if err != nil {
				return err
			}
			st, err := core.OpenStore(path)
			if err != nil {
				return err
			}
			defer st.Close()
			known, err := st.ForgetHost(args[0])
			if err != nil {
				return err
			}
			if !known {
				return fmt.Errorf("ym does not remember %s", args[0])
			}
			fmt.Fprintf(cmd.OutOrStdout(), "forgot %s\n", args[0])
			return nil
		},
	}
}

// attachCommand is remoteCommand for a terminal: the host's tmux takes it for an xterm, so what a session copies or links
// reaches this end whichever terminal or tmux runs here.
func attachCommand(args []string) string {
	return "TERM=xterm-256color " + remoteCommand(args)
}

// remoteCommand runs ym on a host with its own directory on PATH, so what it starts finds ym and the user's tools without a login shell.

func remoteCommand(args []string) string {
	remote := `PATH="$HOME/.local/bin:$PATH" ` + remoteBin
	for _, a := range args {
		remote += " " + shellQuote(a)
	}
	return remote
}

func remoteInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install <host>",
		Short: "Build ym for a host, copy it to ~/.local/bin/ym there, and run ym install --statusline on it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, out := args[0], cmd.OutOrStdout()
			start := time.Now()
			probe, err := exec.Command(sshBin(), host, "uname -m; sha256sum "+remoteBin+" 2>/dev/null; true").Output()
			if exit, ok := err.(*exec.ExitError); ok {
				return fmt.Errorf("ssh %s: %w: %s", host, err, bytes.TrimSpace(exit.Stderr))
			} else if err != nil {
				return fmt.Errorf("ssh %s: %w", host, err)
			}
			if err := rememberHost(host); err != nil {
				return err
			}
			lines := strings.Fields(string(probe))
			if len(lines) == 0 {
				return fmt.Errorf("ssh %s uname -m printed nothing", host)
			}
			arch, err := goArch(lines[0])
			if err != nil {
				return err
			}
			bin, err := buildFor("linux", arch)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(bin)
			current := len(lines) > 1 && lines[1] == hex.EncodeToString(sum[:])
			setup := `mkdir -p ~/.local/bin && ` +
				`{ bash -lc 'command -v ym' >/dev/null 2>&1 || { f=~/.profile; [ -f ~/.bash_profile ] && f=~/.bash_profile; printf '\n[ -d "$HOME/.local/bin" ] && PATH="$HOME/.local/bin:$PATH"\n' >> "$f"; }; } && ` +
				remoteBin + ` install --statusline && { ` + remoteBin + ` service keep || ` + remoteBin + ` service ensure; }`
			var stdin io.Reader
			if !current {
				var gz bytes.Buffer
				w, _ := gzip.NewWriterLevel(&gz, gzip.BestSpeed)
				w.Write(bin)
				w.Close()
				stdin = &gz
				setup = `mkdir -p ~/.local/bin && gzip -dc > ~/.local/bin/ym.new && chmod 755 ~/.local/bin/ym.new && mv ~/.local/bin/ym.new ` + remoteBin + ` && ` + setup
			}
			c := exec.Command(sshBin(), host, setup)
			c.Stdin, c.Stdout, c.Stderr = stdin, out, cmd.ErrOrStderr()
			if err := c.Run(); err != nil {
				return fmt.Errorf("install on %s: %w", host, err)
			}
			verb := "copied"
			if current {
				verb = "already current"
			}
			fmt.Fprintf(out, "ym linux/%s %s on %s in %s; run it with: ym remote %s\n", arch, verb, host, time.Since(start).Round(100*time.Millisecond), host)
			return nil
		},
	}
}

func goArch(machine string) (string, error) {
	switch machine {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("unsupported host architecture %q", machine)
}

// buildFor returns a ym binary for goos/goarch: this executable when it already is one, else a build of ym's embedded source.
func buildFor(goos, goarch string) ([]byte, error) {
	if runtime.GOOS == goos && runtime.GOARCH == goarch {
		if self, err := os.Executable(); err == nil {
			return os.ReadFile(self)
		}
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		return nil, fmt.Errorf("building ym for %s/%s needs the go toolchain on PATH: %w", goos, goarch, err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cache, "yamnaya")
	if err := buildSource(gobin, dir, goos, goarch); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(dir, "ym-"+goos+"-"+goarch))
}

// buildSource builds ym for goos/goarch from the embedded source into dir, with the module's directory replaces, which the
// embedded source cannot carry, dropped for their upstream modules.
func buildSource(gobin, dir, goos, goarch string) error {
	src := filepath.Join(dir, "src")
	if err := os.RemoveAll(src); err != nil {
		return err
	}
	if err := os.CopyFS(src, yamnaya.Source); err != nil {
		return err
	}
	env := append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=-mod=mod")
	run := func(args ...string) ([]byte, error) {
		c := exec.Command(gobin, args...)
		c.Dir, c.Env = src, env
		out, err := c.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("go %s for %s/%s: %w: %s", args[0], goos, goarch, err, bytes.TrimSpace(out))
		}
		return out, nil
	}
	out, err := run("mod", "edit", "-json")
	if err != nil {
		return err
	}
	var mod struct {
		Replace []struct {
			Old, New struct{ Path, Version string }
		}
	}
	if err := json.Unmarshal(out, &mod); err != nil {
		return err
	}
	for _, r := range mod.Replace {
		if r.New.Version == "" {
			if _, err := run("mod", "edit", "-dropreplace="+r.Old.Path); err != nil {
				return err
			}
		}
	}
	_, err = run("build", "-trimpath", "-o", filepath.Join(dir, "ym-"+goos+"-"+goarch), "./cmd/ym")
	return err
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || r == '/' || r == '=' || r == ':' || r == ',' || r == '@' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
