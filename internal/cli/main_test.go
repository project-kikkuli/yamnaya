package cli

import (
	"os"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/agenttest"
)

const (
	testChild = "YM_CLI_TEST_CHILD"
	testRun   = "YM_CLI_TEST_RUN"
)

// TestMain keeps ym from starting itself under go test, where the executable is the test binary: a detached helper, serve or
// relay would rerun every test instead, unreaped. A copy started anyway exits at once. Agents tests start keep out of the owner's config.
// A copy started with testRun set runs the ym command its arguments name instead.
func TestMain(m *testing.M) {
	if os.Getenv(testRun) != "" {
		root := NewRoot()
		root.SetArgs(os.Args[1:])
		if root.Execute() != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Getenv(testChild) != "" {
		os.Exit(0)
	}
	os.Setenv(testChild, "1")
	detach = func(...string) error { return nil }
	os.Exit(agenttest.Run(m))
}
