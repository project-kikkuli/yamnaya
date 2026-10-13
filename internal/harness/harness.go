package harness

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

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

// Resume builds the command that reopens an existing session in its harness.
func Resume(name, sessionID string) (Launch, error) {
	if err := Valid(name); err != nil {
		return Launch{}, err
	}
	bin, err := binary(name)
	if err != nil {
		return Launch{}, err
	}
	if name == Claude {
		return Launch{Argv: []string{bin, "--resume", sessionID}, SessionID: sessionID}, nil
	}
	return Launch{Argv: []string{bin, "resume", sessionID}, SessionID: sessionID}, nil
}

// CarryEnv returns argv with ym's YM_* and YAMNAYA_* variables set for the harness's own shell commands.
// Codex builds that environment from its shell_environment_policy, which may drop them, so each is set there explicitly.
func CarryEnv(name string, argv []string, env map[string]string) []string {
	if name != Codex || len(argv) == 0 {
		return argv
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		if strings.HasPrefix(k, "YM_") || strings.HasPrefix(k, "YAMNAYA_") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := []string{argv[0]}
	for _, k := range keys {
		out = append(out, "-c", "shell_environment_policy.set."+k+"="+strconv.Quote(env[k]))
	}
	return append(out, argv[1:]...)
}
