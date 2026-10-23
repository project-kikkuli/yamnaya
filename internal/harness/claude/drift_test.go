package claude

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/drift"
)

// The fixtures are real transcripts cut to the episode and reduced to what drift reads: their texts and commands are replaced by
// tokens that keep which ones were equal, and generic harness errors are kept as written.
func TestRealTranscriptsShowOnlyTheAgentsThatWentNowhere(t *testing.T) {
	cases := []struct {
		file string
		want []string
	}{
		{"commit-timeouts", []string{drift.Looping}},
		{"frozen-browser", []string{drift.Looping}},
		{"compaction-loop", []string{drift.Compacting}},
		{"ci-poll", nil},
		{"rate-limited", nil},
		{"session-limit", nil},
		{"burst", nil},
		{"spaced-compactions", nil},
		{"blocked-but-moving", nil},
		{"parallel-batch", nil},
		{"recovered-retry", nil},
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

func TestDriftResumesFromTheScanCache(t *testing.T) {
	src := filepath.Join("testdata", "drift", "commit-timeouts.jsonl")
	f, err := os.Open(src)
	must(t, err)
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	must(t, f.Close())
	half := len(lines) / 2
	w := newWorld(t, lines[:half]...)
	cache := memCache{}
	if s := w.session(t, cache); len(s.Drift) != 0 {
		t.Fatalf("first half shows %v, want nothing yet", s.Drift)
	}
	out, err := os.OpenFile(w.transcript, os.O_APPEND|os.O_WRONLY, 0)
	must(t, err)
	for _, l := range lines[half:] {
		_, err = out.WriteString(l + "\n")
		must(t, err)
	}
	must(t, out.Close())
	resumed := (&Adapter{Dir: w.dir, Cache: cache}).scan(w.transcript)
	whole := Episodes(src)
	if len(whole) == 0 || len(resumed.Facts.Drift) != 1 || resumed.Facts.Drift[0].Since != whole[0].Since {
		t.Fatalf("resumed signs %v, want the episode a full replay finds, %v", resumed.Facts.Drift, whole)
	}
}
