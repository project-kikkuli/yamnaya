package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/project-kikkuli/yamnaya/internal/cli"
	"github.com/project-kikkuli/yamnaya/internal/tui"
)

func main() {
	err := cli.NewRoot().Execute()
	if errors.Is(err, tui.ErrRestart) {
		err = cli.Restart()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ym:", err)
		os.Exit(cli.ExitCode(err))
	}
}
