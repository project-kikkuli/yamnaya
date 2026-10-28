package launch

import (
	"os"
	"os/exec"
	"strconv"
)

func gitCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	noAutoMaintenance(cmd)
	return cmd
}

func noAutoMaintenance(cmd *exec.Cmd) {
	count, _ := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT"))
	cmd.Env = append(cmd.Environ(),
		"GIT_CONFIG_COUNT="+strconv.Itoa(count+2),
		"GIT_CONFIG_KEY_"+strconv.Itoa(count)+"=gc.auto",
		"GIT_CONFIG_VALUE_"+strconv.Itoa(count)+"=0",
		"GIT_CONFIG_KEY_"+strconv.Itoa(count+1)+"=maintenance.auto",
		"GIT_CONFIG_VALUE_"+strconv.Itoa(count+1)+"=false",
	)
}

// local answers a read-only git query from objects already in the clone, never fetching a missing one from a promisor remote.
func local(args ...string) error {
	cmd := gitCommand(append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false"}, args...)...)
	cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1")
	return cmd.Run()
}
