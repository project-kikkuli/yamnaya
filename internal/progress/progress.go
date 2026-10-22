// Package progress measures a mission from facts: how much of its crew finished and how far each pull request got.
// Agent-set phases are labels only and never move the bar.
package progress

import (
	"fmt"
	"path"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// Gates are the steps a branch passes on its way to main.
var Gates = [...]string{"branch", "commits", "pr", "ci", "threads", "merged"}

// Unit is one branch of work: a pull request, or a branch with none yet.
type Unit struct {
	Label  string
	Branch string
	URL    string
	// Met marks each gate in Gates the unit passed.
	Met [len(Gates)]bool
	// Detail holds the raw facts behind the gates, such as "ci 4/5" or "2/3 threads open".
	Detail  string
	Closed  bool
	Failing bool
}

// Passed counts the gates met.
func (u Unit) Passed() int {
	n := 0
	for _, ok := range u.Met {
		if ok {
			n++
		}
	}
	return n
}

// Crew counts dispatched agents. Cut ones, killed or failed mid-turn, are left out of Finished and Dispatched.
type Crew struct {
	Finished, Cut, Dispatched int
}

// Mission is a mission's measured progress.
type Mission struct {
	Crew  Crew
	Units []Unit
	// Phase is the agent-set phase, shown as a label.
	Phase string
}

// Fraction is the bar's fill; ok is false when the mission has nothing to measure.
func (m Mission) Fraction() (f float64, ok bool) {
	done, total := m.Crew.Finished, m.Crew.Dispatched
	for _, u := range m.open() {
		done, total = done+u.Passed(), total+len(Gates)
	}
	// progress = (crew finished + gates passed) / (crew dispatched + 6 × open units)
	if total == 0 {
		return 0, false
	}
	return float64(done) / float64(total), true
}

// Parts states the formula's raw numbers.
func (m Mission) Parts() string {
	gates := 0
	for _, u := range m.open() {
		gates += u.Passed()
	}
	s := fmt.Sprintf("crew %d/%d finished", m.Crew.Finished, m.Crew.Dispatched)
	if m.Crew.Cut > 0 {
		s += fmt.Sprintf(" (%d cut, not counted)", m.Crew.Cut)
	}
	return s + fmt.Sprintf(", gates %d/%d over %d branches", gates, len(Gates)*len(m.open()), len(m.open()))
}

func (m Mission) open() []Unit {
	var out []Unit
	for _, u := range m.Units {
		if !u.Closed {
			out = append(out, u)
		}
	}
	return out
}

// Of measures a mission from its sessions, crew, exited sessions and pull requests.
func Of(p core.Project) Mission {
	var m Mission
	if len(p.Phases.Names) > 0 && p.Phases.Current < len(p.Phases.Names) {
		m.Phase = p.Phases.Names[p.Phases.Current]
	}
	var lives []core.Live
	for _, s := range p.Sessions {
		if s.Key() != p.Key {
			m.Crew.add(s.Status, false)
		}
		for _, c := range s.Crew {
			m.Crew.add(c.Status, c.Cut)
		}
		lives = append(lives, s)
	}
	for _, d := range p.Dormant {
		if d.ID() != p.Key {
			m.Crew.add(core.Done, false)
		}
	}
	m.Units = Units(lives)
	return m
}

func (c *Crew) add(s core.Status, cut bool) {
	if cut {
		c.Cut++
		return
	}
	c.Dispatched++
	if s != core.Busy && s != core.Waiting {
		c.Finished++
	}
}

// Units lists the pull requests the sessions and their crew opened, then their branches that have none.
func Units(sessions []core.Live) []Unit {
	var out []Unit
	seen := map[string]bool{}
	type head struct{ repo, branch string }
	heads := map[head]bool{}
	headed := map[string]bool{}
	for _, s := range sessions {
		for _, pr := range s.AllPRs() {
			if seen[pr.URL] {
				continue
			}
			seen[pr.URL] = true
			u := PRUnit(pr)
			heads[head{strings.ToLower(path.Base(pr.Repo)), u.Branch}] = true
			headed[u.Branch] = true
			out = append(out, u)
		}
	}
	listed := map[head]bool{}
	branch := func(repoKey, def, name string, ahead int) {
		g := core.GitInfo{Common: repoKey, Default: def}
		if name == "" || name == "HEAD" || g.IsDefault(name) {
			return
		}
		id := core.RepoID(repoKey)
		repo := strings.ToLower(core.RepoLabel(id))
		if listed[head{id, name}] || heads[head{repo, name}] || (repo == "" && headed[name]) {
			return
		}
		listed[head{id, name}] = true
		out = append(out, BranchUnit(name, ahead))
	}
	for _, s := range sessions {
		branch(s.RepoKey, s.Default, s.Branch, s.Ahead)
		for _, c := range s.Crew {
			branch(c.RepoKey, c.Default, c.Branch, c.Ahead)
		}
	}
	return out
}

// BranchUnit is a branch with no pull request yet.
func BranchUnit(name string, ahead int) Unit {
	u := Unit{Label: name, Branch: name, Detail: fmt.Sprintf("%d commits", ahead)}
	u.Met[0], u.Met[1] = true, ahead > 0
	return u
}

// PRUnit derives the gates a pull request passed from its last fetched status.
func PRUnit(pr core.PR) Unit {
	u := Unit{Label: fmt.Sprintf("%s#%d", pr.Repo, pr.Number), URL: pr.URL}
	u.Met[0], u.Met[1], u.Met[2] = true, true, true
	st := pr.Status
	if st == nil || st.State == "UNKNOWN" {
		u.Detail = "status not fetched yet"
		return u
	}
	u.Branch = st.Head
	if st.State == "MERGED" {
		u.Met = [len(Gates)]bool{true, true, true, true, true, true}
		u.Detail = "merged"
		return u
	}
	if st.State == "CLOSED" {
		u.Closed, u.Detail = true, "closed unmerged"
		return u
	}
	u.Met[3], u.Failing = st.Failed == 0 && st.Pending == 0, st.Failed > 0
	u.Met[4] = st.Unresolved == 0
	checks := st.Passed + st.Failed + st.Pending
	parts := []string{fmt.Sprintf("%d commits", st.Commits)}
	if checks > 0 {
		parts = append(parts, fmt.Sprintf("ci %d/%d", st.Passed, checks))
		if st.Failed > 0 {
			parts[len(parts)-1] += fmt.Sprintf(" %d failed", st.Failed)
		}
	} else {
		parts = append(parts, "no checks")
	}
	parts = append(parts, fmt.Sprintf("%d/%d threads open", st.Unresolved, st.Threads))
	u.Detail = strings.Join(parts, ", ")
	return u
}

// Bar draws a fill of width cells, eighths included.
func Bar(f float64, width int) string {
	if width <= 0 {
		return ""
	}
	f = min(1, max(0, f))
	eighths := int(f*float64(width*8) + 0.5)
	full, part := eighths/8, eighths%8
	b := strings.Repeat("█", full)
	if part > 0 {
		b += string([]rune(" ▏▎▍▌▋▊▉")[part])
	}
	return b + strings.Repeat("·", width-len([]rune(b)))
}

// Strip names each gate with a mark: ✓ passed, ✗ failing CI, · not yet.
func Strip(u Unit) string {
	var parts []string
	for i, g := range Gates {
		mark := "·"
		switch {
		case u.Met[i]:
			mark = "✓"
		case u.Closed:
			mark = "-"
		case g == "ci" && u.Failing:
			mark = "✗"
		}
		parts = append(parts, mark+" "+g)
	}
	return strings.Join(parts, "  ")
}
