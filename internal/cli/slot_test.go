package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/slot"
)

func TestSlotStatusShowsLaneHoldersAndPlacesInLine(t *testing.T) {
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
		{"second for a slot", slot.Claim{SlotPlace: 2}, "#2 for a slot"},
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

func TestSlotStatusNamesTheJobsTheirOwnDiskHoldsBack(t *testing.T) {
	var st slot.Status
	raw := `{"capacity":3,"waiters":[
		{"pid":7,"command":["make"],"reports_disk":true,"disk_low":{"path":"/low","total":100,"avail":4}},
		{"pid":8,"command":["make"],"reports_disk":true}]}`
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printSlots(&out, st, 0, time.Now())
	for _, want := range []string{"/low has 4.0% free", "holds back pid 7;", "other jobs can still get slots", "held by low disk /low"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "pid 8;") || strings.Contains(out.String(), "new slots wait") {
		t.Errorf("status overclaims:\n%s", out.String())
	}
}
