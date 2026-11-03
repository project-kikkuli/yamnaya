package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

const (
	fleetHostB = "prod-forge-ws-rajat-mehndiratta-rajatsmachine.exe.xyz"
	fleetHostC = "ci-delta-runner.example.net"
)

// fleetRows is today's real case on the local host and a second host holding the same claude-work account at another number,
// with one more host for each subscription past six.
func fleetRows(now time.Time, count int) (core.Fleet, []core.RemoteBoard) {
	limit := func(harness, id, account, name string, used float64, reset time.Duration, seen time.Time) core.Limit {
		return core.Limit{Harness: harness, Profile: id, Account: account, Name: name, Used: used, Window: 7 * 24 * time.Hour, ResetsAt: now.Add(reset), Seen: seen}
	}
	local := core.Fleet{Ready: true, At: now, Today: core.Spend{USD: 169}, Week: core.Spend{USD: 675}, Limits: []core.Limit{
		limit("claude", "default", "claude/personal", "7d", 100, 29*time.Hour, now.Add(-14*time.Hour-11*time.Minute)),
		limit("claude", "claude-work", "claude/work", "7d", 10, 163*time.Hour, now),
		limit("codex", "default", "codex/one", "codex 7d", 24, 21*time.Hour, now),
	}}
	second := []core.Limit{
		limit("claude", "claude-work", "claude/work", "7d", 7, 163*time.Hour, now.Add(-time.Hour)),
		limit("claude", "default", "claude/second", "7d", 31, 50*time.Hour, now),
		limit("codex", "default", "codex/two", "codex 7d", 12, 90*time.Hour, now),
	}
	remotes := []core.RemoteBoard{{Host: fleetHostB, Self: fleetHostB, At: now, Usage: &core.Fleet{Ready: true, At: now, Limits: second[:min(count-3, len(second))], Today: core.Spend{USD: 20}, Week: core.Spend{USD: 50}}}}
	if count > 6 {
		var extra []core.Limit
		for i := 6; i < count; i++ {
			extra = append(extra, limit("claude", fmt.Sprintf("login-%d", i), fmt.Sprintf("claude/other-%d", i), "7d", float64(40+i), 40*time.Hour, now))
		}
		remotes = append(remotes, core.RemoteBoard{Host: fleetHostC, Self: fleetHostC, At: now, Usage: &core.Fleet{Ready: true, At: now, Limits: extra}})
	}
	return local, remotes
}

func fleetModel(t *testing.T, now time.Time, count, width, height int) Model {
	t.Helper()
	m, c, _ := setup(t)
	local, remoteBoards := fleetRows(now, count)
	c.Meter = meter{local}
	c.Remotes, c.ShowRemotes = &remotes{boards: remoteBoards}, true
	m.width, m.height, m.peaks.hidden = width, height, true
	m = refresh(t, m)
	m.board.At = now
	m.page = nil
	return m
}

// hasRow reports whether cells appear together across the wrapped text, each whole on one line.
func hasRow(text string, cells ...string) bool {
	for _, cell := range cells {
		if !strings.Contains(text, cell) {
			return false
		}
	}
	return strings.Contains(strings.Join(strings.Fields(text), " "), strings.Join(cells, " "))
}

func TestTheToplineShowsEverySubscriptionOnEveryHostAtAnyWidth(t *testing.T) {
	now := time.Date(2026, 11, 3, 21, 5, 41, 0, time.UTC)
	type row struct{ cells []string }
	base := []row{
		{[]string{"local", "claude", "7d 100% ↻1d5h", "(14h11m old)"}},
		{[]string{"local", "claude/claude-work", "7d 10% ↻6d19h"}},
		{[]string{"local", "codex", "7d 24% ↻21h0m"}},
		{[]string{"@rajatsmachine", "claude/claude-work", "7d 7% ↻6d19h", "(1h0m old)"}},
		{[]string{"@rajatsmachine", "claude", "7d 31% ↻2d2h"}},
		{[]string{"@rajatsmachine", "codex", "7d 12% ↻3d18h"}},
	}
	order := []int{0, 1, 2, 4, 3, 5}
	for _, count := range []int{5, 6, 9} {
		for _, width := range []int{44, 50, 80, 130, 200} {
			t.Run(fmt.Sprintf("%d subscriptions at %d cols", count, width), func(t *testing.T) {
				m := fleetModel(t, now, count, width, 40)
				text := ansi.Strip(fleetLine(m.board.Fleet, now, m.width, m.hostTag))
				for _, l := range strings.Split(text, "\n") {
					if ansi.StringWidth(l) >= width {
						t.Errorf("line is %d columns on a %d-column board: %q", ansi.StringWidth(l), width, l)
					}
				}
				var want []row
				for _, i := range order {
					if i < count {
						want = append(want, base[i])
					}
				}
				for _, r := range want {
					if !hasRow(text, r.cells...) {
						t.Errorf("no row %q in\n%s", r.cells, text)
					}
				}
				for i := 6; i < count; i++ {
					if !hasRow(text, "@runner", fmt.Sprintf("claude/login-%d", i), fmt.Sprintf("7d %d%%", 40+i)) {
						t.Errorf("no row for login-%d in\n%s", i, text)
					}
				}
				rows := strings.Count(text, "local ") + strings.Count(text, "@")
				if rows != count {
					t.Errorf("%d host labels for %d subscriptions in\n%s", rows, count, text)
				}
			})
		}
	}
}

