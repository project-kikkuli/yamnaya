package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/slot"
)

func TestSlotStatusTellsALaneHolderFromItsWaitersInOrder(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name  string
		claim slot.Claim
		want  string
	}{
		{"holder", slot.Claim{Lane: "yamnaya-test", LaneHolder: true}, "lane yamnaya-test holder"},
		{"first in line", slot.Claim{Lane: "yamnaya-test", LanePlace: 1}, "lane yamnaya-test #1 in line"},
		{"third in line", slot.Claim{Lane: "yamnaya-test", LanePlace: 3}, "lane yamnaya-test #3 in line"},
		{"a waiter from a ym older than tickets", slot.Claim{Lane: "yamnaya-test"}, "lane yamnaya-test "},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.claim.Command = []string{"go", "test"}
			c.claim.PID = 7
			if got := claimLine(c.claim, now); !strings.Contains(got+" ", c.want) {
				t.Fatalf("%q does not say %q", got, c.want)
			}
		})
	}
}
