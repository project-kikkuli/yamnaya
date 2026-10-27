package cli

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// linkRelayEnv tells the ym that ssh runs for an attach that the ym on the other end opens what a right-click here asks for.
const linkRelayEnv = "YAMNAYA_LINK_RELAY"

// linkNonce is the file holding the nonce for the terminal tty under state dir dir.
func linkNonce(dir, tty string) string {
	return filepath.Join(dir, "links", strings.ReplaceAll(strings.TrimSpace(tty), "/", "_"))
}

// announceLinks, under an attach the far end relays, keeps a fresh nonce for this terminal where only a right-click's
// link-open reads it and announces it to out before anything else is drawn; panes never see it in their environment.
func announceLinks(out io.Writer) {
	if os.Getenv(linkRelayEnv) == "" {
		return
	}
	os.Unsetenv(linkRelayEnv)
	c := exec.Command("tty")
	c.Stdin = os.Stdin
	tty, err := c.Output()
	if err != nil {
		return
	}
	dir, err := stateDir()
	if err != nil {
		return
	}
	path, nonce := linkNonce(dir, string(tty)), rand.Text()
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil || os.WriteFile(path, []byte(nonce), 0o600) != nil {
		return
	}
	fmt.Fprintf(out, oscNonce+"%s\a", nonce)
}

func linkOpenCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "link-open <state-dir> <url> <client-tty> <pane-tty>",
		Short:  "Ask the ym attached to client-tty to open url, as ym's tmux does on a right-click",
		Args:   cobra.ExactArgs(4),
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, url, client, pane := args[0], args[1], args[2], args[3]
			nonce, err := os.ReadFile(linkNonce(dir, client))
			if errors.Is(err, fs.ErrNotExist) || !webLink.MatchString(url) {
				return nil
			}
			if err != nil {
				return err
			}
			f, err := os.OpenFile(pane, os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = fmt.Fprintf(f, "\x1bPtmux;\x1b"+oscOpen+"%s;%s\a\x1b\\", nonce, url)
			return err
		},
	}
}
