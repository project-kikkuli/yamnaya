package gh

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

const (
	mergePR   = "https://github.com/acme/app/pull/7"
	mergeRepo = "https://github.com/acme/app"
	mergeSHA  = "a1b2c3d4e5"
	parentSHA = "b2c3d4e5f6"
	fixSHA    = "c3d4e5f607"
	headSHA   = "d4e5f60718"
	lintName  = "lint"
	unitName  = "lint / unit"
	wfChecks  = "checks"
)

type jobRow struct{ workflow, name, conclusion string }

func (r jobRow) json() string {
	if r.workflow == "" {
		return fmt.Sprintf(`{"context":%q,"state":%q}`, r.name, r.conclusion)
	}
	conclusion, status := `"`+r.conclusion+`"`, "COMPLETED"
	if r.conclusion == "IN_PROGRESS" {
		conclusion, status = "null", r.conclusion
	}
	return fmt.Sprintf(`{"name":%q,"status":%q,"conclusion":%s,"checkSuite":{"app":{"slug":"github-actions"},"workflowRun":{"workflow":{"name":%q}}}}`, r.name, status, conclusion, r.workflow)
}

// mergeHub answers MergeJobs' queries from commits on a first-parent line, newest first, and counts what they read.
type mergeHub struct {
	line    []string
	rollup  map[string]string
	status  string
	noBase  bool
	noRuns  []string
	checks  map[string][]jobRow
	read    []string
	pageErr error
}

func (h *mergeHub) run(q string, vars map[string]string) ([]byte, error) {
	if q == mergeBaseQuery {
		var nodes []string
		for i, oid := range h.line {
			parents := fmt.Sprintf(`[{"oid":%q}]`, parentSHA)
			if i+1 < len(h.line) {
				parents = fmt.Sprintf(`[{"oid":%q}]`, h.line[i+1])
			} else if slices.Contains(h.line, parentSHA) {
				parents = `[]`
			}
			state := "null"
			if s, ok := h.rollup[oid]; ok {
				runs := 1
				if slices.Contains(h.noRuns, oid) {
					runs = 0
				}
				state = fmt.Sprintf(`{"state":%q,"contexts":{"checkRunCount":%d}}`, s, runs)
			}
			nodes = append(nodes, fmt.Sprintf(`{"oid":%q,"parents":{"nodes":%s},"statusCheckRollup":%s}`, oid, parents, state))
		}
		head := ""
		if len(h.line) > 0 {
			head = h.line[0]
		}
		base := fmt.Sprintf(`{"compare":{"status":%q},"target":{"oid":%q,"history":{"nodes":[%s]}}}`, h.status, head, strings.Join(nodes, ","))
		if h.noBase {
			base = "null"
		}
		return fmt.Appendf(nil, `{"data":{"resource":{"repository":{"url":%q},"mergeCommit":{"parents":{"nodes":[{"oid":%q}]}},"baseRef":%s}}}`, mergeRepo, parentSHA, base), nil
	}
	if h.pageErr != nil {
		return nil, h.pageErr
	}
	oid := strings.TrimPrefix(vars["url"], mergeRepo+"/commit/")
	rows := h.checks[oid]
	if vars["after"] == "" {
		h.read = append(h.read, oid)
	}
	// Each commit's rollup spans two pages, as real ones past 100 checks do.
	half := len(rows) / 2
	page, next, cursor := rows[:half], "true", "MTAw"
	if vars["after"] != "" {
		page, next, cursor = rows[half:], "false", ""
	}
	var parts []string
	for _, r := range page {
		parts = append(parts, r.json())
	}
	return fmt.Appendf(nil, `{"data":{"resource":{"statusCheckRollup":{"contexts":{"pageInfo":{"hasNextPage":%s,"endCursor":%q},"nodes":[%s]}}}}}`, next, cursor, strings.Join(parts, ",")), nil
}

