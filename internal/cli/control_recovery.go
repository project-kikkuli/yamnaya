package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func mergeRecoveryNote(first, next string) string {
	if strings.TrimSpace(next) == "" || strings.Contains(first, next) {
		return first
	}
	if strings.TrimSpace(first) == "" {
		return next
	}
	return first + "\n\n" + next
}

func (l launcher) queueRecoveryDirective(rec launch.Record) error {
	if rec.Handoff == "" || rec.Session == "" {
		return nil
	}
	if l.core == nil {
		return errors.New("recovery directives require Yamnaya's durable action ledger")
	}
	text := fmt.Sprintf("Yamnaya resumed launch %s at %s.\n\n%s", rec.Key, rec.AttachedAt.UTC().Format(time.RFC3339Nano), rec.Handoff)
	return l.core.RequestDirective(rec.Session, text)
}
