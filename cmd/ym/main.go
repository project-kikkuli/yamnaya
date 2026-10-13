package main

import (
	"fmt"
	"os"

	"github.com/project-kikkuli/yamnaya/internal/cli"
)

func main() {
	if err := cli.NewRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "ym:", err)
		os.Exit(1)
	}
}
