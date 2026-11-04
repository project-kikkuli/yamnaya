package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/profile"
)

// errStatusUnanswered is a native status check that did not answer in time: it says nothing about the login, unlike one that answered no.
var errStatusUnanswered = errors.New("native status check did not answer")

// statusTimeout is how long a native status check may take: it runs out of serve's pass, and an answer however late is worth having.
const statusTimeout = time.Minute

// loginUnusable is the login's own answer that it is signed out, the one failure that says something about it rather than about the attempt.
type loginUnusable struct{ error }

func (e loginUnusable) Unwrap() error { return e.error }

func answeredUnusable(err error) bool {
	var unusable loginUnusable
	return errors.As(err, &unusable)
}

func loginRequired(p profile.Profile) string {
	return fmt.Sprintf("native login required for %s/%s; press i in B or run ym profiles login %s %s", p.Harness, p.ID, p.Harness, p.ID)
}

// switchAuthentication is the login's status for a command someone is waiting on.
func switchAuthentication(p profile.Profile, cwd string) error {
	if err := loginConfigured(p, cwd); err != nil {
		return err
	}
	return nativeStatus(p, statusTimeout)
}

// loginConfigured is what can be read from the login's files, with no process to wait for.
func loginConfigured(p profile.Profile, cwd string) error {
	login := loginRequired(p)
	if p.Account == "" || !p.SubscriptionSafe(cwd) {
		return fmt.Errorf("%s", login)
	}
	if p.Harness == "codex" {
		var auth struct {
			Mode string `json:"auth_mode"`
		}
		data, err := os.ReadFile(filepath.Join(p.Root, "auth.json"))
		if err != nil || json.Unmarshal(data, &auth) != nil || auth.Mode != "chatgpt" {
			return fmt.Errorf("%s (ChatGPT subscription authentication required)", login)
		}
	}
	return nil
}

// nativeStatus asks the harness itself, unanswered if it takes longer than timeout.
func nativeStatus(p profile.Profile, timeout time.Duration) error {
	login := loginRequired(p)
	binary, err := bin(p.Harness)
	if err != nil {
		return err
	}
	args := []string{"auth", "status", "--json"}
	if p.Harness == "codex" {
		args = []string{"-c", "sqlite_home=" + strconv.Quote(sqliteRoot(p)), "login", "status"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env, cmd.Dir, cmd.WaitDelay = p.Environment(os.Environ()), p.Root, time.Second
	out, err := cmd.Output()
	var exit *exec.ExitError
	exited := errors.As(err, &exit)
	if ctx.Err() != nil || exited && !exit.Exited() {
		return fmt.Errorf("%s: %w", login, errStatusUnanswered)
	}
	if p.Harness == "claude" {
		var status struct {
			LoggedIn *bool  `json:"loggedIn"`
			Method   string `json:"authMethod"`
		}
		answered := json.Unmarshal(out, &status) == nil && status.LoggedIn != nil
		if answered && (!*status.LoggedIn || status.Method != "claude.ai") {
			return loginUnusable{fmt.Errorf("%s", login)}
		}
		if err == nil && !answered {
			return fmt.Errorf("%s", login)
		}
	}
	if exited && p.Harness == "codex" && strings.Contains(string(out)+string(exit.Stderr), "Not logged in") {
		return loginUnusable{fmt.Errorf("%s", login)}
	}
	if err != nil {
		return fmt.Errorf("%s (native status unavailable or logged out)", login)
	}
	if profile.Identity(p) != p.Account {
		return fmt.Errorf("%s (native account changed)", login)
	}
	return nil
}
