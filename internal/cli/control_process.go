package cli

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/harness/codex"
)

func nativeControlEnv(additional ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(additional))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID", "CLAUDECODE", codex.ControlSocketEnv, claude.ChannelSocketEnv:
			continue
		}
		env = append(env, entry)
	}
	return append(env, additional...)
}

func nativeControlShutdown(cmd *exec.Cmd, group bool) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if group {
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			if err == syscall.ESRCH {
				return os.ErrProcessDone
			}
			return err
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second
}
