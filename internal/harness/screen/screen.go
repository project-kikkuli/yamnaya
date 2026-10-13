// Package screen holds line helpers for reading a terminal screen.
package screen

import "strings"

// Lines splits a screen into trimmed, non-blank lines.
func Lines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// FooterNear reports whether one of footers sits within the last n lines, which marks a dialog as live.
func FooterNear(lines []string, n int, footers ...string) bool {
	for _, l := range lines[max(0, len(lines)-n):] {
		for _, f := range footers {
			if strings.Contains(l, f) {
				return true
			}
		}
	}
	return false
}

func Has(lines []string, s string) bool {
	return Index(lines, func(l string) bool { return strings.Contains(l, s) }) >= 0
}

// After is the line following the line equal to s.
func After(lines []string, s string) string {
	i := Index(lines, func(l string) bool { return l == s })
	if i < 0 || i+1 >= len(lines) {
		return ""
	}
	return lines[i+1]
}

// LastPrefixed is the last line starting with prefix, without the prefix.
func LastPrefixed(lines []string, prefix string) string {
	i := LastIndex(lines, func(l string) bool { return strings.HasPrefix(l, prefix) })
	if i < 0 {
		return ""
	}
	return strings.TrimPrefix(lines[i], prefix)
}

// IsRule reports a line drawn only with r, at least ten wide.
func IsRule(l string, r rune) bool {
	return len([]rune(l)) >= 10 && strings.Trim(l, string(r)) == ""
}

func Index(lines []string, match func(string) bool) int {
	for i, l := range lines {
		if match(l) {
			return i
		}
	}
	return -1
}

func LastIndex(lines []string, match func(string) bool) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if match(lines[i]) {
			return i
		}
	}
	return -1
}
