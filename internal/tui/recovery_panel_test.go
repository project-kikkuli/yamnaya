package tui

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func blockedPanelAction(t *testing.T, c *core.Core) core.Activation {
	t.Helper()
	goal, err := c.Store.AddGoal(core.Goal{Ask: "supervise recovery delivery", Repo: "/repo/recovery"})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := c.Store.AddManager(core.Manager{Role: core.RoleManager, GoalID: goal.ID, Session: "recovery-supervisor", Harness: "claude", Repo: goal.Repo})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Store.SaveManager(manager); err != nil {
		t.Fatal(err)
	}
	goal.ManagerID = manager.ID
	if err := c.Store.SaveGoal(goal); err != nil {
		t.Fatal(err)
	}
	c.Harnesses[0].(harness).sessions[7000] = core.Session{ID: manager.Session, Status: core.Idle}
	previousProcs := c.Procs
	c.Procs = func() ([]proc.Proc, error) {
		rows, err := previousProcs()
		return append(rows, proc.Proc{PID: 7000, TTY: "/dev/recovery-supervisor", Args: []string{"claude"}}), err
	}
	if err := c.Store.Link("lead", manager.Session); err != nil {
		t.Fatal(err)
	}
	item, err := c.AddItem(core.Item{Kind: core.KindDecision, Session: "lead", Question: "which recovery behavior?", DoLine: "choose recovery behavior", Rec: "preserve identity", Options: picks})
	if err != nil || item.Recipient != manager.ID {
		t.Fatalf("managed ask %+v, %v", item, err)
	}
	b, err := c.Board()
	if err != nil {
		t.Fatal(err)
	}
	c.BackgroundSend = func(core.Live, string) error { return errors.New("native receipt unavailable") }
	if _, _, err := c.Answer(b, item.Key(), "Original action payload\n"+strings.Repeat("full original request ", 100)+"action-tail"); err != nil {
		t.Fatal(err)
	}
	if err := c.DispatchActions(b); err != nil {
		t.Fatal(err)
	}
	actions, err := c.Store.Activations()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range actions {
		if a.State == "blocked" || a.State == "uncertain" {
			return a
		}
	}
	t.Fatalf("no recoverable action: %+v", actions)
	return core.Activation{}
}

func panelActionState(t *testing.T, c *core.Core, id string) string {
	t.Helper()
	actions, err := c.Store.Activations()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range actions {
		if a.ID == id {
			return a.State
		}
	}
	t.Fatal("action disappeared")
	return ""
}

func TestYInspectsFullActionBeforeExplicitLocalRetry(t *testing.T) {
	m, _, _ := panelBoard(t)
	a := blockedPanelAction(t, m.core)
	m = recoveryPress(t, m, "Y")
	m = toPanelRow(t, m, a.ID)
	m = recoveryPress(t, m, "r")
	if got := panelActionState(t, m.core, a.ID); got != a.State {
		t.Fatalf("uninspected action retried: %s", got)
	}
	m = recoveryPress(t, m, "enter")
	if m.mode != modeScreen || !strings.Contains(m.screen, a.Payload) || !strings.Contains(m.screen, "action-tail") || !strings.Contains(m.screen, a.ID) {
		t.Fatalf("incomplete inspector: %q", m.screen)
	}
	m = recoveryPress(t, m, "esc", "r")
	if m.mode != modeConfirm {
		t.Fatal("retry had no explicit confirmation")
	}
	if got := panelActionState(t, m.core, a.ID); got != a.State {
		t.Fatal("confirmation dispatched retry early")
	}
	m = recoveryPress(t, m, "n", "r", "y")
	if got := panelActionState(t, m.core, a.ID); got != "pending" {
		t.Fatalf("explicit retry state %s", got)
	}
}