func redOnMerge() []jobRow {
	return []jobRow{{wfChecks, "build-image", "SKIPPED"}, {wfChecks, unitName, "FAILURE"}, {"check-web", "build-image", "SUCCESS"}, {wfChecks, lintName, "FAILURE"}}
}

func TestMergeJobsReadsEachRedJobAcrossCommits(t *testing.T) {
	pre, tool := wfChecks+": "+lintName, wfChecks+": "+unitName
	greenBoth := []jobRow{{wfChecks, unitName, "SUCCESS"}, {wfChecks, lintName, "SUCCESS"}}
	redBoth := []jobRow{{wfChecks, unitName, "FAILURE"}, {wfChecks, lintName, "FAILURE"}}
	for _, tc := range []struct {
		name string
		hub  mergeHub
		want []core.MergeJob
		// reads is the commits read, in order; nil skips the check.
		reads []string
	}{
		{name: "red on merge and parent, green at main's head, later than a head with no checks and one still pending",
			hub: mergeHub{line: []string{"newest", "running", headSHA, fixSHA, "older", mergeSHA}, status: "BEHIND",
				rollup: map[string]string{"running": "PENDING", headSHA: "SUCCESS", fixSHA: "SUCCESS", "older": "FAILURE"},
				checks: map[string][]jobRow{mergeSHA: redOnMerge(), headSHA: greenBoth, fixSHA: greenBoth, parentSHA: redBoth}},
			want:  []core.MergeJob{{Name: pre, Later: "pass", LaterAt: headSHA}, {Name: tool, Later: "pass", LaterAt: headSHA}},
			reads: []string{mergeSHA, headSHA}},
		{name: "still red later, red on the parent",
			hub: mergeHub{line: []string{headSHA, mergeSHA}, status: "BEHIND", rollup: map[string]string{headSHA: "FAILURE"},
				checks: map[string][]jobRow{mergeSHA: redOnMerge(), headSHA: redBoth, parentSHA: redBoth}},
			want: []core.MergeJob{{Name: pre, Later: "fail", LaterAt: headSHA, Parent: "fail", ParentAt: parentSHA}, {Name: tool, Later: "fail", LaterAt: headSHA, Parent: "fail", ParentAt: parentSHA}}},
		{name: "new with the merge and still red",
			hub: mergeHub{line: []string{headSHA, mergeSHA}, status: "BEHIND", rollup: map[string]string{headSHA: "FAILURE"},
				checks: map[string][]jobRow{mergeSHA: redOnMerge(), headSHA: redBoth, parentSHA: greenBoth}},
			want: []core.MergeJob{{Name: pre, Later: "fail", LaterAt: headSHA, Parent: "pass", ParentAt: parentSHA}, {Name: tool, Later: "fail", LaterAt: headSHA, Parent: "pass", ParentAt: parentSHA}}},
		{name: "no later commit",
			hub:   mergeHub{line: []string{mergeSHA}, status: "IDENTICAL", checks: map[string][]jobRow{mergeSHA: redOnMerge(), parentSHA: redBoth}},
			want:  []core.MergeJob{{Name: pre, Parent: "fail", ParentAt: parentSHA}, {Name: tool, Parent: "fail", ParentAt: parentSHA}},
			reads: []string{mergeSHA, parentSHA}},
		{name: "the same job name in another workflow says nothing, nor does a job that did not run",
			hub: mergeHub{line: []string{headSHA, mergeSHA}, status: "BEHIND", rollup: map[string]string{headSHA: "SUCCESS"},
				checks: map[string][]jobRow{mergeSHA: redOnMerge(), headSHA: {{"check-web", lintName, "SUCCESS"}, {"check-web", unitName, "SUCCESS"}}, parentSHA: greenBoth}},
			want: []core.MergeJob{{Name: pre, Parent: "pass", ParentAt: parentSHA}, {Name: tool, Parent: "pass", ParentAt: parentSHA}}},
		{name: "a finished red outweighs a newer run in progress, and green outweighs both",
			hub: mergeHub{line: []string{"running", headSHA, fixSHA, mergeSHA}, status: "BEHIND", rollup: map[string]string{"running": "PENDING", headSHA: "FAILURE", fixSHA: "FAILURE"},
				checks: map[string][]jobRow{mergeSHA: redOnMerge(), "running": {{wfChecks, unitName, "IN_PROGRESS"}, {wfChecks, lintName, "IN_PROGRESS"}},
					headSHA: {{wfChecks, unitName, "FAILURE"}, {wfChecks, lintName, "FAILURE"}}, fixSHA: {{wfChecks, unitName, "SUCCESS"}, {wfChecks, lintName, "FAILURE"}}, parentSHA: greenBoth}},
			want: []core.MergeJob{{Name: pre, Later: "fail", LaterAt: headSHA, Parent: "pass", ParentAt: parentSHA}, {Name: tool, Later: "pass", LaterAt: fixSHA, Parent: "pass", ParentAt: parentSHA}}},
		{name: "a finished red later outweighs a newer run in progress on a red parent too",
			hub: mergeHub{line: []string{"running", headSHA, mergeSHA}, status: "BEHIND", rollup: map[string]string{"running": "PENDING", headSHA: "FAILURE"},
				checks: map[string][]jobRow{mergeSHA: {{wfChecks, lintName, "FAILURE"}, {wfChecks, unitName, "SUCCESS"}}, "running": {{wfChecks, lintName, "IN_PROGRESS"}, {wfChecks, unitName, "SUCCESS"}}, headSHA: redBoth, parentSHA: redBoth}},
			want: []core.MergeJob{{Name: pre, Later: "fail", LaterAt: headSHA, Parent: "fail", ParentAt: parentSHA}}},
		{name: "a red parent does not stop later commits being read",
			hub: mergeHub{line: []string{headSHA, mergeSHA}, status: "BEHIND", rollup: map[string]string{headSHA: "SUCCESS"},
				checks: map[string][]jobRow{mergeSHA: redOnMerge(), headSHA: greenBoth, parentSHA: redBoth}},
			want:  []core.MergeJob{{Name: pre, Later: "pass", LaterAt: headSHA}, {Name: tool, Later: "pass", LaterAt: headSHA}},
			reads: []string{mergeSHA, headSHA}},
		{name: "a green commit below the merge does not clear it",
			hub: mergeHub{line: []string{headSHA, mergeSHA, parentSHA, "older"}, status: "BEHIND", rollup: map[string]string{headSHA: "FAILURE", parentSHA: "SUCCESS", "older": "SUCCESS"},
				checks: map[string][]jobRow{mergeSHA: redOnMerge(), headSHA: redBoth, parentSHA: greenBoth, "older": greenBoth}},
			want:  []core.MergeJob{{Name: pre, Later: "fail", LaterAt: headSHA, Parent: "pass", ParentAt: parentSHA}, {Name: tool, Later: "fail", LaterAt: headSHA, Parent: "pass", ParentAt: parentSHA}},
			reads: []string{mergeSHA, headSHA, parentSHA}},
		{name: "a job cancelled on the merge is still red there",
			hub: mergeHub{line: []string{mergeSHA}, status: "IDENTICAL",
				checks: map[string][]jobRow{mergeSHA: {{wfChecks, lintName, "CANCELLED"}, {wfChecks, unitName, "SUCCESS"}}, parentSHA: greenBoth}},
			want: []core.MergeJob{{Name: pre, Parent: "pass", ParentAt: parentSHA}}},
		{name: "timed out and startup failure read as red on later commits and the parent",
			hub: mergeHub{line: []string{headSHA, mergeSHA}, status: "BEHIND", rollup: map[string]string{headSHA: "FAILURE"},
				checks: map[string][]jobRow{mergeSHA: redOnMerge(), headSHA: {{wfChecks, unitName, "TIMED_OUT"}, {wfChecks, lintName, "STARTUP_FAILURE"}}, parentSHA: {{wfChecks, unitName, "STARTUP_FAILURE"}, {wfChecks, lintName, "TIMED_OUT"}}}},
			want: []core.MergeJob{{Name: pre, Later: "fail", LaterAt: headSHA, Parent: "fail", ParentAt: parentSHA}, {Name: tool, Later: "fail", LaterAt: headSHA, Parent: "fail", ParentAt: parentSHA}}},
		{name: "commits with no check runs use up no read",
			hub: mergeHub{line: []string{"docs1", "docs2", "docs3", "c1", "c2", "c3", "c4", "c5", "c6", headSHA, mergeSHA}, status: "BEHIND", noRuns: []string{"docs1", "docs2", "docs3"},
				rollup: map[string]string{"docs1": "SUCCESS", "docs2": "SUCCESS", "docs3": "SUCCESS", "c1": "FAILURE", "c2": "FAILURE", "c3": "FAILURE", "c4": "FAILURE", "c5": "FAILURE", "c6": "FAILURE", headSHA: "SUCCESS"},
				checks: map[string][]jobRow{mergeSHA: append(redOnMerge(), jobRow{"", "deploy/app/prod", "SUCCESS"}), headSHA: greenBoth}},
			want:  []core.MergeJob{{Name: pre, Later: "pass", LaterAt: headSHA}, {Name: tool, Later: "pass", LaterAt: headSHA}},
			reads: []string{mergeSHA, headSHA}},
		{name: "a red status context still reads commits with no check runs",
			hub: mergeHub{line: []string{headSHA, mergeSHA}, status: "BEHIND", noRuns: []string{headSHA}, rollup: map[string]string{headSHA: "SUCCESS"},
				checks: map[string][]jobRow{mergeSHA: {{"", "deploy/app/prod", "FAILURE"}, {wfChecks, lintName, "SUCCESS"}}, headSHA: {{"", "deploy/app/prod", "SUCCESS"}, {"", "deploy/other", "SUCCESS"}}}},
			want: []core.MergeJob{{Name: "deploy/app/prod", Later: "pass", LaterAt: headSHA}}},
		{name: "only a run in progress later",
			hub: mergeHub{line: []string{headSHA, mergeSHA}, status: "BEHIND", rollup: map[string]string{headSHA: "PENDING"},
				checks: map[string][]jobRow{mergeSHA: redOnMerge(), headSHA: {{wfChecks, unitName, "IN_PROGRESS"}, {wfChecks, lintName, "IN_PROGRESS"}}, parentSHA: greenBoth}},
			want: []core.MergeJob{{Name: pre, Later: "pending", LaterAt: headSHA, Parent: "pass", ParentAt: parentSHA}, {Name: tool, Later: "pending", LaterAt: headSHA, Parent: "pass", ParentAt: parentSHA}}},
		{name: "base branch went elsewhere: its commits do not contain the merge",
			hub: mergeHub{line: []string{headSHA}, status: "DIVERGED", rollup: map[string]string{headSHA: "SUCCESS"},
				checks: map[string][]jobRow{mergeSHA: redOnMerge(), headSHA: greenBoth, parentSHA: greenBoth}},
			want:  []core.MergeJob{{Name: pre, Parent: "pass", ParentAt: parentSHA}, {Name: tool, Parent: "pass", ParentAt: parentSHA}},
			reads: []string{mergeSHA, parentSHA}},
		{name: "base branch deleted",
			hub:  mergeHub{noBase: true, checks: map[string][]jobRow{mergeSHA: redOnMerge(), parentSHA: redBoth}},
			want: []core.MergeJob{{Name: pre, Parent: "fail", ParentAt: parentSHA}, {Name: tool, Parent: "fail", ParentAt: parentSHA}}},
		{name: "a red status context matches by its context",
			hub: mergeHub{line: []string{headSHA, mergeSHA}, status: "BEHIND", rollup: map[string]string{headSHA: "SUCCESS"},
				checks: map[string][]jobRow{mergeSHA: {{"", "deploy/app/prod", "FAILURE"}, {wfChecks, lintName, "SUCCESS"}}, headSHA: {{"", "deploy/app/prod", "SUCCESS"}, {wfChecks, lintName, "SUCCESS"}}}},
			want: []core.MergeJob{{Name: "deploy/app/prod", Later: "pass", LaterAt: headSHA}}},
		{name: "a job run twice on the merge reads as its worse result",
			hub: mergeHub{line: []string{mergeSHA}, status: "IDENTICAL",
				checks: map[string][]jobRow{mergeSHA: {{wfChecks, lintName, "SUCCESS"}, {wfChecks, lintName, "FAILURE"}}, parentSHA: {{wfChecks, lintName, "SUCCESS"}, {wfChecks, lintName, "SUCCESS"}}}},
			want: []core.MergeJob{{Name: pre, Parent: "pass", ParentAt: parentSHA}}},
		{name: "nothing red any more",
			hub:   mergeHub{line: []string{mergeSHA}, status: "IDENTICAL", checks: map[string][]jobRow{mergeSHA: {{wfChecks, lintName, "SUCCESS"}, {wfChecks, unitName, "SUCCESS"}}}},
			reads: []string{mergeSHA}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.hub
			s := &Source{Batch: h.run}
			got, err := s.MergeJobs(mergePR, mergeSHA)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				a, _ := json.Marshal(got)
				b, _ := json.Marshal(tc.want)
				t.Fatalf("jobs\n got %s\nwant %s", a, b)
			}
			if tc.reads != nil && !slices.Equal(h.read, tc.reads) {
				t.Fatalf("read %q, want %q", h.read, tc.reads)
			}
		})
	}
}

