package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestYmExitsWithItsOwnStatusOnlyForAFinalRefusal(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{errors.New("database is locked"), 1},
		{fmt.Errorf("item #2: %w", core.ErrNotFound), 1},
		{core.Final(errors.New("item D2 is answered; cannot move it to answered")), exitFinal},
		{fmt.Errorf("answer: %w", core.Final(errors.New("gone"))), exitFinal},
		{exitStatus(7), 7},
		{fmt.Errorf("nothing to kill: %w: %w", core.ErrGone, core.ErrNotFound), exitGone},
	} {
		if got := ExitCode(tc.err); got != tc.want {
			t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}
