package stocks

import (
	"strings"
	"sync"
	"unicode"
)

var stopwords = map[string]bool{"a": true, "an": true, "the": true, "of": true, "to": true, "on": true, "in": true,
	"for": true, "and": true, "with": true, "my": true, "at": true, "by": true, "is": true}

// symbol is a ticker symbol made from a name: the initials of its words, or one word squeezed to its
// consonants, with a number in it kept at the end.
func symbol(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var letters []string
	digits := ""
	for _, w := range words {
		switch {
		case strings.IndexFunc(w, func(r rune) bool { return !unicode.IsDigit(r) }) < 0:
			if digits == "" {
				digits = w
			}
		case !stopwords[strings.ToLower(w)]:
			letters = append(letters, w)
		}
	}
	if len(letters) == 0 {
		for _, w := range words {
			if strings.ContainsFunc(w, unicode.IsLetter) {
				letters = append(letters, w)
			}
		}
	}
	var out []rune
	switch {
	case len(letters) == 1:
		w := []rune(letters[0])
		if len(w) <= 4 {
			out = w
			break
		}
		out = append(out, w[0])
		for _, r := range w[1:] {
			if len(out) < 4 && !strings.ContainsRune("aeiouyAEIOUY", r) && unicode.IsLetter(r) {
				out = append(out, r)
			}
		}
	default:
		for _, w := range letters {
			if len(out) < 4 {
				out = append(out, []rune(w)[0])
			}
		}
	}
	if len(digits) > 2 {
		digits = digits[len(digits)-2:]
	}
	s := strings.ToUpper(string(out)) + digits
	if s == "" {
		return "N/A"
	}
	return s
}

var struck sync.Map

// strike is r with a stroke through it, shared across cells and frames.
func strike(r rune) string {
	if s, ok := struck.Load(r); ok {
		return s.(string)
	}
	s, _ := struck.LoadOrStore(r, string(r)+"̶")
	return s.(string)
}