func TestTheUsageScreenListsEverySubscriptionUnderItsHost(t *testing.T) {
	now := time.Date(2026, 11, 3, 21, 5, 41, 0, time.UTC)
	m := fleetModel(t, now, 6, 80, 40)
	text := m.usageText()
	for _, want := range []string{"limit: local claude 7d 100%", "limit: local claude/claude-work 7d 10%", "limit: local codex 7d 24%",
		"limit: @rajatsmachine claude 7d 31%", "limit: @rajatsmachine claude/claude-work 7d 7%", "limit: @rajatsmachine codex 7d 12%"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
}

func TestAUsageOnlyHostIsNamedShortlyAndNotReportedAsAHost(t *testing.T) {
	now := time.Date(2026, 11, 3, 21, 5, 41, 0, time.UTC)
	m, c, _ := setup(t)
	local, _ := fleetRows(now, 6)
	laptop := "byo-mbp-dm6tyx3m6c"
	c.Meter = meter{local}
	m.width, m.height, m.peaks.hidden = 44, 30, true
	m = refresh(t, m)
	m.board.At = now
	m.board.Fleet.PerHost = append(m.board.Fleet.PerHost, core.Limit{Host: laptop, Harness: "claude", Profile: "default", Name: "7d", Used: 31, ResetsAt: now.Add(time.Hour), Seen: now})
	m.page = nil
	header := ansi.Strip(m.header())
	if !strings.Contains(header, "@dm6tyx3m6c claude") {
		t.Errorf("no short label for the laptop:\n%s", header)
	}
	if strings.Contains(header, "no answer") || strings.Contains(header, " hosts ") {
		t.Errorf("a usage-only host was reported as a host:\n%s", header)
	}
}

func TestAFullScreenKeepsTheSelectedRowAndAllItsUsageWhereItFits(t *testing.T) {
	now := time.Date(2026, 11, 3, 21, 5, 41, 0, time.UTC)
	numbers := []string{"7d 100% ↻1d5h", "7d 10% ↻6d19h", "7d 24% ↻21h0m", "7d 31% ↻2d2h", "7d 7% ↻6d19h", "7d 12% ↻3d18h", "spend today $189"}
	for _, size := range []struct {
		w, h     int
		allUsage bool
	}{{44, 30, true}, {80, 24, true}, {130, 40, true}, {44, 20, true}, {44, 14, false}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			m := fleetModel(t, now, 6, size.w, size.h)
			view := ansi.Strip(m.View().Content)
			if dir := os.Getenv("YM_SCREEN_DIR"); dir != "" {
				os.WriteFile(fmt.Sprintf("%s/%dx%d.txt", dir, size.w, size.h), []byte(view), 0o644)
			}
			if lines := strings.Split(view, "\n"); len(lines) > size.h {
				t.Errorf("%d lines on a %d-row screen", len(lines), size.h)
			}
			if !strings.Contains(view, "1 dec") {
				t.Errorf("the selected row is off screen:\n%s", view)
			}
			if next := ansi.Strip(press(t, m, "down").View().Content); !strings.Contains(next, "2 dec") {
				t.Errorf("the row moved to is off screen:\n%s", next)
			}
			if !size.allUsage {
				return
			}
			for _, n := range numbers {
				if !strings.Contains(view, n) {
					t.Errorf("the screen lost %q:\n%s", n, view)
				}
			}
		})
	}
}
