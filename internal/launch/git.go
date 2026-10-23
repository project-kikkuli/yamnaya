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
