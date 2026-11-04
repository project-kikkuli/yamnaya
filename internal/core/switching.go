package core

// Switching is a login switch queued for a session: To is the profile it moves to, Blocked why its last apply failed.
type Switching struct {
	To      string `json:"to"`
	Blocked string `json:"blocked,omitempty"`
	// AtStop marks no switch at all but why a session held at its quota dialog cannot move: it shows only while the session waits there.
	AtStop bool `json:"at_stop,omitempty"`
}

// LoginName names a session's login whenever its harness has more than one to tell apart, the native login included.
func LoginName(profiles []LoginProfile, harness, id string) string {
	if id != "" && id != "default" {
		return harness + "/" + id
	}
	logins := 0
	for _, p := range profiles {
		if p.Harness == harness {
			logins++
		}
	}
	if logins > 1 {
		return harness + "/default"
	}
	return harness
}

// SwitchOutcome is how a login switch request ended: Queued when it waits for the session's turn end, else applied.
type SwitchOutcome struct {
	Message string `json:"message"`
	Queued  bool   `json:"queued,omitempty"`
}

func (s Switching) String() string {
	if s.AtStop {
		return s.Blocked
	}
	if s.Blocked != "" {
		return "switch -> " + s.To + " blocked: " + s.Blocked
	}
	return "switch pending -> " + s.To
}
