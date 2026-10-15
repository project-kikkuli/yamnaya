package health

import (
	"regexp"
	"strings"
)

// heading opens a block of questions for the owner, after markdown markers are stripped from the line.
var heading = regexp.MustCompile(`(?i)^(still |remaining )?(` +
	`open for you|for you|open questions?|open decisions?|open items?|` +
	`decisions? (for|from|needed from) you|questions? for you|` +
	`(one|two|three|four|five|\d+) (open )?(things|questions|decisions|answers) (for you|only you can answer|still open)|` +
	`questions? only you can answer|(what )?i need from you|needs? (you|your (call|input|decision|answer|approval|explicit approval))|your call` +
	`)\b`)

var (
	listItem = regexp.MustCompile(`^\s*(\d+[.)]|[-*•])\s+`)
	empty    = regexp.MustCompile(`(?i)^[\s(:.\-–—]*(\(0\)|0\b|nothing|none|no (decision|question|open|action)|n/a|nil\b)`)
	markers  = strings.NewReplacer("#", "", "*", "", "_", "", "`", "", ">", "")
)

// Asks reports whether text carries a non-empty block of questions for the owner, such as a numbered "Open for you" list.
func Asks(text string) bool {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		head := strings.TrimSpace(markers.Replace(line))
		loc := heading.FindStringIndex(head)
		if loc == nil {
			continue
		}
		rest := strings.TrimSpace(head[loc[1]:])
		if empty.MatchString(rest) {
			continue
		}
		if strings.Contains(rest, "?") {
			return true
		}
		for _, next := range lines[i+1 : min(len(lines), i+4)] {
			if strings.TrimSpace(next) == "" {
				continue
			}
			if (listItem.MatchString(next) || strings.HasSuffix(strings.TrimSpace(next), "?")) &&
				!empty.MatchString(markers.Replace(listItem.ReplaceAllString(next, ""))) {
				return true
			}
			break
		}
	}
	return false
}
