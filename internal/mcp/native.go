package mcp

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type Native struct {
	Binary string
	Run    func(context.Context, Target, []string, io.Reader, io.Writer, io.Writer) error
}

type Addition struct {
	Name      string
	Scope     string
	Transport string
	URL       string
	Command   []string
	BearerEnv string
}

func (n Native) Manage(ctx context.Context, t Target, in io.Reader, out, stderr io.Writer) error {
	if err := n.run(ctx, t, nil, in, out, stderr); err != nil {
		return fmt.Errorf("native MCP management ended without completing")
	}
	return nil
}

var serverName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
var envName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func (a Addition) Args(harness string) ([]string, error) {
	if !serverName.MatchString(a.Name) {
		return nil, fmt.Errorf("server name must contain letters, digits, dots, underscores or dashes")
	}
	if a.Scope != "user" && a.Scope != "local" && a.Scope != "project" {
		return nil, fmt.Errorf("scope must be user, local or project")
	}
	if harness == "codex" && a.Scope != "user" {
		return nil, fmt.Errorf("Codex native MCP add supports user scope only; project definitions remain visible and read-only")
	}
	args := []string{"mcp", "add"}
	if harness == "claude" {
		args = append(args, "--scope", a.Scope, "--transport", a.Transport)
	}
	if a.Transport == "stdio" {
		if len(a.Command) == 0 || a.URL != "" || a.BearerEnv != "" {
			return nil, fmt.Errorf("stdio requires an executable after -- and no URL or bearer variable")
		}
		return append(append(args, a.Name, "--"), a.Command...), nil
	}
	if a.Transport != "http" && !(harness == "claude" && a.Transport == "sse") {
		return nil, fmt.Errorf("transport unsupported by this native MCP add command; Claude supports http/sse/stdio, Codex http/stdio")
	}
	parsed, err := url.Parse(a.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || len(a.Command) > 0 {
		return nil, fmt.Errorf("provide an HTTP URL without credentials, query or fragment; authenticate separately in the native terminal")
	}
	if a.BearerEnv != "" {
		if harness != "codex" || !envName.MatchString(a.BearerEnv) {
			return nil, fmt.Errorf("--bearer-token-env-var requires a Codex HTTP server and a variable name, never a token value")
		}
		args = append(args, "--bearer-token-env-var", a.BearerEnv)
	}
	if harness == "claude" {
		return append(args, a.Name, a.URL), nil
	}
	return append(args, a.Name, "--url", a.URL), nil
}

func (n Native) run(ctx context.Context, t Target, args []string, in io.Reader, out, stderr io.Writer) error {
	if n.Run != nil {
		return n.Run(ctx, t, args, in, out, stderr)
	}
	cmd := exec.CommandContext(ctx, n.Binary, args...)
	cmd.Env = t.Native.Environment(os.Environ())
	cmd.Dir = t.Cwd
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, stderr
	if in == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		cmd.WaitDelay = time.Second
		defer func() {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		}()
	}
	return cmd.Run()
}

func (n Native) Add(ctx context.Context, t Target, a Addition) error {
	args, err := a.Args(t.Harness)
	if err != nil {
		return err
	}
	i, err := Read(t)
	if err != nil {
		return err
	}
	for _, s := range i.Servers {
		if s.Name == a.Name && s.Scope == a.Scope {
			return fmt.Errorf("server already exists in this scope; select its source before removing it")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = n.run(ctx, t, args, nil, io.Discard, io.Discard); err != nil {
		return fmt.Errorf("native MCP add failed; check installed native command support and selected configuration permissions (details withheld)")
	}
	return nil
}

func (n Native) Remove(ctx context.Context, t Target, s Server) error {
	if !s.Mutable {
		return fmt.Errorf("this source is read-only; manage it through its native project, plugin, cloud or policy source")
	}
	args := []string{"mcp", "remove"}
	if t.Harness == "claude" {
		args = append(args, "--scope", s.Scope)
	}
	args = append(args, s.Name)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := n.run(ctx, t, args, nil, io.Discard, io.Discard); err != nil {
		return fmt.Errorf("native MCP remove failed (details withheld)")
	}
	return nil
}

func (n Native) Auth(ctx context.Context, t Target, s Server, logout, noBrowser bool, in io.Reader, out, stderr io.Writer) error {
	if !s.Effective {
		return fmt.Errorf("native authentication selects the effective server; this definition is shadowed or inactive")
	}
	action := "login"
	if logout {
		action = "logout"
	}
	args := []string{"mcp", action, s.Name}
	if noBrowser && !logout {
		args = append(args, "--no-browser")
	}
	if err := n.run(ctx, t, args, in, out, stderr); err != nil {
		return fmt.Errorf("native MCP %s did not complete; credentials and callback URLs were not recorded by ym", action)
	}
	return nil
}

func (n Native) Check(ctx context.Context, t Target, s Server) (Observation, error) {
	o := Observation{Auth: "unknown", Health: "unknown", At: time.Now().UTC()}
	if !s.Effective {
		return o, fmt.Errorf("check the effective server; this definition is shadowed or inactive")
	}
	if s.Configuration == "disabled" {
		o.Health = "disabled"
		return o, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if t.Harness == "codex" {
		return n.checkCodex(ctx, t, s.Name)
	}
	var out strings.Builder
	err := n.run(ctx, t, []string{"mcp", "get", s.Name}, nil, &out, &out)
	status := strings.ToLower(out.String())
	switch {
	case strings.Contains(status, "pending approval"):
		o.Health = "approval-required"
	case strings.Contains(status, "needs authentication"), strings.Contains(status, "authentication required"), strings.Contains(status, "not authenticated"):
		o.Auth, o.Health = "needs-login", "authentication-required"
	case strings.Contains(status, "✓ connected"):
		o.Health = "connected"
	case strings.Contains(status, "disabled"):
		o.Health = "disabled"
	case strings.Contains(status, "enoent"), strings.Contains(status, "executable not found"):
		o.Health = "executable-unavailable"
	case strings.Contains(status, "unknown command"), strings.Contains(status, "unknown option"):
		o.Health = "unsupported"
	case strings.Contains(status, "failed to connect"), err != nil:
		o.Health = "native-connection-failed"
	}
	if ctx.Err() != nil {
		o.Health = "timed-out"
	}
	return o, nil
}
