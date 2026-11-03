// Throwaway: renders the patched fleetLine, and today's output via today's fold, from the real board.json fixture.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/tui"
)

func main() {
	raw, err := os.ReadFile("goal31-mockups/rajatsmachine-board-fixture.json")
	if err != nil {
		panic(err)
	}
	var fx struct {
		At          time.Time
		Self        string
		Today, Week float64
		Limits      []core.Limit
	}
	if err = json.Unmarshal(raw, &fx); err != nil {
		panic(err)
	}
	now := fx.At
	week := 7 * 24 * time.Hour
	laptop := []core.Limit{
		{Harness: "claude", Profile: "default", Account: "claude/laptop-default-invented/org", Name: "7d*", Used: 31, Window: week, ResetsAt: now.Add(50 * time.Hour), Seen: now.Add(-3 * time.Minute)},
		fx.Limits[1],
		{Harness: "codex", Profile: "default", Account: "codex/laptop-invented/acct", Name: "codex 7d*", Used: 12, Window: week, ResetsAt: now.Add(90 * time.Hour), Seen: now.Add(-2 * time.Minute)},
	}
	laptop[1].Seen = now.Add(-time.Hour)
	r := core.RemoteBoard{Host: fx.Self, Self: fx.Self, At: now, Usage: &core.Fleet{Ready: true, At: now, Limits: fx.Limits, Today: core.Spend{USD: fx.Today}, Week: core.Spend{USD: fx.Week}}}
	tag := func(h string) string { return "@" + core.Nicknames([]string{h}, nil)[h] }
	merged := core.MergeFleet(core.Fleet{Ready: true, At: now, Limits: laptop}, []core.RemoteBoard{r})
	own := core.MergeFleet(core.Fleet{Ready: true, At: now, Limits: fx.Limits, Today: core.Spend{USD: fx.Today}, Week: core.Spend{USD: fx.Week}}, nil)
	for _, w := range []int{44, 130} {
		todayFleet := merged
		todayFleet.PerHost = nil
		emit := func(name string, f core.Fleet) {
			s := tui.Goal31Line(f, now, w, tag)
			fmt.Printf("=== %s at %d cols (%d lines)\n%s\n", name, w, strings.Count(s, "\n")+1, s)
			os.WriteFile(fmt.Sprintf("goal31-mockups/%s-%d.txt", name, w), []byte(s+"\n"), 0o644)
		}
		emit("proposed-laptop-six", merged)
		emit("proposed-rajatsmachine-own", own)
		emitToday := func(name string, f core.Fleet) {
			s := tui.Goal31Today(f, now, w, tag)
			fmt.Printf("=== %s at %d cols (%d lines)\n%s\n", name, w, strings.Count(s, "\n")+1, s)
			os.WriteFile(fmt.Sprintf("goal31-mockups/%s-%d.txt", name, w), []byte(s+"\n"), 0o644)
		}
		emitToday("today-laptop", todayFleet)
		ownToday := own
		ownToday.PerHost = nil
		emitToday("today-rajatsmachine-own", ownToday)
	}
	fmt.Println("=== today's fold: Limits rows the laptop board keeps")
	for _, l := range merged.Limits {
		fmt.Printf("%s %s %.0f%%\n", l.IdentityLabel(), l.Name, l.Used)
	}
}
