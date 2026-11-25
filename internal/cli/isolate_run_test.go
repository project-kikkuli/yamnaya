package cli

import "testing"

func TestAnIsolatedCopyRunsTheSubtestsTheRunPatternAsksFor(t *testing.T) {
	for _, c := range []struct{ run, want string }{
		{"", "^TestX$"},
		{"TestX", "^TestX$"},
		{"^TestX$/one", "^TestX$/one"},
		{"Test/one/deep", "^TestX$/one/deep"},
		{"TestX/a[/]b", "^TestX$/a[/]b"},
		{"TestX/(a|b)", "^TestX$/(a|b)"},
		{"TestX/one|TestY/two", "^TestX$/one"},
		{"TestX/one|TestX/two", "^TestX$/one|^TestX$/two"},
		{"TestX/one|Test", "^TestX$"},
	} {
		if got := isolatedRun("TestX", c.run); got != c.want {
			t.Errorf("-run %q: copy runs %q, want %q", c.run, got, c.want)
		}
	}
}
