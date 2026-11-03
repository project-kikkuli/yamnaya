package core

// Switching is a login switch queued for a session: To is the profile it moves to, Blocked why its last apply failed.
type Switching struct {
	To      string `json:"to"`
	Blocked string `json:"blocked,omitempty"`
}

// SwitchOutcome is how a login switch request ended: Queued when it waits for the session's turn end, else applied.
type SwitchOutcome struct {
	Message string `json:"message"`
	Queued  bool   `json:"queued,omitempty"`
}

func (s Switching) String() string {
	if s.Blocked != "" {
		return "switch -> " + s.To + " blocked: " + s.Blocked
	}
	return "switch pending -> " + s.To
}
