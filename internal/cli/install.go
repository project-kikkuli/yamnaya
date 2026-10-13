package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/project-kikkuli/yamnaya/skill"
	"github.com/spf13/cobra"
)

func installCmd() *cobra.Command {
	var boardKey, nextKey string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the ym skill for Claude and Codex and write the tmux key bindings",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			bin, err := os.Executable()
			if err != nil {
				return err
			}
			if bin, err = filepath.EvalSymlinks(bin); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			src := filepath.Join(home, ".local", "share", "yamnaya", "skills", "ym")
			if err := writeSkill(src); err != nil {
				return err
			}
			shared := filepath.Join(home, ".agents", "skills", "ym")
			links := [][2]string{
				{shared, src},
				{filepath.Join(home, ".claude", "skills", "ym"), shared},
				{filepath.Join(home, ".codex", "skills", "ym"), shared},
			}
			for _, l := range links {
				if err := symlink(l[0], l[1]); err != nil {
					return err
				}
				fmt.Fprintf(out, "skill: %s -> %s\n", l[0], l[1])
			}
			conf := filepath.Join(home, ".config", "yamnaya", "tmux.conf")
			if err := writeTmuxConf(conf, bin, boardKey, nextKey); err != nil {
				return err
			}
			fmt.Fprintf(out, "tmux: %s (prefix+%s board, prefix+%s next)\n", conf, boardKey, nextKey)
			if main, err := os.ReadFile(filepath.Join(home, ".tmux.conf")); err != nil || !strings.Contains(string(main), "yamnaya/tmux.conf") {
				fmt.Fprintf(out, "add to ~/.tmux.conf: source-file -q %s\n", conf)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&boardKey, "board-key", "Space", "prefix key that opens the board popup")
	cmd.Flags().StringVar(&nextKey, "next-key", "n", "prefix key that jumps to the next window needing you")
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

// symlink points link at target, replacing an existing symlink but never a real file or directory.
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

func writeTmuxConf(path, bin, boardKey, nextKey string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	conf := fmt.Sprintf("bind-key %s display-popup -E -w 80%% -h 70%% '%s board'\nbind-key %s run-shell '%s next'\n", boardKey, bin, nextKey, bin)
	return os.WriteFile(path, []byte(conf), 0o644)
}
