package core

import (
	"fmt"
	"strings"
)

// Closed is how a ym goal close went. Output is what another host printed; Pending names the chief on another host asked to
// confirm the author is above the goal, and the close lands once it does.
type Closed struct {
	Goal        string
	Disposition string
	State       string
	Toll        Toll
	Items       int
	PRs         []string
	Pending     string
	Output      string
}

func (c Closed) String() string {
	if c.Disposition == "" {
		return c.Output
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "closed %s as %s, state %s: %s ended", c.Goal, c.Disposition, c.State, c.Toll)
	if c.Items > 0 {
		fmt.Fprintf(&sb, ", %s dismissed", count(c.Items, "open item"))
	}
	fmt.Fprintf(&sb, "; ym revive %s brings it back", c.Goal)
	for _, pr := range c.PRs {
		fmt.Fprintf(&sb, "\nstill open: %s", pr)
	}
	return sb.String()
}
