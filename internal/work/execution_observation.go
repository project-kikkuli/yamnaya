package work

import "time"

type ExecutionObservation struct {
	State      string
	Result     string
	LastLiveAt time.Time
}
