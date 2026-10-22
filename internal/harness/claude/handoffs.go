package claude

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/handoff"
)

func (a *Adapter) HandoffFiles(session, cwd string) ([]handoff.File, error) {
	path := a.transcript(cwd, session)
	if path == "" {
		return nil, fmt.Errorf("claude transcript for %s is unavailable", session)
	}
	out := []handoff.File{{Harness: Name, Session: session, Agent: session, Path: path}}
	base := strings.TrimSuffix(path, ".jsonl")
	for _, pattern := range []string{filepath.Join(base, "subagents", "agent-*.jsonl"), filepath.Join(base, "subagents", "workflows", "*", "agent-*.jsonl")} {
		files, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			out = append(out, handoff.File{Harness: Name, Session: session, Agent: strings.TrimSuffix(strings.TrimPrefix(filepath.Base(file), "agent-"), ".jsonl"), Path: file})
		}
	}
	return out, nil
}
