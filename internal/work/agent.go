package work

import "time"

type Agent struct {
	ID        string
	Name      string
	Role      string
	Workspace string
	Repo      string
	Execution Execution
	Session   string
	Sessions  []string
	Paused    bool
	Updated   time.Time
	Pending   int
	Error     string
	Running   bool
}
