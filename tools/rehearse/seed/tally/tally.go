// Package tally counts words in text.
package tally

import (
	"sort"
	"strings"
	"unicode"
)

// Count is one word and how often it appears.
type Count struct {
	Word string
	N    int
}

// Words splits text into lowercase words on anything that is not a letter or digit.
func Words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// Top returns the n most frequent words, most frequent first, ties broken alphabetically.
func Top(text string, n int) []Count {
	seen := map[string]int{}
	for _, w := range Words(text) {
		seen[w]++
	}
	out := make([]Count, 0, len(seen))
	for w, c := range seen {
		out = append(out, Count{w, c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Word < out[j].Word
	})
	if n >= 0 && n < len(out) {
		out = out[:n]
	}
	return out
}
