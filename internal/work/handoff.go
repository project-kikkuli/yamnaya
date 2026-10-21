package work

import "time"

type Handoff struct {
	Event, Agent, Session, Path, Quote, Version string
	Offset                                      int64
	At                                          time.Time
}
