package work

import "time"

type AgentBatch struct {
	ID, Actor, Text string
	Created         time.Time
	Recipients      []AgentRecipient
}

type AgentRecipient struct {
	Agent, Name, Workspace, Event, Error, State string
	Revision                                    int64
	Delivered, Incorporated                     time.Time
}

func (r AgentRecipient) Status() string {
	switch {
	case !r.Incorporated.IsZero():
		return "incorporated"
	case r.Error != "":
		return "failed"
	case !r.Delivered.IsZero():
		return "delivered"
	case r.Revision > 0:
		return "accepted"
	default:
		return "pending"
	}
}

type AgentMessage struct {
	Batch, Event, Actor, Text string
	Revision                  int64
}
