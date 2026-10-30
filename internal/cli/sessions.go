package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
	Parent       string   `json:"parent,omitempty"`
	ConfigRoot   string   `json:"config_root,omitempty"`
	Profile      string   `json:"profile,omitempty"`
	ProfileLabel string   `json:"profile_label,omitempty"`
	Account      string   `json:"account,omitempty"`
	Ref          string   `json:"ref,omitempty"`
	Harness      string   `json:"harness"`
	ID           string   `json:"id"`
	PID          int      `json:"pid"`
	TTY          string   `json:"tty"`
	Cwd          string   `json:"cwd"`
	Repo         string   `json:"repo"`
	RepoKey      string   `json:"repo_key,omitempty"`
	RepoID       string   `json:"repo_id,omitempty"`
	Worktree     string   `json:"worktree,omitempty"`
	Branch       string   `json:"branch"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	WaitingFor   string   `json:"waiting_for,omitempty"`
	Since        string   `json:"since,omitempty"`
	Background   bool     `json:"background,omitempty"`
	Prompt       string   `json:"prompt,omitempty"`
	Host         string   `json:"host"`
	Remote       string   `json:"remote,omitempty"`
	Project      string   `json:"project"`
	Crew         int      `json:"crew"`
	Copies       int      `json:"copies"`
	CopyPIDs     []int    `json:"copy_pids,omitempty"`
	Resume       []string `json:"resume,omitempty"`
	// Adrift are the signs that the session, or a crew member of it, is busy going nowhere.
	Adrift []string `json:"adrift,omitempty"`
	// Last is the last line of the session's latest reply, written at LastAt.
	Last   string `json:"last,omitempty"`
	LastAt string `json:"last_at,omitempty"`
}

func sessionsCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var asJSON, local bool
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List every live Claude Code and Codex session, on this host and the hosts ym remote reached, with its host and project",
		Args:  cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if local {
				e.core.Remotes = nil
			}
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			out := sessionsJSON(e.core, b)
			if asJSON {
				for _, r := range b.Remotes {
					if r.Err != nil || r.At.IsZero() {
						out = append(out, sessionJSON{Remote: r.Host, Host: r.Host, Status: "unreachable", Title: r.State()})
					}
				}
				return writeJSON(cmd.OutOrStdout(), out)
			}
			for _, j := range out {
				line := fmt.Sprintf("%-6s %-8s %-8s %-22s %s", profileDisplay(j.Harness, j.Profile), core.Short(j.ID), j.Status, j.Host, j.Title)
				if j.Copies > 1 {
					line += fmt.Sprintf("  [%d COPIES %v]", j.Copies, j.CopyPIDs)
				}
				if j.Prompt != "" {
					line += "  [" + j.Prompt + "]"
				}
				for _, d := range j.Adrift {
					line += "  [adrift " + d + "]"
				}
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			for _, r := range b.Remotes {
				if r.Err != nil || r.At.IsZero() {
					fmt.Fprintf(cmd.OutOrStdout(), "host   %s %s\n", r.Host, r.State())
				}
			}
			return nil
		}),
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().BoolVar(&local, "local", false, "only this host's sessions")
	return cmd
}

func sessionsJSON(c *core.Core, b core.Board) []sessionJSON {
	out := []sessionJSON{}
	for _, s := range b.Sessions {
		j := sessionJSON{Parent: b.ParentOf(s.Ref()), ConfigRoot: s.ConfigRoot, Harness: s.Harness, Profile: s.Profile, ProfileLabel: s.ProfileLabel, Account: s.Account, Ref: s.Ref(), ID: s.ID, PID: s.PID, TTY: s.TTY, Cwd: s.Cwd, Repo: s.Repo, RepoKey: s.RepoKey, RepoID: s.RepoID, Worktree: s.Worktree,
			Branch: s.Branch, Title: s.Title, Status: s.Status.String(), WaitingFor: s.WaitingFor, Since: ts(s.Since), Background: s.Background,
			Host: s.Location(), Remote: s.Remote, Project: s.Project, Crew: len(s.Crew), Copies: len(s.Copies), Last: s.LastLine(), LastAt: ts(s.ResultAt)}
		if s.Prompt != nil {
			j.Prompt = s.Prompt.String()
		}
		for _, cp := range s.Copies {
			j.CopyPIDs = append(j.CopyPIDs, cp.PID)
		}
		for _, d := range s.Drift {
			j.Adrift = append(j.Adrift, d.String())
		}
		for _, m := range s.Crew {
			for _, d := range m.Drift {
				j.Adrift = append(j.Adrift, m.Name+": "+d.String())
			}
		}
		if s.Handle == nil && s.Remote == "" {
			for _, t := range c.Targets(b, s.Key(), s.Harness) {
				j.Resume = t.Resume
			}
		}
		out = append(out, j)
	}
	return out
}

func switchCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var to, selected string
	var relaunch bool
	cmd := &cobra.Command{
		Use:   "switch <session> --profile ID | --to claude|codex [--profile ID] [--relaunch]",
		Short: "Port an idle session to the other harness with cs; --relaunch also ends it and starts the port on ym's server",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if selected != "" && to == "" {
				l, ok := e.core.Launches.(launcher)
				if !ok {
					return fmt.Errorf("launcher unavailable")
				}
				msg, err := l.SwitchProfile(args[0], selected)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), msg)
				return nil
			}
			return switchSessionProfile(e.core, args[0], to, selected, relaunch, cmd.OutOrStdout())
		}),
	}
	cmd.Flags().StringVar(&to, "to", "", "claude or codex")
	cmd.Flags().BoolVar(&relaunch, "relaunch", false, "end the session and resume the port on ym's server in the same directory")
	cmd.Flags().StringVar(&selected, "profile", "", "switch an idle or exited session to this login and resume it")
	return cmd
}

func switchSession(k *core.Core, ref, to string, relaunch bool, out io.Writer) error {
	return switchSessionProfile(k, ref, to, "", relaunch, out)
}

func switchSessionProfile(k *core.Core, ref, to, selected string, relaunch bool, out io.Writer) error {
	b, err := k.Board()
	if err != nil {
		return err
	}
	s, err := sessionByPrefix(b, ref)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return err
	}
	if err != nil {
		r, err := k.Find(ref)
		if err != nil {
			return fmt.Errorf("no live session %q", ref)
		}
		s = r
	}
	var msg string
	if relaunch {
		msg, err = k.RelaunchWithProfile(s, to, selected)
	} else {
		msg, err = k.SwitchWithProfile(b, s, to, selected)
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(out, msg)
	return nil
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
			keepDefault(out)
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
