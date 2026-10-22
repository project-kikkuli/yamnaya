package cli

import (
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/drift"
	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/harness/codex"
	"github.com/project-kikkuli/yamnaya/internal/usage"
)

type episode struct {
	path string
	drift.Sign
}

// replayDrift replays every transcript and rollout on this machine changed within since, and returns each episode of an agent going
// nowhere, oldest first, with how many files it read.
func replayDrift(since time.Duration, paths []string) ([]episode, int) {
	srcs := usage.AllTranscripts(usage.Dirs())
	if len(paths) > 0 {
		srcs = nil
		for _, p := range paths {
			h := "claude"
			if strings.Contains(p, "/rollout-") {
				h = "codex"
			}
			srcs = append(srcs, usage.Transcript{Path: p, Harness: h, MTime: time.Now().UnixNano()})
		}
	}
	cut := time.Now().Add(-since).UnixNano()
	var mu sync.Mutex
	var out []episode
	n := 0
	jobs := make(chan usage.Transcript)
	var wg sync.WaitGroup
	for range max(1, runtime.NumCPU()-1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range jobs {
				replay := claude.Episodes
				if s.Harness == "codex" {
					replay = codex.Episodes
				}
				found := replay(s.Path)
				mu.Lock()
				for _, sign := range found {
					out = append(out, episode{s.Path, sign})
				}
				mu.Unlock()
			}
		}()
	}
	for _, s := range srcs {
		if since > 0 && s.MTime < cut {
			continue
		}
		n++
		jobs <- s
	}
	close(jobs)
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out, n
}

func renderDrift(w io.Writer, eps []episode, files int) {
	count := map[string]int{}
	for _, e := range eps {
		count[e.Kind]++
		fmt.Fprintf(w, "%s  %-10s %s\n  %s\n", e.Since.Local().Format("2006-01-02 15:04"), e.Kind, e.path, e.Evidence)
	}
	fmt.Fprintf(w, "\n%d transcripts read: %d looping, %d compacting\n", files, count[drift.Looping], count[drift.Compacting])
}

func healthDriftCmd() *cobra.Command {
	var since time.Duration
	cmd := &cobra.Command{
		Use:   "drift [transcript...]",
		Short: "Replay this machine's transcripts and list every time an agent was busy going nowhere",
		Long: `Replay every Claude Code transcript (subagents included) and Codex rollout on this machine through the checks the
board runs live, and list each episode: an agent failing the same way again and again, or its context compacted over and over
with no work between. Writes in another repository need the live session's worktree, so only the board shows those. Name transcript files to replay only those.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			eps, n := replayDrift(since, args)
			renderDrift(cmd.OutOrStdout(), eps, n)
			return nil
		},
	}
	cmd.Flags().DurationVar(&since, "since", 0, "only transcripts changed within this long, such as 168h; 0 reads all")
	return cmd
}
