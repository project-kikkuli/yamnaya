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

// AddHandoffItem files the owner item a captured handoff asks for, once per key.
func (c *Core) AddHandoffItem(key string, it Item) (Item, error) {
	return c.Store.addItem(it, key)
}
