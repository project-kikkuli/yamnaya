package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestAGoalRowNamesItsBriefWhereverTheGoalRuns(t *testing.T) {
	brief := &core.Brief{Version: 86, CreatedAt: time.Now().Add(-3 * time.Minute)}
	for _, c := range []struct {
		name string
		row  goalRow
		want string
	}{
		{"here", goalRow{ID: "mgr1", fresh: core.Freshness{Brief: brief}}, "  brief v86"},
		{"on another host", goalRow{ID: "box/mgr1", Host: "box", fresh: core.Freshness{Brief: brief}}, "  on box, brief v86"},
		{"on a host not answering", goalRow{ID: "box/mgr1", Host: "box", HostState: "no answer for 12m", fresh: core.Freshness{Brief: brief}},
			"  on box (no answer for 12m), brief v86"},
		{"on another host, none written", goalRow{ID: "box/mgr1", Host: "box"}, "  on box, no brief"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out strings.Builder
			printGoalRow(&out, c.row)
			if !strings.Contains(out.String(), c.want) {
				t.Errorf("row %q, want it to contain %q", out.String(), c.want)
			}
		})
	}
}
