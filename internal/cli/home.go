package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/harness"
	"github.com/project-kikkuli/yamnaya/internal/signals"
	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/project-kikkuli/yamnaya/internal/tmux"
	"github.com/spf13/cobra"
)

const (
	homeSession  = signals.HomeSession
	homeWindow   = "=" + homeSession + ":home"
	sidebarWidth = 30
	roleSidebar  = "sidebar"
)

func homeEnv() map[string]string {
	env := map[string]string{}
	for _, k := range []string{"YAMNAYA_STATE_DIR", "YAMNAYA_TMUX_SOCKET", "YAMNAYA_CLAUDE_BIN", "YAMNAYA_CODEX_BIN", "YAMNAYA_CS_BIN", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	return env
}

// ymBin is the binary the sidebar and rest panes run; tests point it at a stand-in.
var ymBin = func() string {
	if bin, err := os.Executable(); err == nil {
		return bin
	}
	return "ym"
}

// ensureHome creates the ym session's home window with a sidebar and a resting stage pane, repairing whichever is missing.
func (a *app) ensureHome(cwd string) error {
	env := homeEnv()
	sidebarArgv := []string{ymBin(), "board", "--sidebar"}
	if !a.tmux.HasSession(homeSession) {
		if err := a.tmux.NewHome(homeSession, "home", cwd, env, sidebarArgv, []string{ymBin(), "rest"}, roleSidebar, signals.RoleRest, sidebarWidth); err != nil {
			return err
		}
		resize := fmt.Sprintf("resize-pane -t '%s.{left}' -x %d", homeWindow, sidebarWidth)
		for _, hook := range []string{"client-attached", "client-resized"} {
			if err := a.tmux.Hook(homeSession, hook, resize); err != nil {
				return err
			}
		}
	}
	panes, err := a.tmux.WindowPanes(homeWindow)
	if err != nil {
		return fmt.Errorf("ym home window is missing; kill the %s session and run ym again: %w", homeSession, err)
	}
	var sidebar string
	others := 0
	for _, p := range panes {
		if p.Role == roleSidebar {
			sidebar = p.ID
		} else {
			others++
		}
	}
	if sidebar == "" {
		if sidebar, err = a.tmux.Split(panes[0].ID, true, cwd, env, sidebarArgv); err != nil {
			return err
		}
		if err := a.tmux.SetPaneRole(sidebar, roleSidebar); err != nil {
			return err
		}
	}
	if others == 0 {
		rest, err := a.tmux.Split(sidebar, false, cwd, env, []string{ymBin(), "rest"})
		if err != nil {
			return err
		}
		if err := a.tmux.SetPaneRole(rest, signals.RoleRest); err != nil {
			return err
		}
	}
	return a.tmux.ResizeWidth(sidebar, sidebarWidth)
}

// stageSlot is the pane right of the sidebar: the resting placeholder or the staged agent.
func (a *app) stageSlot() (string, error) {
	panes, err := a.tmux.WindowPanes(homeWindow)
	if err != nil {
		return "", err
	}
	for _, p := range panes {
		if p.Role != roleSidebar {
			return p.ID, nil
		}
	}
	return "", errors.New("ym home has no stage pane")
}

// stage lends window id's agent pane to the stage, returning whatever was there first.
func (a *app) stage(id int64, focus bool) error {
	if st, ok, err := a.store.Stage(); err != nil {
		return err
	} else if ok && st.WindowID == id {
		return a.focusStaged(st.Pane, focus)
	}
	if err := a.unstage(); err != nil {
		return err
	}
	w, err := a.store.WindowByID(id)
	if err != nil {
		return err
	}
	live, err := a.tmux.Windows()
	if err != nil {
		return err
	}
	if !signals.Live(w, live) {
		return fmt.Errorf("window %s/%s is not alive in tmux", w.Project, w.Name)
	}
	agent, err := a.tmux.Pane(w.TmuxTarget)
	if err != nil {
		return err
	}
	return a.lend(agent.ID, w.ID, w.Harness, w.SessionID, focus)
}

// stagePane lends any pane, such as a discovered session's, to the stage.
// harnessName and sessionID, when discovery knows them, let the pane be resumed if it is destroyed on stage.
func (a *app) stagePane(pane, harnessName, sessionID string, focus bool) error {
	if st, ok, err := a.store.Stage(); err != nil {
		return err
	} else if ok && st.Pane == pane {
		return a.focusStaged(pane, focus)
	}
	if err := a.unstage(); err != nil {
		return err
	}
	return a.lend(pane, 0, harnessName, sessionID, focus)
}

func (a *app) focusStaged(pane string, focus bool) error {
	if !focus {
		return nil
	}
	return a.tmux.SelectPane(pane)
}

func (a *app) lend(pane string, windowID int64, harnessName, sessionID string, focus bool) error {
	if err := a.ensureHome(homeCwd()); err != nil {
		return err
	}
	agent, err := a.tmux.Pane(pane)
	if err != nil {
		return err
	}
	if agent.Session == homeSession {
		return fmt.Errorf("pane %s is part of ym home", pane)
	}
	slot, err := a.stageSlot()
	if err != nil {
		return err
	}
	st := store.Stage{WindowID: windowID, Origin: agent.Window, OriginSession: agent.Session, Pane: agent.ID, Harness: harnessName, SessionID: sessionID, Cwd: agent.Cwd}
	if err := a.store.SetStage(st); err != nil {
		return err
	}
	if err := a.tmux.SwapPane(agent.ID, slot); err != nil {
		return errors.Join(err, a.store.ClearStage())
	}
	return a.focusStaged(agent.ID, focus)
}

// unstage returns the staged agent pane to its origin window and puts the placeholder back on stage.
// A ym window whose staged pane was destroyed, as when the ym session is killed, is resumed in its origin window; a discovered session's placeholder is removed; one whose origin window was closed gets a new window in its project session.
func (a *app) unstage() error {
	st, ok, err := a.store.Stage()
	if err != nil || !ok {
		return err
	}
	agent, err := a.tmux.Pane(st.Pane)
	if err != nil {
		panes, _ := a.tmux.WindowPanes(st.Origin)
		for _, p := range panes {
			if p.Role != signals.RoleRest {
				continue
			}
			if err := a.resumeInto(st, p.ID); err != nil {
				return err
			}
		}
		return a.store.ClearStage()
	}
	if agent.Window == st.Origin {
		return a.store.ClearStage()
	}
	if panes, err := a.tmux.WindowPanes(st.Origin); err == nil {
		for _, p := range panes {
			if p.Role == signals.RoleRest {
				if err := a.tmux.SwapPane(agent.ID, p.ID); err != nil {
					return err
				}
				return a.store.ClearStage()
			}
		}
	}
	if st.WindowID == 0 {
		if _, err := a.tmux.BreakPane(agent.ID, st.OriginSession, ""); err != nil {
			return fmt.Errorf("return pane %s from the stage: %w", agent.ID, err)
		}
		return a.store.ClearStage()
	}
	w, err := a.store.WindowByID(st.WindowID)
	if err != nil {
		return err
	}
	target, err := a.tmux.BreakPane(agent.ID, tmux.SessionName(w.Project), w.Name)
	if err != nil {
		return fmt.Errorf("return %s/%s from the stage: %w", w.Project, w.Name, err)
	}
	if err := a.tmux.Tag(target, strconv.FormatInt(w.ID, 10)); err != nil {
		return err
	}
	if err := a.store.SetTarget(w.ID, target); err != nil {
		return err
	}
	return a.store.ClearStage()
}

// unstageProject returns the stage when it holds one of project's windows.
func (a *app) unstageProject(project string) error {
	st, ok, err := a.store.Stage()
	if err != nil || !ok {
		return err
	}
	if st.WindowID == 0 {
		return nil
	}
	w, err := a.store.WindowByID(st.WindowID)
	if err != nil || w.Project != project {
		return err
	}
	return a.unstage()
}

// healStage clears a stage whose agent process has ended and restores the resting pane.
func (a *app) healStage() error {
	st, ok, err := a.store.Stage()
	if err != nil || !ok {
		return err
	}
	if _, err := a.tmux.Pane(st.Pane); err == nil {
		return nil
	}
	if err := a.unstage(); err != nil {
		return err
	}
	return a.ensureHome(homeCwd())
}

func homeCwd() string {
	if dir, err := os.Getwd(); err == nil {
		return dir
	}
	return os.Getenv("HOME")
}

func homeCmd(withApp runner) *cobra.Command {
	return &cobra.Command{
		Use:   "home",
		Short: "Open the ym session: a board sidebar beside a stage for one agent window",
		Args:  cobra.NoArgs,
		RunE:  withApp(runHome),
	}
}

func runHome(a *app, cmd *cobra.Command, args []string) error {
	if err := a.ensureHome(homeCwd()); err != nil {
		return err
	}
	return a.tmux.Jump("=" + homeSession)
}

func restCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "rest",
		Short:  "Hold an empty stage or a staged window's place",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := tmux.FromEnv()
			pane := os.Getenv("TMUX_PANE")
			last := ""
			for {
				text := "\033[2m  select a window in the sidebar\033[0m"
				if info, err := c.Pane(pane); err == nil {
					if info.Session != homeSession {
						text = "\033[2m  this window is on the ym stage\033[0m"
					}
				}
				if text != last {
					fmt.Fprint(cmd.OutOrStdout(), "\033[2J\033[H"+text)
					last = text
				}
				time.Sleep(time.Second)
			}
		},
	}
}

