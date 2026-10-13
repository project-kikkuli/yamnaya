package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/harness"
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

// resolveCodexSessions records Codex session ids for open windows whose thread Codex has since written.
func (a *app) resolveCodexSessions(p store.Project, windows []store.Window) error {
	var claimed map[string]bool
	for i, w := range windows {
		if w.Harness != harness.Codex || w.SessionID != "" {
			continue
		}
		if claimed == nil {
			var err error
			if claimed, err = a.store.ClaimedSessionIDs(harness.Codex); err != nil {
				return err
			}
		}
		id, err := harness.CodexSessionID(p.Cwd, w.CreatedAt, w.Prompt, claimed)
		if err != nil {
			return err
		}
		if id == "" {
			continue
		}
		if err := a.store.SetSessionID(w.ID, id); err != nil {
			return err
		}
		claimed[id] = true
		windows[i].SessionID = id
	}
	return nil
}

func envWindow() string { return os.Getenv("YM_WINDOW") }

// liveWindow reports whether w's tmux window still runs; a reused tmux id carries another tag and does not count.
func liveWindow(w store.Window, live map[string]tmux.WindowInfo) bool {
	info, ok := live[w.TmuxTarget]
	return ok && info.Tag == strconv.FormatInt(w.ID, 10) && info.Session == tmux.SessionName(w.Project) && !info.Dead
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
	return a.store.CloseWindow(w.ID)
}