func TestRunsThatNeitherPassedNorFinishedRedAreNoEvidence(t *testing.T) {
	pre := wfChecks + ": " + lintName
	merge := []jobRow{{wfChecks, lintName, "FAILURE"}, {wfChecks, unitName, "SUCCESS"}}
	with := func(conclusion string) []jobRow {
		return []jobRow{{wfChecks, lintName, conclusion}, {wfChecks, unitName, "SUCCESS"}}
	}
	for _, conclusion := range []string{"SKIPPED", "NEUTRAL", "CANCELLED", "STALE", "ACTION_REQUIRED"} {
		for _, tc := range []struct {
			name   string
			line   []string
			rollup map[string]string
			checks map[string][]jobRow
			want   core.MergeJob
		}{
			{name: "later, green parent: nothing clears it, so it stays the goal's",
				line: []string{headSHA, mergeSHA}, rollup: map[string]string{headSHA: "SUCCESS"},
				checks: map[string][]jobRow{headSHA: with(conclusion), parentSHA: with("SUCCESS")},
				want:   core.MergeJob{Name: pre, Parent: "pass", ParentAt: parentSHA}},
			{name: "later, red parent: nothing finished later, whatever the parent says",
				line: []string{headSHA, mergeSHA}, rollup: map[string]string{headSHA: "SUCCESS"},
				checks: map[string][]jobRow{headSHA: with(conclusion), parentSHA: with("FAILURE")},
				want:   core.MergeJob{Name: pre, Parent: "fail", ParentAt: parentSHA}},
			{name: "on the parent: not red before the merge",
				line: []string{headSHA, mergeSHA}, rollup: map[string]string{headSHA: "FAILURE"},
				checks: map[string][]jobRow{headSHA: with("FAILURE"), parentSHA: with(conclusion)},
				want:   core.MergeJob{Name: pre, Later: "fail", LaterAt: headSHA, ParentAt: parentSHA}},
			{name: "on a whole-green head: the walk goes on to the real success below it",
				line: []string{headSHA, fixSHA, mergeSHA}, rollup: map[string]string{headSHA: "SUCCESS", fixSHA: "SUCCESS"},
				checks: map[string][]jobRow{headSHA: with(conclusion), fixSHA: with("SUCCESS")},
				want:   core.MergeJob{Name: pre, Later: "pass", LaterAt: fixSHA}},
			{name: "below a newer run in progress: the job is still running, not red",
				line: []string{"running", headSHA, mergeSHA}, rollup: map[string]string{"running": "PENDING", headSHA: "FAILURE"},
				checks: map[string][]jobRow{"running": with("IN_PROGRESS"), headSHA: with(conclusion), parentSHA: with("SUCCESS")},
				want:   core.MergeJob{Name: pre, Later: "pending", LaterAt: "running", Parent: "pass", ParentAt: parentSHA}},
			{name: "beside a real red later: the red still reads",
				line: []string{headSHA, fixSHA, mergeSHA}, rollup: map[string]string{headSHA: "SUCCESS", fixSHA: "FAILURE"},
				checks: map[string][]jobRow{headSHA: with(conclusion), fixSHA: with("FAILURE"), parentSHA: with("SUCCESS")},
				want:   core.MergeJob{Name: pre, Later: "fail", LaterAt: fixSHA, Parent: "pass", ParentAt: parentSHA}},
		} {
			t.Run(conclusion+" "+tc.name, func(t *testing.T) {
				checks := map[string][]jobRow{mergeSHA: merge}
				for oid, rows := range tc.checks {
					checks[oid] = rows
				}
				h := &mergeHub{line: tc.line, status: "BEHIND", rollup: tc.rollup, checks: checks}
				got, err := (&Source{Batch: h.run}).MergeJobs(mergePR, mergeSHA)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(got, []core.MergeJob{tc.want}) {
					t.Fatalf("jobs %+v, want %+v", got, tc.want)
				}
			})
		}
	}
}

