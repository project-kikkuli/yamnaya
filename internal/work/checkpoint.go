package work

import "time"

type Checkpoint struct {
	Brief   Brief
	Inputs  map[string]int64
	Next    string
	WaitFor string
	WakeAt  time.Time
}
