package core

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"

	"github.com/project-kikkuli/yamnaya/internal/handoff"
)

type HandoffReader interface {
	HandoffFiles(session, cwd string) ([]handoff.File, error)
}

func (c *Core) CaptureHandoffs(ids ...string) error {
	all, err := c.Store.Work.List()
	if err != nil {
		return err
	}
	for _, w := range all {
		if len(ids) > 0 && !slices.Contains(ids, w.ID) {
			continue
		}
		for session, x := range w.Executions {
			if strings.HasPrefix(session, "pid:") {
				continue
			}
			for _, h := range c.Harnesses {
				reader, ok := h.(HandoffReader)
				if !ok || h.Name() != x.Harness {
					continue
				}
				files, readErr := reader.HandoffFiles(session, x.Directory)
				if readErr != nil {
					c.Store.Work.SourceError(w.ID, "handoff/"+session, readErr)
					continue
				}
				for _, file := range files {
					file.Work = w.ID
					info, e := os.Stat(file.Path)
					if e != nil {
						readErr = e
						continue
					}
					stat := info.Sys().(*syscall.Stat_t)
					identity := fmt.Sprintf("%d/%d", stat.Dev, stat.Ino)
					offset, e := c.Store.Handoffs.Cursor(file.Path, identity)
					if e != nil {
						return e
					}
					events, next, e := handoff.Read(file, offset)
					if e != nil {
						readErr = e
						continue
					}
					if next != offset || len(events) > 0 {
						if e = c.Store.Handoffs.Ingest(file.Path, identity, next, events); e != nil {
							return e
						}
					}
				}
				if err = c.Store.Work.SourceError(w.ID, "handoff/"+session, readErr); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