func TestMergeJobsReadsABoundedNumberOfLaterCommits(t *testing.T) {
	line := []string{}
	rollup := map[string]string{}
	checks := map[string][]jobRow{mergeSHA: {{wfChecks, lintName, "FAILURE"}, {wfChecks, unitName, "SUCCESS"}}, parentSHA: {{wfChecks, lintName, "SUCCESS"}, {wfChecks, unitName, "SUCCESS"}}}
	for i := range 100 {
		oid := fmt.Sprintf("c%03d", i)
		state := "FAILURE"
		if i%2 == 1 {
			state = "SUCCESS"
		}
		line, rollup[oid], checks[oid] = append(line, oid), state, []jobRow{{wfChecks, lintName, "FAILURE"}, {wfChecks, unitName, "SUCCESS"}}
	}
	h := &mergeHub{line: line, status: "BEHIND", rollup: rollup, checks: checks}
	s := &Source{Batch: h.run}
	got, err := s.MergeJobs(mergePR, mergeSHA)
	if err != nil {
		t.Fatal(err)
	}
	// Two rollup reads beyond the later commits: the merge and its parent.
	want := []string{mergeSHA, "c001", "c003", "c005", "c000", "c002", "c004", parentSHA}
	if !slices.Equal(h.read, want) {
		t.Fatalf("read %q, want %q", h.read, want)
	}
	if len(got) != 1 || got[0].Later != "fail" {
		t.Fatalf("jobs %+v", got)
	}
}

func TestMergeJobsCarriesGitHubErrors(t *testing.T) {
	h := &mergeHub{line: []string{mergeSHA}, status: "IDENTICAL", pageErr: errors.New("HTTP 502")}
	s := &Source{Batch: h.run}
	if _, err := s.MergeJobs(mergePR, mergeSHA); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("error %v, want GitHub's", err)
	}
}
