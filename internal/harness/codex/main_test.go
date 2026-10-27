package codex

import (
	"os"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/agenttest"
)

// TestMain keeps these tests out of the owner's home, ym state and agents.
func TestMain(m *testing.M) { os.Exit(agenttest.Run(m)) }
