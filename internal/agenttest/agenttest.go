// Package agenttest keeps the claude and codex a test starts, real or fake, out of the owner's ~/.claude and ~/.codex.
package agenttest

import (
	"os"
	"path/filepath"
	"testing"
)

// Run runs m with CLAUDE_CONFIG_DIR and CODEX_HOME in a fresh temporary directory, removed afterwards, and returns its exit code.
func Run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "ymtest-agents-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	for _, env := range []string{"CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID"} {
		os.Unsetenv(env)
	}
	for env, sub := range map[string]string{"CLAUDE_CONFIG_DIR": "claude", "CODEX_HOME": "codex"} {
		path := filepath.Join(dir, sub)
		if err := os.MkdirAll(path, 0o755); err != nil {
			panic(err)
		}
		os.Setenv(env, path)
	}
	return m.Run()
}
