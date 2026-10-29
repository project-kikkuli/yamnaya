package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func (n Native) checkCodex(ctx context.Context, t Target, name string) (Observation, error) {
	o := Observation{Auth: "unknown", Health: "unknown", At: time.Now().UTC()}
	cmd := exec.CommandContext(ctx, n.Binary, "app-server", "--listen", "stdio://")
	cmd.Env, cmd.Dir, cmd.Stderr = t.Native.Environment(os.Environ()), t.Cwd, io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		return o, fmt.Errorf("cannot open native MCP status input")
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return o, fmt.Errorf("cannot open native MCP status output")
	}
	if err = cmd.Start(); err != nil {
		input.Close()
		return o, fmt.Errorf("cannot start native Codex MCP status")
	}
	defer func() { input.Close(); _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() }()
	return codexStatus(input, output, name, o)
}

func codexStatus(input io.Writer, output io.Reader, name string, o Observation) (Observation, error) {
	reader := bufio.NewScanner(output)
	reader.Buffer(make([]byte, 4096), 8*1024*1024)
	call := func(id int, method string, params any) (json.RawMessage, error) {
		if err := json.NewEncoder(input).Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			return nil, fmt.Errorf("native Codex status request failed")
		}
		for reader.Scan() {
			var reply struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(reader.Bytes(), &reply) != nil || reply.ID != id {
				continue
			}
			if len(reply.Error) > 0 {
				return nil, fmt.Errorf("native Codex MCP status unsupported or refused (details withheld)")
			}
			return reply.Result, nil
		}
		return nil, fmt.Errorf("native Codex MCP status ended without a response")
	}
	if _, err := call(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "ym_mcp_status", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}); err != nil {
		return o, err
	}
	if err := json.NewEncoder(input).Encode(map[string]string{"method": "initialized"}); err != nil {
		return o, fmt.Errorf("native Codex status initialization failed")
	}
	raw, err := call(2, "mcpServerStatus/list", map[string]any{"serverName": name, "detail": "toolsAndAuthOnly"})
	if err != nil {
		return o, err
	}
	var result struct {
		Data []struct {
			Name       string          `json:"name"`
			Auth       string          `json:"authStatus"`
			Runtime    string          `json:"runtimeStatus"`
			ToolsError *string         `json:"toolsError"`
			Info       json.RawMessage `json:"serverInfo"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return o, fmt.Errorf("native Codex status schema unsupported")
	}
	for _, s := range result.Data {
		if s.Name != name {
			continue
		}
		switch s.Auth {
		case "notLoggedIn":
			o.Auth = "needs-login"
		case "oAuth":
			o.Auth = "oauth"
		case "bearerToken":
			o.Auth = "bearer-token"
		case "unsupported":
			o.Auth = "unsupported"
		}
		switch s.Runtime {
		case "connected":
			o.Health = "connected"
		case "authenticationRequired":
			o.Health = "authentication-required"
		case "failed":
			o.Health = "native-connection-failed"
		case "disabled":
			o.Health = "disabled"
		case "starting", "notStarted", "cancelled":
			o.Health = s.Runtime
		}
		if s.ToolsError != nil {
			o.Health = "native-discovery-failed"
		}
		if s.Runtime == "" && s.ToolsError == nil && len(s.Info) > 0 && string(s.Info) != "null" {
			o.Health = "initialized"
		}
		return o, nil
	}
	return o, fmt.Errorf("native Codex did not report this server; effective native configuration may differ")
}
