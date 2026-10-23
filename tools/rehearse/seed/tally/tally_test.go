package tally

import (
	"reflect"
	"testing"
)

func TestTop(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		n    int
		want []Count
	}{
		{"empty", "", 3, []Count{}},
		{"ties alphabetical", "b a b a c", 2, []Count{{"a", 2}, {"b", 2}}},
		{"case and punctuation", "Go, go! GO.", 5, []Count{{"go", 3}}},
		{"all", "x y", -1, []Count{{"x", 1}, {"y", 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Top(tc.text, tc.n); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Top(%q, %d) = %v, want %v", tc.text, tc.n, got, tc.want)
			}
		})
	}
}