func TestYInspectsPreservedLaunchAndRetriesOnlyAfterConfirmation(t *testing.T) {
	m, _, sw := panelBoard(t)
	sw.startErr = errors.New("native startup failed before receipt")
	prompt := "Original launch\n" + strings.Repeat("preserve these launch details ", 80) + "launch-tail"
	if _, err := m.core.StartNew(core.NewLaunch{Session: "preserved-worker", Source: "board-recovery", Repo: t.TempDir(), Prompt: prompt}); err == nil {
		t.Fatal("launch failure hidden")
	}
	pending, err := m.core.Store.PendingLaunches()
	if err != nil || len(pending) != 1 {
		t.Fatalf("launches %+v, %v", pending, err)
	}
	p := pending[0]
	m = recoveryPress(t, m, "Y")
	m = toPanelRow(t, m, p.ID)
	m = recoveryPress(t, m, "r")
	if len(sw.started) != 1 {
		t.Fatal("uninspected launch retried")
	}
	m = recoveryPress(t, m, "enter")
	if m.mode != modeScreen || !strings.Contains(m.screen, prompt) || !strings.Contains(m.screen, p.Result) || !strings.Contains(m.screen, "preserved-worker") {
		t.Fatalf("original launch missing: %q", m.screen)
	}
	m = recoveryPress(t, m, "esc", "r")
	if len(sw.started) != 1 {
		t.Fatal("confirmation launched work before yes")
	}
	sw.startErr = nil
	m = recoveryPress(t, m, "y")
	if len(sw.started) != 2 || sw.started[1].Session != "preserved-worker" || sw.started[1].Request != p.ID {
		t.Fatalf("retry lost identity: %+v", sw.started)
	}
	if current, err := m.core.Store.PendingLaunches(); err != nil || len(current) != 0 {
		t.Fatalf("successful launch stays blocked: %+v, %v", current, err)
	}
}

func TestYRoutesInspectionAndRetryToTheSourceHost(t *testing.T) {
	m, _, _ := panelBoard(t)
	local := blockedPanelAction(t, m.core)
	_, remote, _ := setup(t)
	a := blockedPanelAction(t, remote)
	var retries []string
	m.pn.run = func(host, stdin string, args ...string) (string, error) {
		if host == "" {
			if slices.Equal(args, []string{"service", "status"}) {
				return `{"running":true}`, nil
			}
			return "connected", nil
		}
		if host != box {
			t.Fatalf("wrong source %s", host)
		}
		switch strings.Join(args, " ") {
		case "action list --json":
			rows, err := remote.Store.Activations()
			raw, _ := json.Marshal(rows)
			return string(raw), err
		case "launch list --json":
			return "[]", nil
		case "action retry " + a.ID:
			retries = append(retries, host)
			return "retried", remote.RetryAction(a.ID)
		}
		return "", errors.New("unexpected recovery command")
	}
	m = recoveryPress(t, m, "Y")
	m = toPanelRow(t, m, m.hostTag(box)+" action "+a.ID)
	m = recoveryPress(t, m, "enter")
	if !strings.Contains(m.screen, "Source: "+m.hostTag(box)) || !strings.Contains(m.screen, a.Payload) {
		t.Fatalf("source inspector missing: %q", m.screen)
	}
	m = recoveryPress(t, m, "esc", "r", "y")
	if !slices.Equal(retries, []string{box}) || panelActionState(t, remote, a.ID) != "pending" || panelActionState(t, m.core, local.ID) != local.State {
		t.Fatalf("retry crossed source boundary: %q", retries)
	}
}

func TestYShowsOfflineSourcesWithoutRetryOrLocalFallback(t *testing.T) {
	m, _, _ := panelBoard(t)
	var remoteReads int
	m.pn.run = func(host, stdin string, args ...string) (string, error) {
		if host == "" {
			return `{"running":true}`, nil
		}
		remoteReads++
		if !slices.Equal(args, []string{"action", "list", "--json"}) {
			t.Fatalf("offline source retried: %v", args)
		}
		return "", core.ErrUnreachable
	}
	m = recoveryPress(t, m, "Y")
	if !m.pn.settings.loaded || remoteReads != 1 || !strings.Contains(m.pn.settings.recoveryErrors[box], core.ErrUnreachable.Error()) {
		t.Fatalf("offline inspection state: loaded %v reads %d errors %v", m.pn.settings.loaded, remoteReads, m.pn.settings.recoveryErrors)
	}
}

