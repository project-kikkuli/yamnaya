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

func TestGoalListTitlesSkipStandingInstructionsAndKeepTheAskBehindAFlag(t *testing.T) {
	standing := "Fix forward. Diagnose before acting.\n\n"
	long := strings.Repeat("word ", 400)
	rows := func() []goalRow {
		return []goalRow{
			{ID: "mgr1", Ask: standing + "Make the importer handle CSV. Then " + long},
			{ID: "mgr2", Ask: standing + "Rotate the signing keys."},
			{ID: "mgr3", Ask: standing + long},
			{ID: "mgr4", Role: core.RoleChief, Name: "platform"},
		}
	}
	for _, tc := range []struct {
		name string
		full bool
	}{{"by default", false}, {"with --full", true}} {
		t.Run(tc.name, func(t *testing.T) {
			got := titled(rows(), tc.full)
			if got[0].Title != "Make the importer handle CSV" || got[1].Title != "Rotate the signing keys" {
				t.Fatalf("titles %q, %q", got[0].Title, got[1].Title)
			}
			if n := len([]rune(got[2].Title)); n > titleMax || strings.Contains(got[2].Title, "\n") {
				t.Fatalf("title of %d runes: %q", n, got[2].Title)
			}
			var sb strings.Builder
			printGoalRow(&sb, got[0])
			if has := strings.Contains(sb.String(), strings.TrimSpace(long)); has != tc.full {
				t.Fatalf("whole ask printed: %v, want %v", has, tc.full)
			}
		})
	}
}
