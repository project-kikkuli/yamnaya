package health

import (
	"regexp"
	"strings"
)

// heading opens a block of questions for the owner, after markdown markers are stripped from the line.
var heading = regexp.MustCompile(`(?i)^(still |remaining )?(` +
	`open for you|for you|open questions?|open decisions?|open items?|` +
	`decisions? (for|from|needed from) you|questions? for you|` +
	`(one|two|three|four|five|\d+) (\w+ )?(things?|questions?|decisions?|answers?) (for you|only you can answer|still open)|(still )?waiting on you|` +
	`questions? only you can answer|(what )?i need from you|needs? (you|your (call|input|decision|answer|approval|explicit approval))|your call` +
	`)\b`)

var (
	listItem = regexp.MustCompile(`^\s*(\d+[.)]|[-*•])\s+`)
	empty    = regexp.MustCompile(`(?i)^[\s(:.\-–—]*(\(0\)|0\b|nothing|none|no (decision|question|open|action)|n/a|nil\b)`)
	markers  = strings.NewReplacer("#", "", "*", "", "_", "", "`", "", ">", "")
	emphasis = strings.NewReplacer("**", "", "__", "")
)

// Asks reports whether text carries a non-empty block of questions for the owner, such as a numbered "Open for you" list.
func Asks(text string) bool { return Ask(text) != "" }

// Ask is the first question of text's first non-empty block of questions for the owner, "" when it has none.
func Ask(text string) string {
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
			return plain(line)
		}
		for _, next := range lines[i+1 : min(len(lines), i+4)] {
			if strings.TrimSpace(next) == "" {
				continue
			}
			if (listItem.MatchString(next) || strings.HasSuffix(strings.TrimSpace(next), "?")) &&
				!empty.MatchString(markers.Replace(listItem.ReplaceAllString(next, ""))) {
				return plain(next)
			}
			break
		}
	}
	return ""
}

func plain(line string) string {
	return strings.TrimSpace(emphasis.Replace(strings.TrimLeft(strings.TrimSpace(line), "#> ")))
}

// FollowUp is what asked becomes after reply: reply alone when it asks the owner, else reply appended to the replies since one did.
func FollowUp(asked, reply string) string {
	if Asks(reply) {
		return reply
	}
	if asked == "" {
		return ""
	}
	return asked + "\n\n" + reply
}

var (
	implement = regexp.MustCompile(`(?i)please go implement this`)
	request   = regexp.MustCompile(`(?i)\b(please|can you|could you|would you|needs? (you|your)|waiting (on|for) (you|your)|ready for|awaiting|for your|(once|when|if) you)\b[^.\n]{0,60}?\b(approv|review|merg|decid|decision|sign[- ]?off|go[- ]ahead|say so|ok\b|confirm|answer)`)
	question  = regexp.MustCompile(`\?($|[\s)"'*_` + "`" + `.:])`)
	next      = regexp.MustCompile(`(?i)^next\s*:\s*`)
	nextAsk   = regexp.MustCompile(`(?i)\b(you|your|answer|approve|review|merge|decide|confirm|reply|pick|choose|say)\b`)
)

// Addressed is the line of text, an agent's last reply, that asks the owner for something, "" when none does. It checks, in order:
// a block of questions for the owner, "PLEASE GO IMPLEMENT THIS", a request to approve, review, merge or decide, then in the final
// paragraph a question mark, a "Next:" line that asks the owner, or a numbered "Next:" list.
func Addressed(text string) string {
	if line := Ask(text); line != "" {
		return line
	}
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		if implement.MatchString(line) || request.MatchString(markers.Replace(line)) {
			return plain(line)
		}
	}
	final := finalParagraph(lines)
	for i, line := range final {
		head := strings.TrimSpace(markers.Replace(line))
		if question.MatchString(head) {
			return plain(line)
		}
		if loc := next.FindStringIndex(head); loc != nil {
			rest := head[loc[1]:]
			if rest == "" && i+1 < len(final) && listItem.MatchString(final[i+1]) {
				return plain(final[i+1])
			}
			if nextAsk.MatchString(rest) {
				return plain(line)
			}
		}
	}
	return ""
}

// finalParagraph is the last run of non-blank lines outside code fences, joined to a list that a heading line opens.
func finalParagraph(lines []string) []string {
	var paras [][]string
	var cur []string
	fenced := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		if strings.TrimSpace(line) == "" {
			if len(cur) > 0 {
				paras, cur = append(paras, cur), nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		paras = append(paras, cur)
	}
	if len(paras) == 0 {
		return nil
	}
	last := paras[len(paras)-1]
	if n := len(paras); n > 1 && listItem.MatchString(last[0]) {
		prev := paras[n-2]
		return append(append([]string{}, prev[len(prev)-1]), last...)
	}
	return last
}
