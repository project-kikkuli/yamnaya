package core

// PendingSwitch is a login switch ym holds for a session until the session itself sits idle at its prompt; Blocked says what
// keeps it from applying.
type PendingSwitch struct {
	To      string
	Blocked string
}

func (p PendingSwitch) String() string {
	switch {
	case p.To == "":
		return ""
	case p.Blocked != "":
		return "switch -> " + p.To + " blocked: " + p.Blocked
	}
	return "switch pending -> " + p.To
}