func TestYRequiresReinspectionWhenTheLaunchChangesAfterInspection(t *testing.T) {
	m, _, sw := panelBoard(t)
	sw.startErr = errors.New("first startup failure")
	if _, err := m.core.StartNew(core.NewLaunch{Source: "changed-recovery", Repo: t.TempDir(), Prompt: "preserve this request"}); err == nil {
		t.Fatal("missing initial failure")
	}
	pending, err := m.core.Store.PendingLaunches()
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending %+v, %v", pending, err)
	}
	id := pending[0].ID
	m = recoveryPress(t, toPanelRow(t, recoveryPress(t, m, "Y"), id), "enter", "esc")
	sw.startErr = errors.New("new failure from another explicit attempt")
	if _, err := m.core.RetryLaunch(id); err == nil {
		t.Fatal("missing external failure")
	}
	sw.startErr = nil
	m = recoveryPress(t, m, "r", "y")
	if len(sw.started) != 2 || !strings.Contains(m.message, "changed") {
		t.Fatalf("stale inspection retried: starts %d, message %q", len(sw.started), m.message)
	}
	m = toPanelRow(t, m, id)
	m = recoveryPress(t, m, "r")
	if m.mode == modeConfirm {
		t.Fatal("changed record did not require another inspection")
	}
	m = recoveryPress(t, m, "enter")
	if !strings.Contains(m.screen, "new failure from another explicit attempt") {
		t.Fatalf("new reason missing: %q", m.screen)
	}
	m = recoveryPress(t, m, "esc", "r", "y")
	if len(sw.started) != 3 {
		t.Fatalf("fresh inspection failed to retry: %d", len(sw.started))
	}
}

func recoveryPress(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	var execute func(tea.Cmd)
	execute = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		message := cmd()
		if batch, ok := message.(tea.BatchMsg); ok {
			for _, next := range batch {
				execute(next)
			}
			return
		}
		next, follow := m.Update(message)
		m = next.(Model)
		switch message.(type) {
		case settingsMsg, recoveryMsg, actMsg, doneMsg:
			execute(follow)
		}
	}
	for _, key := range keys {
		next, cmd := m.Update(keyOf(key))
		m = next.(Model)
		execute(cmd)
	}
	return m
}

func TestYRechecksRemoteAttemptBeforeRetryingTheSameID(t *testing.T) {
	m, _, _ := panelBoard(t)
	a := core.Activation{ID: "same-remote-id", Recipient: "manager", State: "uncertain", Attempt: 1, Payload: "original remote action"}
	retries := 0
	m.pn.run = func(host, stdin string, args ...string) (string, error) {
		if host == "" {
			return `{"running":true}`, nil
		}
		if host != box {
			t.Fatalf("wrong source %s", host)
		}
		switch strings.Join(args, " ") {
		case "action list --json":
			raw, _ := json.Marshal([]core.Activation{a})
			return string(raw), nil
		case "launch list --json":
			return "[]", nil
		case "action retry " + a.ID:
			retries++
			return "retried", nil
		}
		return "", errors.New("unexpected command")
	}
	m = recoveryPress(t, m, "Y")
	m = toPanelRow(t, m, m.hostTag(box)+" action "+a.ID)
	m = recoveryPress(t, m, "enter", "esc")
	a.Attempt++
	a.Error = "later attempt needs a different review"
	m = recoveryPress(t, m, "r", "y")
	if retries != 0 || !strings.Contains(m.message, "changed") {
		t.Fatalf("uninspected remote attempt retried %d times: %q", retries, m.message)
	}
}
