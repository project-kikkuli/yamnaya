package core

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/project-kikkuli/yamnaya/internal/handoff"
)

type HandoffReader interface {
	HandoffFiles(session, cwd string) ([]handoff.File, error)
}

// CaptureHandoffs reads new transcript records of b's live sessions into the handoff store.
func (c *Core) CaptureHandoffs(b Board) error {
	var failures []error
	for _, s := range b.Sessions {
		if strings.HasPrefix(s.Key(), "pid:") {
			continue
		}
		for _, h := range c.Harnesses {
			reader, ok := h.(HandoffReader)
			if !ok || h.Name() != s.Harness {
				continue
			}
			files, err := reader.HandoffFiles(s.Key(), s.Cwd)
			if err != nil {
				failures = append(failures, fmt.Errorf("handoff/%s: %w", s.Key(), err))
				continue
			}
			for _, file := range files {
				file.Work, file.Dir = s.Key(), s.Cwd
				if err := c.captureFile(file); err != nil {
					failures = append(failures, fmt.Errorf("handoff/%s: %w", s.Key(), err))
				}
			}
		}
	}
	return errors.Join(failures...)
}

func (c *Core) captureFile(file handoff.File) error {
	info, err := os.Stat(file.Path)
	if err != nil {
		return err
	}
	stat := info.Sys().(*syscall.Stat_t)
	identity := fmt.Sprintf("%d/%d", stat.Dev, stat.Ino)
	offset, err := c.Store.Handoffs.Cursor(file.Path, identity)
	if err != nil {
		return err
	}
	events, next, err := handoff.Read(file, offset)
	if err != nil {
		return err
	}
	if next == offset && len(events) == 0 {
		return nil
	}
	return c.Store.Handoffs.Ingest(file.Path, identity, next, events)
}

// FileHandoff records what a captured handoff asks and names what it became. An agent a manager runs gets one item, routed like
// AddItem, with its message attached verbatim; a session the owner started gets no item, only its one waiting row showing that message.
func (c *Core) FileHandoff(b Board, key string, it Item, message string) (string, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return "", err
	}
	if _, managed := o.Managed(c.Resolve(it.Session), c.parentsFor(b, nil)); !managed {
		return "waiting:" + it.Session, c.Store.MarkAsking(it.Session, message)
	}
	it.Assumption = strings.TrimSpace(it.Assumption + "\n\nThe agent's message, verbatim:\n" + message)
	it, err = c.file(b, nil, it, key)
	return "item:" + it.Key(), err
}
