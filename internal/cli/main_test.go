package cli

import (
	"os"
	"testing"
)

const testChild = "YM_CLI_TEST_CHILD"

// TestMain keeps ym from starting itself under go test, where the executable is the test binary: a detached helper, serve or
// relay would rerun every test instead, unreaped. A copy started anyway exits at once.
func TestMain(m *testing.M) {
	if os.Getenv(testChild) != "" {
		os.Exit(0)
	}
	os.Setenv(testChild, "1")
	detach = func(...string) error { return nil }
	os.Exit(m.Run())
}
