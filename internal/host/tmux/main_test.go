package tmux

import (
	"os"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/agenttest"
)

// TestMain keeps every claude and codex these tests start out of the owner's ~/.claude and ~/.codex.
func TestMain(m *testing.M) { os.Exit(agenttest.Run(m)) }
