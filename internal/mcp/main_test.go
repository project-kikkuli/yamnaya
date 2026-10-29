package mcp

import (
	"github.com/project-kikkuli/yamnaya/internal/agenttest"
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(agenttest.Run(m)) }
