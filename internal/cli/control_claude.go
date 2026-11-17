package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/spf13/cobra"
)

func managedClaudeArgv(rec launch.Record, native []string) ([]string, error) {
	ym, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := []string{ym, "control", "claude", "--binary", native[0], "--session", rec.Session, "--"}
	return scopedArgv(rec, append(args, native[1:]...)), nil
}

func claudeControlCmd() *cobra.Command {
	var binary, session string
	cmd := &cobra.Command{Use: "claude", Args: cobra.ArbitraryArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return runClaudeControl(cmd.Context(), binary, session, args)
	}}
	cmd.Flags().StringVar(&binary, "binary", "claude", "Native Claude executable")
	cmd.Flags().StringVar(&session, "session", "", "Native session identity")
	return cmd
}

func claudeChannelCmd() *cobra.Command {
	var socket, session string
	cmd := &cobra.Command{Use: "claude-channel", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		invoke := func(args ...string) error {
			ym, err := os.Executable()
			if err != nil {
				return err
			}
			child := exec.CommandContext(cmd.Context(), ym, append(args, "--session", session)...)
			child.Stderr = cmd.ErrOrStderr()
			if err := child.Run(); err != nil {
				return fmt.Errorf("record supervisor receipt: %w", err)
			}
			return nil
		}
		callbacks := claude.ChannelCallbacks{Accept: func(id string) error { return invoke("action", "accept", id) }, Complete: func(id, result string) error { return invoke("action", "complete", id, "--result", result) }}
		return claude.ServeChannel(cmd.Context(), socket, session, cmd.InOrStdin(), cmd.OutOrStdout(), callbacks)
	}}
	cmd.Flags().StringVar(&socket, "socket", "", "Private supervisor endpoint")
	cmd.Flags().StringVar(&session, "session", "", "Bound native session identity")
	_ = cmd.MarkFlagRequired("socket")
	_ = cmd.MarkFlagRequired("session")
	return cmd
}

func runClaudeControl(parent context.Context, binary, session string, nativeArgs []string) error {
	ctx, stop := signal.NotifyContext(parent, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	dir, err := os.MkdirTemp("/tmp", "ym-channel-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err = os.Chmod(dir, 0700); err != nil {
		return err
	}
	ym, err := os.Executable()
	if err != nil {
		return err
	}
	socket := filepath.Join(dir, "s")
	config := struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}{Servers: map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}{"yamnaya": {Command: ym, Args: []string{"control", "claude-channel", "--socket", socket, "--session", session}}}}
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	args := []string{"--dangerously-load-development-channels", "server:yamnaya", "--mcp-config", string(raw)}
	args = append(args, nativeArgs...)
	child := exec.CommandContext(ctx, binary, args...)
	child.Env = append(os.Environ(), claude.ChannelSocketEnv+"="+socket)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	return child.Run()
}
