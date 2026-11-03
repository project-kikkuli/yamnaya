package core

// Switching is a login switch queued for a session: To is the profile it moves to, Blocked why its last apply failed.
type Switching struct {
	To      string
	Blocked string
}

func (s Switching) String() string {
	if s.Blocked != "" {
		return "switch -> " + s.To + " blocked: " + s.Blocked
	}
	return "switch pending -> " + s.To
}
