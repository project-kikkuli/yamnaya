package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/discover"
	"github.com/project-kikkuli/yamnaya/internal/harness"
	"github.com/project-kikkuli/yamnaya/internal/signals"
	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/spf13/cobra"
)

func switchCmd(withApp runner) *cobra.Command {
	var to string
	var minIdle time.Duration
	cmd := &cobra.Command{
		Use:   "switch <window> --to claude|codex",
		Short: "Port an idle window's session to the other harness and resume it there",
		Args:  cobra.ExactArgs(1),
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			if err := harness.Valid(to); err != nil {
				return err
			}
			project, err := a.requireProject()
			if err != nil {
				return err
			}
			p, err := a.store.ActiveProject(project)
			if err != nil {
				return err
			}
			w, err := a.store.OpenWindow(project, args[0])
			if err != nil {
				return err
			}
			if w.Harness == to {
				return fmt.Errorf("window %q already runs %s", w.Name, to)
			}
			pane, staged, err := a.agentPane(w)
			if err != nil {
				return err
			}
			if info, err := a.tmux.Pane(pane); err != nil || info.Dead {
				return fmt.Errorf("window %q is not alive in tmux; ym revive %s first", w.Name, project)
			}
			windows := []store.Window{w}
			if err := a.resolveCodexSessions(p, windows); err != nil {
				return err
			}
			w = windows[0]
			if err := a.idleEnough(w, pane, staged, minIdle); err != nil {
				return err
			}
			if w.SessionID == "" {
				return fmt.Errorf("window %q has no recorded session to port yet", w.Name)
			}
			newID, err := portSession(p.Cwd, w.SessionID, to)
			if err != nil {
				return err
			}
			l, err := harness.Resume(to, newID)
			if err != nil {
				return err
			}
			if err := trustFor(to, p.Cwd); err != nil {
				return err
			}
			if err := a.refuseDuplicate(newID, ""); err != nil {
				return err
			}
			env := windowEnv(project, w.Name)
			if err := a.tmux.Respawn(pane, p.Cwd, env, harness.CarryEnv(to, l.Argv, env)); err != nil {
				return err
			}
			if err := a.store.SwitchHarness(w.ID, to, newID); err != nil {
				return err
			}
			if staged {
				st, _, err := a.store.Stage()
				if err != nil {
					return err
				}
				st.Harness, st.SessionID = to, newID
				if err := a.store.SetStage(st); err != nil {
					return err
				}
			}
			if err := a.checkStarted(project, w.Name); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s now runs %s session %s\n", project, w.Name, to, newID)
			return nil
		}),
	}
	cmd.Flags().StringVar(&to, "to", "", "target harness: claude or codex")
	cmd.Flags().DurationVar(&minIdle, "min-idle", 30*time.Second, "refuse unless the window has printed nothing for this long")
	cmd.MarkFlagRequired("to")
	return cmd
}

// portSession rebuilds a session in the target harness with cs and returns the new session id.
func portSession(cwd, sessionID, to string) (string, error) {
	bin := os.Getenv("YAMNAYA_CS_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("cs"); err != nil {
			return "", fmt.Errorf("cs not found on PATH: %w", err)
		}
	}
	c := exec.Command(bin, "port", sessionID, "--to", to)
	c.Dir = cwd
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("cs port %s --to %s: %w: %s", sessionID, to, err, strings.TrimSpace(errb.String()))
	}
	lines := strings.Fields(out.String())
	if len(lines) == 0 {
		return "", fmt.Errorf("cs port %s printed no session id", sessionID)
	}
	return lines[len(lines)-1], nil
}

// idleEnough refuses a window mid-turn: for Codex, whose TUI keeps redrawing, by its rollout's open task; otherwise by recent output or transcript writes.
// A staged window shares ym home with the sidebar, whose redraws would read as output, so only its transcript counts.
func (a *app) idleEnough(w store.Window, pane string, staged bool, minIdle time.Duration) error {
	if w.Harness == harness.Codex {
		if path := signals.NewTranscripts().Path(w.Harness, w.SessionID); path != "" {
			if open, modified := discover.CodexTurn(path); open && time.Since(modified) < discover.StaleAfter {
				return fmt.Errorf("window %q is mid-turn; switch once Codex finishes it", w.Name)
			}
			return nil
		}
	}
	var last time.Time
	if !staged {
		var err error
		if last, err = a.tmux.LastActivity(pane); err != nil {
			return err
		}
	}
	if t := signals.NewTranscripts().LastActive(w.Harness, w.SessionID); t.After(last) {
		last = t
	}
	if idle := time.Since(last); idle < minIdle {
		return fmt.Errorf("window %q is busy (output %s ago); switch once it has been idle %s", w.Name, idle.Round(time.Second), minIdle)
	}
	return nil
}
