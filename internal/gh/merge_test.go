package gh

import (
	"strings"
	"testing"
)

func TestMergeBindsToTheHeadTheOwnerSawAndTheRepositorysMethod(t *testing.T) {
	var ran []string
	s := &Source{Exec: func(args ...string) ([]byte, error) {
		ran = append(ran, strings.Join(args, " "))
		if args[0] == "repo" {
			return []byte("SQUASH\n"), nil
		}
		return nil, nil
	}}
	if err := s.Merge("https://github.com/acme/repo/pull/5", "abc"); err != nil {
		t.Fatal(err)
	}
	if want := "pr merge https://github.com/acme/repo/pull/5 --squash --match-head-commit abc"; len(ran) != 2 || ran[1] != want {
		t.Fatalf("ran %q, want %q", ran, want)
	}
	if err := s.Merge("not a link", "abc"); err == nil {
		t.Fatal("merged a non-link")
	}
}
