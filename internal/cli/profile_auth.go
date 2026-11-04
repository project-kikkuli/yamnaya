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
	"time"

	"github.com/project-kikkuli/yamnaya/internal/profile"
)

// errStatusUnanswered is a native status check that did not answer in time: it says nothing about the login, unlike one that answered no.
var errStatusUnanswered = errors.New("native status check did not answer")

func switchAuthentication(p profile.Profile, cwd string) error {
	login := fmt.Sprintf("native login required for %s/%s; press i in B or run ym profiles login %s %s", p.Harness, p.ID, p.Harness, p.ID)
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
	binary, err := bin(p.Harness)
	if err != nil {
		return err
	}
	args := []string{"auth", "status", "--json"}
	if p.Harness == "codex" {
		args = []string{"-c", "sqlite_home=" + strconv.Quote(sqliteRoot(p)), "login", "status"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env, cmd.Dir, cmd.WaitDelay = p.Environment(os.Environ()), p.Root, time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", login, errStatusUnanswered)
	}
	if err != nil {
		return fmt.Errorf("%s (native status unavailable or logged out)", login)
	}
	if p.Harness == "claude" {
		var status struct {
			LoggedIn bool   `json:"loggedIn"`
			Method   string `json:"authMethod"`
		}
		if json.Unmarshal(out, &status) != nil || !status.LoggedIn || status.Method != "claude.ai" {
			return fmt.Errorf("%s", login)
		}
	}
	if profile.Identity(p) != p.Account {
		return fmt.Errorf("%s (native account changed)", login)
	}
	return nil
}
