package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/harness"
	"github.com/project-kikkuli/yamnaya/internal/signals"
	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/project-kikkuli/yamnaya/internal/tmux"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

type app struct {
	store       *store.Store
	tmux        tmux.Client
	project     string
	projectFlag string
}

func openApp(projectFlag string) (*app, error) {
	path, err := store.DefaultPath()
	if err != nil {
		return nil, err
	}
	s, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	project := projectFlag
	if project == "" {
		project = os.Getenv("YM_PROJECT")
	}
	return &app{store: s, tmux: tmux.FromEnv(), project: project, projectFlag: projectFlag}, nil
}

func (a *app) close() { a.store.Close() }

func (a *app) requireProject() (string, error) {
	if a.project == "" {
		return "", errors.New("no project: pass --project or run inside a ym window (YM_PROJECT)")
	}
	return a.project, nil
}

func validName(kind, name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("invalid %s name %q: use letters, digits, '-' and '_'", kind, name)
	}
	return nil
}

func absDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	return abs, nil
}

func windowEnv(project, window string) map[string]string {
	env := map[string]string{"YM_PROJECT": project, "YM_WINDOW": window}
	for _, k := range []string{"YAMNAYA_STATE_DIR", "YAMNAYA_TMUX_SOCKET", "YAMNAYA_CLAUDE_BIN", "YAMNAYA_CODEX_BIN", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	return env
}

// newWindow prepares a window record whose launch starts the harness in tmux.
func (a *app) newWindow(project, cwd, name, role, harnessName, prompt string) (store.Window, func(store.Window) (string, error), error) {
	l, err := harness.Start(harnessName, prompt)
	if err != nil {
		return store.Window{}, nil, err
	}
	w := store.Window{
		Project:   project,
		Name:      name,
		Role:      role,
		Harness:   harnessName,
		SessionID: l.SessionID,
		Prompt:    prompt,
		Managed:   true,
		CreatedAt: time.Now().UTC(),
	}
	launch := func(w store.Window) (string, error) {
		if harnessName == harness.Claude {
			if err := harness.TrustClaudeDir(cwd); err != nil {
				return "", fmt.Errorf("mark %s trusted for claude: %w", cwd, err)
			}
		}
		return a.tmux.NewWindow(tmux.SessionName(project), name, strconv.FormatInt(w.ID, 10), cwd, windowEnv(project, name), l.Argv)
	}
	return w, launch, nil
}

func (a *app) resolveCodexSessions(p store.Project, windows []store.Window) error {
	return signals.ResolveCodexSessions(a.store, p, windows)
}

func envWindow() string { return os.Getenv("YM_WINDOW") }

// liveWindow reports whether w's tmux window still runs; a reused tmux id carries another tag and does not count.
func liveWindow(w store.Window, live map[string]tmux.WindowInfo) bool {
	return signals.Live(w, live)
}

// closeIfDead closes the open window row named name when its tmux window no longer runs, so the name can be reused.
func (a *app) closeIfDead(project, name string) error {
	w, err := a.store.OpenWindow(project, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	live, err := a.tmux.Windows()
	if err != nil {
		return err
	}
	if liveWindow(w, live) {
		return nil
	}
	if signals.Present(w, live) {
		if err := a.tmux.KillWindow(w.TmuxTarget); err != nil {
			return err
		}
	}
	return a.store.CloseWindow(w.ID)
}

// startGrace is how long a fresh harness must keep running before ym calls its start good.
func startGrace() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("YAMNAYA_START_GRACE")); err == nil {
		return d
	}
	return 2 * time.Second
}

// checkStarted reports a window whose harness exited within the start grace, with the pane's last output.
func (a *app) checkStarted(project, name string) error {
	w, err := a.store.OpenWindow(project, name)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(startGrace())
	for {
		if p, err := a.tmux.Pane(w.TmuxTarget); err == nil && p.Dead {
			out := strings.Join(a.tmux.LastLines(w.TmuxTarget, 5), "\n")
			return fmt.Errorf("%s/%s exited right after starting:\n%s", project, name, out)
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}