// resumeInto replaces the placeholder holding a destroyed staged pane's place with its harness, resuming the recorded session in the recorded directory.
// A pane with nothing to resume, such as one whose session id was never known, just loses its placeholder.
func (a *app) resumeInto(st store.Stage, pane string) error {
	var argv []string
	cwd, env := st.Cwd, map[string]string{}
	if st.WindowID != 0 {
		w, err := a.store.WindowByID(st.WindowID)
		if err != nil {
			return err
		}
		p, err := a.store.Project(w.Project)
		if err != nil {
			return err
		}
		l, err := relaunch(w)
		if err != nil {
			return err
		}
		argv, cwd, env = l.Argv, p.Cwd, windowEnv(p.Name, w.Name)
	} else if st.Harness != "" && st.SessionID != "" {
		l, err := harness.Resume(st.Harness, st.SessionID)
		if err != nil {
			return err
		}
		argv = l.Argv
	}
	if argv == nil || cwd == "" {
		return a.tmux.KillPane(pane)
	}
	if err := a.refuseDuplicate(st.SessionID, pane); err != nil {
		return err
	}
	if err := trustFor(st.Harness, cwd); err != nil {
		return err
	}
	if err := a.tmux.SetPaneRole(pane, ""); err != nil {
		return err
	}
	return a.tmux.Respawn(pane, cwd, env, harness.CarryEnv(st.Harness, argv, env))
}

// agentPane is where window w's harness runs now: the stage pane while w is staged, else its own window.
func (a *app) agentPane(w store.Window) (pane string, staged bool, err error) {
	st, ok, err := a.store.Stage()
	if err != nil {
		return "", false, err
	}
	if ok && st.WindowID == w.ID {
		return st.Pane, true, nil
	}
	return w.TmuxTarget, false, nil
}

// refuseDuplicate errors when a live pane other than except already runs sessionID, so no path starts a second harness on one session.
func (a *app) refuseDuplicate(sessionID, except string) error {
	if sessionID == "" {
		return nil
	}
	panes, err := a.tmux.PanesRunning(sessionID)
	if err != nil {
		return err
	}
	for _, p := range panes {
		if p != except {
			return fmt.Errorf("session %s already runs in tmux pane %s", sessionID, p)
		}
	}
	return nil
}
