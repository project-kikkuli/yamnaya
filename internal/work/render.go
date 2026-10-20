package work

import (
	"fmt"
	"io"
	"sort"
)

func Render(out io.Writer, w Scope) {
	fmt.Fprintf(out, "%s\nwork %s · revision %d · manager %s\n\n", w.Name, w.ID, w.Revision, w.Manager)
	if !w.BriefCurrent() {
		fmt.Fprintln(out, "Assessment needs refreshing against current context or evidence.")
	}
	for _, field := range [][2]string{{"Assessment", w.Brief.Assessment}, {"Changed", w.Brief.Changed}, {"Purpose", w.Purpose}, {"Approach", w.Brief.Rationale}, {"Uncertainty", w.Brief.Uncertainty}, {"Next action", w.Brief.Next}, {"Authority", w.Authority}, {"Context", w.Context.Text}} {
		if field[1] != "" {
			fmt.Fprintf(out, "\n%s\n%s\n", field[0], field[1])
		}
	}
	for id, meaning := range w.Related {
		fmt.Fprintf(out, "\nRelated: %s · %s\n", id, meaning)
	}
	fmt.Fprintf(out, "\nContext revision %d\n", w.Context.Revision)
	sessions := make([]string, 0, len(w.Bindings))
	for session := range w.Bindings {
		sessions = append(sessions, session)
	}
	sort.Strings(sessions)
	for _, session := range sessions {
		fmt.Fprintf(out, "  %s incorporated r%d\n", session, w.Bindings[session])
	}
	ids := make([]string, 0, len(w.Reviews))
	for id := range w.Reviews {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := w.Reviews[id]
		fmt.Fprintf(out, "\n%s · %s\nreview %s · source %s\nrequester: %s\nquestion: %s\nrecommendation: %s\nchanged: %s\nfeedback: %s\njudgment: %s\n", r.Title, r.State, r.ID, r.Version, r.Requester, r.Question, r.Recommendation, r.Changed, r.Feedback, r.Judgment)
	}
	for _, source := range w.Sources {
		fmt.Fprintf(out, "\nCoverage: %s · %s\n", source.URL, source.Coverage)
	}
	for _, issue := range w.CoverageErrors {
		fmt.Fprintf(out, "Unavailable: %s\n", issue)
	}
}
