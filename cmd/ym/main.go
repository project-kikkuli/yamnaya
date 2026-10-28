package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/project-kikkuli/yamnaya/internal/cli"
	"github.com/project-kikkuli/yamnaya/internal/tui"
)

func main() {
	if err := cli.RefuseForeign(); err != nil {
		fmt.Fprintln(os.Stderr, "ym:", err)
		os.Exit(1)
	}
	err := cli.NewRoot().Execute()
	if errors.Is(err, tui.ErrRestart) {
		err = cli.Restart()
	}
	if err != nil {
		if !cli.Logged(err) {
			fmt.Fprintln(os.Stderr, "ym:", err)
		}
		if hint := cli.OwnershipHint(err); hint != "" {
			fmt.Fprintln(os.Stderr, "ym:", hint)
		}
		os.Exit(cli.ExitCode(err))
	}
}
