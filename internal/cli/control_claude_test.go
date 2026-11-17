package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
)

func TestManagedClaudePreservesNativeTaskAndAttachesItsOwnChannel(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "claude-fixture")
	capture := filepath.Join(dir, "args")
	endpoint := filepath.Join(dir, "endpoint")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$YM_CAPTURE_ARGS\"\nprintf '%s' \"$YAMNAYA_CLAUDE_CHANNEL_SOCKET\" > \"$YM_CAPTURE_SOCKET\"\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YM_CAPTURE_ARGS", capture)
	t.Setenv("YM_CAPTURE_SOCKET", endpoint)
	if err := runClaudeControl(context.Background(), binary, "native", []string{"--dangerously-skip-permissions", "--session-id", "native", "initial task"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(args) < 6 || args[0] != "--dangerously-load-development-channels" || args[1] != "server:yamnaya" || args[2] != "--mcp-config" || args[len(args)-1] != "initial task" {
		t.Fatalf("native arguments changed: %q", args)
	}
	var config struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(args[3]), &config); err != nil {
		t.Fatal(err)
	}
	server, ok := config.Servers["yamnaya"]
	if !ok || server.Command == "" || len(server.Args) < 2 || server.Args[0] != "control" || server.Args[1] != "claude-channel" {
		t.Fatalf("channel is not native MCP: %+v", config)
	}
	socket, err := os.ReadFile(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(socket) == 0 {
		t.Fatal(claude.ChannelSocketEnv + " not inherited")
	}
	if _, err := os.Stat(filepath.Dir(string(socket))); !os.IsNotExist(err) {
		t.Fatalf("private runtime directory leaked: %v", err)
	}
}
