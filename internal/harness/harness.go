package harness

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/google/uuid"
)

const (
	Claude = "claude"
	Codex  = "codex"
)

type Launch struct {
	Argv      []string
	SessionID string
}

func Valid(name string) error {
	if name != Claude && name != Codex {
		return fmt.Errorf("unknown harness %q (want claude or codex)", name)
	}
	return nil
}

func binary(name string) (string, error) {
	env := map[string]string{Claude: "YAMNAYA_CLAUDE_BIN", Codex: "YAMNAYA_CODEX_BIN"}[name]
	if bin := os.Getenv(env); bin != "" {
		return bin, nil
	}
	return exec.LookPath(name)
}

// Start builds the command for a fresh session; Claude takes its session id up front, Codex reports it later via Codex state.
func Start(name, prompt string) (Launch, error) {
	if err := Valid(name); err != nil {
		return Launch{}, err
	}
	bin, err := binary(name)
	if err != nil {
		return Launch{}, err
	}
	l := Launch{Argv: []string{bin}}
	if name == Claude {
		l.SessionID = uuid.NewString()
		l.Argv = append(l.Argv, "--session-id", l.SessionID)
	}
	if prompt != "" {
		l.Argv = append(l.Argv, prompt)
	}
	return l, nil
}
