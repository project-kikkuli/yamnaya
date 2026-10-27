package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func helpText(width int) string {
	gold := lipgloss.NewStyle()
	if pal.color {
		gold = gold.Foreground(lipgloss.Color("#e0b030"))
	}
	g := func(s string) string {
		return "   " + strings.ReplaceAll(ansi.Wrap(s, max(20, width-4), " "), "\n", "\n     ")
	}
	var b strings.Builder
	b.WriteString(Help)
	b.WriteString("\n Mountains (each repository is a range, each goal or session you started a peak; A hides them)\n")
	for _, l := range []string{
		pal.green.Render("λ") + " agent at work, climbing the west face; the leader stands higher the more is done",
		pal.red.Render("λ") + " agent stopped and waiting on you",
		"▴ N resting: idle teammates that stay on the mountain",
		"▲ pull request camp on the east face; higher = more of its 6 gates passed (branch, commits, PR, CI, threads, merged)",
		pal.green.Render("▲") + " camp whose CI passed      " + pal.red.Render("▲") + " camp whose CI fails",
		gold.Render("⚑") + " merged pull request (several collapse to \"N merged\")",
		gold.Render("⚑") + " on a summit: goal done, or every pull request merged",
		pal.red.Render("│ ✸") + " red flare over a peak: something waits on you; it blinks and the name turns red",
		"name NN%: how far along it is (see Percentages)",
	} {
		b.WriteString(g(l) + "\n")
	}
	b.WriteString("\n Weather (per peak; the sky above a range takes the worst of its peaks)\n")
	for _, l := range []string{
		"Clear: moving or verifying, or a session with agents or pull requests in flight",
		"Clouds: quiet (nudged, or nothing moved for 30 minutes), adrift, or a session with no active agent and no camp yet",
		"Storm: dark cloud, rain " + pal.dim.Render("╱") + " and lightning: a stuck goal, or a pull request with failing CI",
		"Eruption: lava, smoke and embers, no climbers: the manager's session died and has not come back",
		"Summit: glow and halo: goal done, or every pull request merged; a group only when all its members are",
		"Sky tint: where peaks overlap, Eruption beats Storm beats Summit beats Clouds",
	} {
		b.WriteString(g(l) + "\n")
	}
	b.WriteString("\n Decoration only, no meaning: sun, moon and stars (your clock), lake, pines, fog, wisps, drifting clouds, far ridges, rim light.\n")
	b.WriteString(" Colors never mark a goal or repository: only weather, camp state and time of day.\n")
	b.WriteString("\n Percentages\n")
	for _, l := range []string{
		"done % = (finished crew + gates passed) / (crew + 6 x pull requests and pushed branches not closed)",
		"Crew is every agent beyond the main session; finished = no longer busy or waiting. The 6 gates weigh equally.",
		"A verifying goal shows at least 90%; a done goal 100%; a merged group averages its members.",
	} {
		b.WriteString(g(l) + "\n")
	}
	return b.String()
}
