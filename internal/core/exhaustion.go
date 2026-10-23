package core

import "time"

type Exhaustion struct {
	Harness   string    `json:"harness"`
	Profile   string    `json:"profile"`
	Account   string    `json:"account"`
	Session   string    `json:"session"`
	Model     string    `json:"model,omitempty"`
	Reason    string    `json:"reason"`
	Event     string    `json:"event"`
	Seen      time.Time `json:"seen"`
	Recovered bool      `json:"recovered,omitempty"`
}
type FailoverState struct {
	Session string    `json:"session"`
	Profile string    `json:"profile"`
	Account string    `json:"account"`
	Status  string    `json:"status"`
	Reason  string    `json:"reason"`
	Reset   time.Time `json:"reset"`
}
