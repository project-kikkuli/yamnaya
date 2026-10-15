package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/skill"
)

type sessionJSON struct {
	Harness  string   `json:"harness"`
	ID       string   `json:"id"`
	PID      int      `json:"pid"`
	TTY      string   `json:"tty"`
	Cwd      string   `json:"cwd"`
	Repo     string   `json:"repo"`
	Branch   string   `json:"branch"`
	Title    string   `json:"title"`
	Status   string   `json:"status"`
	Prompt   string   `json:"prompt,omitempty"`
	Host     string   `json:"host"`
	Project  string   `json:"project"`
	Crew     int      `json:"crew"`
	Copies   int      `json:"copies"`
	CopyPIDs []int    `json:"copy_pids,omitempty"`
	Resume   []string `json:"resume,omitempty"`
}

func sessionsCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List every live Claude Code and Codex session with its host and project",
		Args:  cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			var out []sessionJSON
			for _, s := range b.Sessions {
				j := sessionJSON{Harness: s.Harness, ID: s.ID, PID: s.PID, TTY: s.TTY, Cwd: s.Cwd, Repo: s.Repo, Branch: s.Branch,
					Title: s.Title, Status: s.Status.String(), Host: s.Location(), Project: s.Project, Crew: len(s.Crew), Copies: len(s.Copies)}
				if s.Prompt != nil {
					j.Prompt = s.Prompt.String()
				}
				for _, c := range s.Copies {
					j.CopyPIDs = append(j.CopyPIDs, c.PID)
				}
				if s.Handle == nil {
					for _, t := range e.core.Targets(b, s.Key(), s.Harness) {
						j.Resume = t.Resume
					}
				}
				out = append(out, j)
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			for _, j := range out {
				line := fmt.Sprintf("%-6s %-8s %-8s %-22s %s", j.Harness, core.Short(j.ID), j.Status, j.Host, j.Title)
				if j.Copies > 1 {
					line += fmt.Sprintf("  [%d COPIES %v]", j.Copies, j.CopyPIDs)
				}
				if j.Prompt != "" {
					line += "  [" + j.Prompt + "]"
				}
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			return nil
		}),
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func switchCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use:   "switch <session> --to claude|codex",
		Short: "Port an idle session to the other harness with cs and print the command that resumes it",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			s, ok := sessionByPrefix(b, args[0])
			if !ok {
				return fmt.Errorf("no live session %q", args[0])
			}
			if s.Harness == to {
				return fmt.Errorf("%s already runs %s", core.Short(s.Key()), to)
			}
			if s.Status != core.Idle {
				return fmt.Errorf("%s is %s; switch once it is idle", core.Short(s.Key()), s.Status)
			}
			id, err := port(s.Cwd, s.ID, to)
			if err != nil {
				return err
			}
			resume := e.core.Targets(b, id, to)
			fmt.Fprintf(cmd.OutOrStdout(), "ported to %s session %s\n", to, id)
			if len(resume) > 0 && resume[0].Resume != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "quit the %s session, then run: %s\n", s.Harness, strings.Join(resume[0].Resume, " "))
			}
			return nil
		}),
	}
	cmd.Flags().StringVar(&to, "to", "", "claude or codex")
	cmd.MarkFlagRequired("to")
	return cmd
}

// port rebuilds a session in the other harness with cs and returns the new session id.
func port(cwd, id, to string) (string, error) {
	bin := os.Getenv("YAMNAYA_CS_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("cs"); err != nil {
			return "", fmt.Errorf("cs not found on PATH: %w", err)
		}
	}
	c := exec.Command(bin, "port", id, "--to", to)
	c.Dir = cwd
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("cs port %s --to %s: %w: %s", id, to, err, strings.TrimSpace(errb.String()))
	}
	f := strings.Fields(out.String())
	if len(f) == 0 {
		return "", fmt.Errorf("cs port %s printed no session id", id)
	}
	return f[len(f)-1], nil
}

func installCmd() *cobra.Command {
	var statusline bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the ym skill for Claude Code and Codex",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			src := filepath.Join(home, ".local", "share", "yamnaya", "skills", "ym")
			if err := os.RemoveAll(src); err != nil {
				return err
			}
			if err := writeSkill(src); err != nil {
				return err
			}
			shared := filepath.Join(home, ".agents", "skills", "ym")
			for _, l := range [][2]string{
				{shared, src},
				{filepath.Join(home, ".claude", "skills", "ym"), shared},
				{filepath.Join(home, ".codex", "skills", "ym"), shared},
			} {
				if err := symlink(l[0], l[1]); err != nil {
					return err
				}
				fmt.Fprintf(out, "skill: %s -> %s\n", l[0], l[1])
			}
			old := filepath.Join(home, ".config", "yamnaya", "tmux.conf")
			if err := os.Remove(old); err == nil {
				fmt.Fprintf(out, "removed %s; drop any source-file line for it from your tmux config\n", old)
			}
			if !statusline {
				return nil
			}
			self, err := os.Executable()
			if err != nil {
				return err
			}
			msg, err := wireStatusline(home, self)
			if err != nil {
				return err
			}
			fmt.Fprintln(out, msg)
			return nil
		},
	}
	cmd.Flags().BoolVar(&statusline, "statusline", false, "also make Claude Code's statusline record limits with ym")
	return cmd
}

func writeSkill(dst string) error {
	return fs.WalkDir(skill.FS, "ym", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, strings.TrimPrefix(path, "ym"))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := skill.FS.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// symlink points link at target, replacing a symlink but never a real file or directory.
func symlink(link, target string) error {
	if info, err := os.Lstat(link); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s exists and is not a symlink", link)
		}
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	return os.Symlink(target, link)
}
