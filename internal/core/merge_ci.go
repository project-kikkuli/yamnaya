package core

import (
	"fmt"
	"strings"
)

// Results of one job on one commit, as the rollup's check states read.
const (
	JobPass    = "pass"
	JobFail    = "fail"
	JobPending = "pending"
)

const ciFact = "ci on merge commit"

// MergeJob is one job red on a merge commit and what the base branch says of it. Empty results mean it did not run there, or was skipped, neutral, cancelled, stale or awaiting action.
type MergeJob struct {
	// Name is the workflow and job, or the status context: a job name alone repeats across workflows.
	Name string
	// Later is the job's best result on the commits after the merge: pass, else fail, else pending; LaterAt is where it read so.
	Later, LaterAt string
	// Parent is the job's result on the merge's first parent, ParentAt.
	Parent, ParentAt string
}

// mergeCI is the fact on a merged pull request's CI and its landing with the red jobs that are not the goal's taken out.
func (c *Core) mergeCI(url string, l Landing) (Fact, Landing) {
	ci := Fact{Check: ciFact, Subject: url, OK: true}
	total := l.Passed + l.Failed + l.Pending
	sha := shortSHA(l.MergeCommit)
	if l.Failed == 0 {
		switch {
		case l.Pending > 0:
			ci.Pending, ci.Detail = true, fmt.Sprintf("%d of %d checks still running on %s", l.Pending, total, sha)
		case l.Passed == 0:
			ci.Detail = "no checks ran on " + sha
		default:
			ci.Detail = fmt.Sprintf("%d checks passed on %s", l.Passed, sha)
		}
		return ci, l
	}
	red := fmt.Sprintf("%d of %d checks failed on %s", l.Failed, total, sha)
	jobs, err := c.Landings.MergeJobs(url, l.MergeCommit)
	if err != nil {
		ci.Pending, ci.Detail = true, red+"; whose they are is unread: "+err.Error()
		return ci, l
	}
	var green, mains, waiting, ours, unsettled []string
	for _, j := range jobs {
		switch {
		case j.Later == JobPass:
			green = append(green, fmt.Sprintf("%s (%s)", j.Name, shortSHA(j.LaterAt)))
		case j.Later == JobPending:
			waiting = append(waiting, fmt.Sprintf("%s (still running on %s)", j.Name, shortSHA(j.LaterAt)))
		case j.Later == JobFail && j.Parent == JobFail:
			mains = append(mains, fmt.Sprintf("%s (%s)", j.Name, shortSHA(j.ParentAt)))
		case j.Later == JobFail && j.Parent == JobPending:
			waiting = append(waiting, fmt.Sprintf("%s (still running on the parent %s)", j.Name, shortSHA(j.ParentAt)))
		case j.Later == JobFail:
			ours = append(ours, fmt.Sprintf("%s (still red on %s)", j.Name, shortSHA(j.LaterAt)))
		case j.Parent == JobFail:
			unsettled = append(unsettled, fmt.Sprintf("%s (also red on the parent %s, no later result in the commits read)", j.Name, shortSHA(j.ParentAt)))
		default:
			unsettled = append(unsettled, fmt.Sprintf("%s (no later result in the commits read)", j.Name))
		}
	}
	parts := []string{red}
	add := func(label string, names []string) {
		if len(names) > 0 {
			parts = append(parts, label+": "+strings.Join(names, ", "))
		}
	}
	add("not red before this merge", ours)
	add("failing until main shows otherwise", unsettled)
	add("waiting on", waiting)
	add("main's, red on the commit before this merge and still red after it, not this goal's", mains)
	add("since green on main", green)
	ci.Detail = strings.Join(parts, "; ")
	if len(ours) > 0 || len(unsettled) > 0 || len(jobs) == 0 {
		ci.OK = false
		return ci, l
	}
	l.Failed, l.Pending = 0, l.Pending+len(waiting)
	if l.Pending > 0 {
		ci.Pending = true
		if others := l.Pending - len(waiting); others > 0 {
			ci.Detail += fmt.Sprintf("; %d of %d checks still running", others, total)
		}
	}
	return ci, l
}

// sameFact reports whether a fact still says what it said. CI that stays good may name other commits as main moves.
func sameFact(a, b Fact) bool {
	if a.Check == ciFact && b.Check == ciFact && a.OK && b.OK && !a.Pending && !b.Pending {
		a.Detail, b.Detail = "", ""
	}
	return a == b
}
