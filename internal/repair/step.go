// Package repair finds what keeps ym from working, on this host or on the way to another, fixes what it safely can and says
// in one line what is left to do.
package repair

import (
	"fmt"
	"strings"
	"time"
)

// Status is how one check came out.
type Status string

const (
	OK      Status = "ok"
	Fixed   Status = "fixed"
	Failed  Status = "fail"
	Skipped Status = "skip"
	Sudo    Status = "sudo"
)

// Step is one check as it ran: what it looked at, how that stands and how long it took.
type Step struct {
	Name   string
	Status Status
	Detail string
	Took   time.Duration
}

func (s Step) String() string {
	line := fmt.Sprintf("  %-5s %-9s %s", s.Status, s.Name, s.Detail)
	if s.Took > 0 {
		line += fmt.Sprintf(" (%s)", s.Took.Round(time.Millisecond))
	}
	return strings.TrimRight(line, " ")
}

// VerdictPrefix starts the line that ends every repair.
const VerdictPrefix = "verdict: "
