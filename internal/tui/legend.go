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
		return "   " + strings.ReplaceAll(ansi.Wrap(s, max(20, width-5), " "), "\n", "\n     ")
	}
	var b strings.Builder
	b.WriteString(reflow(Help, width))
	para := func(s string) { b.WriteString(reflow(" "+s, width)) }
	b.WriteString("\n")
	para("Mountains (each repository is a range, each goal or session you started a peak; A hides them)")
	for _, l := range []string{
		"λ agent at work, climbing the west face; pale yellow at night, dark blue by day. The leader stands higher the more is done",
		pal.red.Render("λ") + " agent stopped and waiting on you (always red)",
		"▴ N resting: idle teammates that stay on the mountain",
		"▲ pull request camp on the east face; higher = more of its 6 gates passed (branch, commits, PR, CI, threads, merged)",
		pal.green.Render("▲") + " camp whose CI passed      " + pal.red.Render("▲") + " camp whose CI fails",
		gold.Render("⚑") + " merged pull request (several collapse to \"N merged\")",
		pal.red.Render("⚑") + " on a summit: goal done, or every pull request merged",
		pal.red.Render("│ ✸") + " red flare over a peak: something waits on you; it blinks and the name turns red",
		"name NN%: how far along it is (see Percentages)",
	} {
		b.WriteString(g(l) + "\n")
	}
	b.WriteString("\n")
	para("Weather (per peak; the sky above a range takes the worst of its peaks)")
	for _, l := range []string{
		"Clear: a goal that is moving or verifying; a session with an agent at work or waiting, or with both a teammate and a pull request. It outranks Summit while an agent is busy",
		"Clouds: a goal that is quiet (nudged, or nothing moved for 30 minutes) or adrift; a session with no working agent, or with pull requests but no teammate",
		"Storm: dark cloud, rain " + pal.dim.Render("╱") + " and lightning: a stuck goal, or a pull request with failing CI",
		"Eruption: lava, smoke and embers, no climbers: the manager's session died and has not come back",
		"Summit: glow and halo: goal done, or a session whose pull requests are all merged with no one on the mountain; a group only when all its members are",
		"Sky tint: where peaks overlap, Eruption beats Storm beats Summit beats Clouds",
	} {
		b.WriteString(g(l) + "\n")
	}
	b.WriteString("\n")
	para("Decoration only, no meaning: sun, moon and stars (your clock), lake, pines, fog, wisps, drifting clouds, far ridges, rim light.")
	para("Colors never mark a goal or repository: only weather, camp state and time of day.")
	b.WriteString("\n")
	para("Percentages")
	for _, l := range []string{
		"done % = (finished crew + gates passed) / (crew + 6 x pull requests and pushed branches not closed)",
		"Crew is every agent beyond the main session; finished = no longer busy or waiting. The 6 gates weigh equally.",
		"A verifying goal shows at least 90%; a done goal 100%; a merged group averages its members.",
	} {
		b.WriteString(g(l) + "\n")
	}
	return b.String()
}

const keyColumn = 16

// reflow wraps the help prose and key list to width: paragraphs hang at one space, key entries under their text.
func reflow(text string, width int) string {
	var out []string
	var para []string
	flush := func() {
		if len(para) == 0 {
			return
		}
		for _, l := range strings.Split(ansi.Wrap(strings.Join(para, " "), max(20, width-2), " "), "\n") {
			out = append(out, " "+l)
		}
		para = nil
	}
	entry := func(key string, body []string) {
		if len(body) == 0 {
			out = append(out, "   "+key)
			return
		}
		lines := strings.Split(ansi.Wrap(strings.Join(body, " "), max(20, width-keyColumn-1), " "), "\n")
		first := "   " + key
		if ansi.StringWidth(first) >= keyColumn {
			out = append(out, first)
			first = ""
		}
		for i, l := range lines {
			head := strings.Repeat(" ", keyColumn)
			if i == 0 && first != "" {
				head = first + strings.Repeat(" ", keyColumn-ansi.StringWidth(first))
			}
			out = append(out, head+l)
		}
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		indent := len(l) - len(strings.TrimLeft(l, " "))
		switch {
		case strings.TrimSpace(l) == "":
			flush()
			out = append(out, "")
		case indent == 1:
			if len(para) > 0 && strings.HasSuffix(para[len(para)-1], ".") {
				flush()
			}
			para = append(para, strings.TrimSpace(l))
		case indent == 3:
			flush()
			body := strings.TrimSpace(l)
			key, rest, gap := strings.Cut(body, "  ")
			var parts []string
			if gap {
				parts = append(parts, strings.TrimSpace(rest))
			}
			j := i + 1
			for ; j < len(lines) && len(lines[j])-len(strings.TrimLeft(lines[j], " ")) >= keyColumn; j++ {
				parts = append(parts, strings.TrimSpace(lines[j]))
			}
			if !gap && len(parts) == 0 {
				out = append(out, l)
				break
			}
			entry(key, parts)
			i = j - 1
		default:
			flush()
			out = append(out, l)
		}
	}
	flush()
	return strings.Join(out, "\n") + "\n"
}
