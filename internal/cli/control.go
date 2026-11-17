package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/harness/codex"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/spf13/cobra"
)

func managedCodexArgv(rec launch.Record, binary, session, action, model string) ([]string, error) {
	ym, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := []string{ym, "control", "codex", "--binary", binary}
	if session != "" {
		args = append(args, "--session", session)
	}
	if action != "" {
		args = append(args, "--action", action)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "--", "--yolo")
	for _, arg := range codexDirs(rec) {
		if arg != "--no-daemon" {
			args = append(args, arg)
		}
	}
	return scopedArgv(rec, args), nil
}

func controlCmd() *cobra.Command {
	root := &cobra.Command{Use: "control", Short: "Native supervisor control transport", Hidden: true}
	var binary, session, action, model string
	cmd := &cobra.Command{Use: "codex", Args: cobra.ArbitraryArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return runCodexControl(cmd.Context(), binary, session, action, model, args)
	}}
	cmd.Flags().StringVar(&binary, "binary", "codex", "Native Codex executable")
	cmd.Flags().StringVar(&session, "session", "", "Existing thread")
	cmd.Flags().StringVar(&action, "action", "", "Supervisor assignment")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	root.AddCommand(cmd, claudeControlCmd(), claudeChannelCmd())
	return root
}

func runCodexControl(parent context.Context, binary, session, action, model string, nativeArgs []string) error {
	ctx, stop := signal.NotifyContext(parent, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	// Darwin Unix sockets have a short path limit even when TMPDIR is deeply nested.
	dir, err := os.MkdirTemp("/tmp", "ym-control-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	socket := filepath.Join(dir, "s")
	env := append(os.Environ(), codex.ControlSocketEnv+"="+socket)
	serverArgs := []string{"app-server", "--listen", "unix://" + socket}
	for i := 0; i < len(nativeArgs); i++ {
		if nativeArgs[i] == "-c" && i+1 < len(nativeArgs) {
			serverArgs = append(serverArgs, "-c", nativeArgs[i+1])
			i++
		}
	}
	server := exec.CommandContext(ctx, binary, serverArgs...)
	server.Env, server.Stderr = env, os.Stderr
	server.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := server.Start(); err != nil {
		return fmt.Errorf("start native control: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Wait() }()
	defer func() { _ = syscall.Kill(-server.Process.Pid, syscall.SIGKILL); <-done }()
	var client *codex.ControlClient
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for client == nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("native Codex control socket did not become ready")
		case <-ticker.C:
			if info, statErr := os.Lstat(socket); statErr == nil && info.Mode()&os.ModeSocket != 0 {
				if err := os.Chmod(socket, 0600); err != nil {
					return err
				}
				client, err = codex.ConnectControl(socket)
				if err != nil {
					return err
				}
			}
		}
	}
	defer client.Close()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	params := struct {
		ThreadID       string `json:"threadId,omitempty"`
		Cwd            string `json:"cwd"`
		Model          string `json:"model,omitempty"`
		ApprovalPolicy string `json:"approvalPolicy"`
		Sandbox        string `json:"sandbox"`
	}{ThreadID: session, Cwd: cwd, Model: model, ApprovalPolicy: "never", Sandbox: "danger-full-access"}
	method := "thread/start"
	if session != "" {
		method = "thread/resume"
	}
	var thread codex.ControlThread
	if err := client.Call(method, params, &thread); err != nil {
		return err
	}
	if thread.Thread.ID == "" {
		return errors.New("native control returned no thread identity")
	}
	if action != "" {
		if err := client.Supervisor(thread.Thread.ID, action); err != nil {
			return err
		}
	}
	args := []string{"--remote", "unix://" + socket, "resume"}
	args = append(args, nativeArgs...)
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, thread.Thread.ID)
	tui := exec.CommandContext(ctx, binary, args...)
	tui.Env, tui.Stdin, tui.Stdout, tui.Stderr = env, os.Stdin, os.Stdout, os.Stderr
	return tui.Run()
}
