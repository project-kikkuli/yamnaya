package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/agenttest"
	"github.com/project-kikkuli/yamnaya/internal/disk"
	"github.com/project-kikkuli/yamnaya/internal/pressure"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

const (
	testChild = "YM_CLI_TEST_CHILD"
	testRun   = "YM_CLI_TEST_RUN"
)

// TestMain keeps ym from starting itself under go test, where the executable is the test binary: a detached helper, serve or
// relay would rerun every test instead, unreaped. A copy started anyway exits at once. Agents tests start keep out of the owner's config,
// launches never wait on this host's processor load or memory pressure, and no test inherits the host's auth overrides.
// A copy started with testRun set runs the ym command its arguments name instead.
func TestMain(m *testing.M) {
	machineLoad = func() (float64, error) { return 0, nil }
	machineMemory = func(*pressure.Meter) (pressure.Host, error) { return pressure.Host{}, nil }
	for _, key := range profile.SubscriptionOverrides {
		os.Unsetenv(key)
	}
	if os.Getenv(testChild) != "" {
		dispatchControlFixture()
	}
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
	code := func() int {
		dir, err := os.MkdirTemp("", "ym-cli-capacity-")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(dir)
		file := filepath.Join(dir, "free")
		if err := os.WriteFile(file, []byte("0.5\n"), 0o600); err != nil {
			panic(err)
		}
		os.Setenv(disk.FreeFileEnv, file)
		return agenttest.Run(m)
	}()
	os.Exit(code)
}
