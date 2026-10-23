package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/slot"
)

func TestTheHeaderShowsSlotHoldersWaitersAndAnOverload(t *testing.T) {
	now := time.Now()
	use := func(goal, name string, cmd ...string) core.SlotUse {
		return core.SlotUse{Claim: slot.Claim{Slot: 1, Command: cmd, Since: now.Add(-3 * time.Minute)}, Goal: goal, Name: name}
	}
	for _, c := range []struct {
		name string
		m    core.Machine
		want []string
	}{
		{"no pool", core.Machine{}, nil},
		{"quiet", core.Machine{Slots: slot.Status{Capacity: 4, Cores: 12}, Load: 3},
			[]string{" slots 0/4 held · load 3.0 on 12 cores"}},
		{"busy and overloaded", core.Machine{Slots: slot.Status{Capacity: 4, Cores: 12}, Load: 140,
			Holders: []core.SlotUse{use("goal1", "PR tests", "go", "test", "./...")},
			Waiters: []core.SlotUse{use("goal3", "board", "make")},
			Hogs:    []core.Hog{{Goal: "goal1", Name: "PR tests", CPU: 620}}},
			[]string{" slots 1/4 held, 1 waiting · load 140.0 on 12 cores", "   slot 1  goal1 PR tests  go test ./...  3m",
				"   waiting  goal3 board  make  3m", " overloaded: load 140 on 12 cores · top: goal1 PR tests 620%"}},
	} {
		got := strings.Split(ansi.Strip(machineLines(c.m, now, 200)), "\n")[1:]
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: header lines\n%q\nwant\n%q", c.name, got, c.want)
		}
	}
}
