package cli

import (
	"flag"
	"os"
	"path/filepath"
	"strconv"
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

// TestMain keeps ym from rerunning the test binary as a helper, serve or relay, and runs the ym command a testRun copy names.
// Tests never read this host's load, memory pressure, auth overrides or owner config.
func TestMain(m *testing.M) {
	machineLoad = func() (float64, error) { return 0, nil }
	machineMemory = func(*pressure.Meter) (pressure.Host, error) { return pressure.Host{}, nil }
	for _, key := range profile.SubscriptionOverrides() {
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
	isolated = os.Getenv(isolatedEnv)
	os.Unsetenv(isolatedEnv)
	if os.Getenv(testChild) != "" && isolated == "" {
		os.Exit(0)
	}
	os.Setenv(testChild, "1")
	detach = func(...string) error { return nil }
	flag.Parse()
	parallel := false
	flag.Visit(func(f *flag.Flag) { parallel = parallel || f.Name == "test.parallel" })
	if !parallel {
		flag.Set("test.parallel", strconv.Itoa(isolatedAtOnce))
	}
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
