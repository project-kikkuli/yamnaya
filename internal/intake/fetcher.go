// Package intake finds the pull requests GitHub asks the owner to review directly and those the owner wrote.
package intake

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type Fetcher struct {
	Run func(context.Context, string, ...string) ([]byte, error)
}

func New() Fetcher {
	return Fetcher{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		out, err := exec.CommandContext(ctx, name, args...).Output()
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
		}
		return out, err
	}}
}
