package codex

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/drift"
)

// The fixtures are real rollouts cut to the episode, with texts and commands replaced by tokens that keep which ones were equal.
func TestRealRolloutsShowOnlyTheAgentsThatWentNowhere(t *testing.T) {
	cases := []struct {
		file string
		want []string
	}{
		{"sandbox-loop", []string{drift.Looping}},
		{"lint-iteration", nil},
		{"search-no-match", nil},
	}
	for _, c := range cases {
		var got []string
		for _, s := range Episodes(filepath.Join("testdata", "drift", c.file+".jsonl")) {
			got = append(got, s.Kind)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: episodes %v, want %v", c.file, got, c.want)
		}
	}
}
