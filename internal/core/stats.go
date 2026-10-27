package core

import (
	"slices"
	"time"
)

// PRCount counts pull requests: open ones by CI result, then merged ones; closed ones count nowhere.
type PRCount struct {
	Open    int `json:"open,omitempty"`
	Green   int `json:"green,omitempty"`
	Failing int `json:"failing,omitempty"`
	Running int `json:"running,omitempty"`
	Merged  int `json:"merged,omitempty"`
}

// CountPRs counts each pull request once by URL.
func CountPRs(prs []PR) PRCount {
	var c PRCount
	seen := map[string]bool{}
	for _, pr := range prs {
		if seen[pr.URL] {
			continue
		}
		seen[pr.URL] = true
		switch {
		case pr.Status != nil && pr.Status.State == "MERGED":
			c.Merged++
		case pr.Status != nil && pr.Status.Closed():
		default:
			c.Open++
			if pr.Status != nil {
				switch pr.Status.CI() {
				case "failing":
					c.Failing++
				case "running":
					c.Running++
				case "passing":
					c.Green++
				}
			}
		}
	}
	return c
}

func (c PRCount) Plus(o PRCount) PRCount {
	return PRCount{c.Open + o.Open, c.Green + o.Green, c.Failing + o.Failing, c.Running + o.Running, c.Merged + o.Merged}
}

// CI is the worst CI state across the open pull requests: failing, running, green, or "" when none is open or none has checks.
func (c PRCount) CI() string {
	switch {
	case c.Failing > 0:
		return "failing"
	case c.Running > 0:
		return "running"
	case c.Green > 0:
		return "green"
	}
	return ""
}

// Stats are a mission's or goal's numbers as the host it runs on computes them each time it builds its board, from transcripts,
// git, GitHub and ym's database; another host reads them from that host's snapshot instead of computing its own.
type Stats struct {
	// Vibe is the latest vibe its agents or manager recorded; Phases its phases.
	Vibe   *Vibe  `json:"vibe,omitempty"`
	Phases Phases `json:"phases,omitzero"`
	// Running and Idle count its agents still working and those sitting idle; finished ones count in neither.
	Running int     `json:"running,omitempty"`
	Idle    int     `json:"idle,omitempty"`
	PRs     PRCount `json:"prs"`
	// Spend is nil until usage is read.
	Spend *Spend `json:"spend,omitempty"`
	// Done is the fraction of its work finished, nil with nothing to measure.
	Done *float64 `json:"done,omitempty"`
	// Progress is a goal's latest progress line from its manager, at ProgressAt; Started is when the goal began.
	Progress   string    `json:"progress,omitempty"`
	ProgressAt time.Time `json:"progress_at,omitzero"`
	Started    time.Time `json:"started,omitzero"`
}

// MissionStats are a mission on this host by this host's reading, its spend once usage is ready and its finished fraction as
// measure reads it.
func MissionStats(p Project, ready bool, measure func(Project) (float64, bool)) Stats {
	st := Stats{Phases: p.Phases}
	if v, ok := p.Vibe(); ok {
		st.Vibe = &v
	}
	var prs []PR
	for _, s := range p.Sessions {
		if s.Key() != p.Key {
			st.count(s.Status)
		}
		for _, m := range s.Crew {
			if !m.Cut {
				st.count(m.Status)
			}
		}
		prs = append(prs, s.AllPRs()...)
	}
	st.PRs = CountPRs(prs)
	if ready {
		sp := p.Spend
		st.Spend = &sp
	}
	if measure != nil {
		if f, ok := measure(p); ok {
			st.Done = &f
		}
	}
	return st
}

func (st *Stats) count(s Status) {
	switch s {
	case Busy:
		st.Running++
	case Idle, Waiting:
		st.Idle++
	}
}

// goalStats are a goal on this host by this host's reading: its workers, every pull request opened for it, its missions' vibe and
// spend, and its manager's latest progress line.
func goalStats(n Node, missions []Project, statuses map[string]PRStatus, opened []string, ready bool) Stats {
	st := Stats{Started: n.Goal.CreatedAt}
	var prs []PR
	for _, w := range n.Workers {
		switch w.State {
		case "busy", "hung", "adrift":
			st.Running++
		case "idle", "waiting":
			st.Idle++
		}
		prs = append(prs, w.PRs...)
	}
	if n.Live != nil {
		prs = append(prs, n.Live.PRs...)
	}
	for _, url := range opened {
		pr := PR{URL: url}
		if s, ok := statuses[url]; ok {
			pr.Status = &s
		}
		prs = append(prs, pr)
	}
	for i := range prs {
		if s, ok := statuses[prs[i].URL]; ok && prs[i].Status == nil {
			prs[i].Status = &s
		}
	}
	st.PRs = CountPRs(prs)
	var spend Spend
	for _, p := range missions {
		spend = spend.Plus(p.Spend)
		if v, ok := p.Vibe(); ok && (st.Vibe == nil || v.At.After(st.Vibe.At)) {
			st.Vibe = &v
		}
	}
	if ready {
		st.Spend = &spend
	}
	if e := n.Feed.Progress; e != nil {
		st.Progress, st.ProgressAt = e.Payload, e.Created
	}
	return st
}

// settleStats sets every mission's and goal's stats: this host's own from its board, another host's from that host's snapshot.
func (c *Core) settleStats(b *Board) error {
	missions := map[string]map[string]Stats{}
	goals := map[string]Stats{}
	self := c.SelfName()
	for _, r := range b.Remotes {
		missions[r.Host] = r.Missions
		for id, st := range r.GoalStats {
			goals[qualifyFrom(self, r.Host, id)] = st
		}
	}
	b.Projects = slices.Clone(b.Projects)
	for i := range b.Projects {
		p := &b.Projects[i]
		host := ProjectHost(*p)
		if host == "" {
			p.Stats = MissionStats(*p, b.Fleet.Ready, c.Measure)
			continue
		}
		st := missions[host][p.Key]
		p.Stats, p.Phases, p.Vibes = st, st.Phases, nil
		if st.Vibe != nil {
			p.Vibes = []Vibe{*st.Vibe}
		}
		if st.Spend != nil {
			p.Spend = *st.Spend
		}
	}
	statuses, err := c.Store.PRStatuses()
	if err != nil {
		return err
	}
	opened, err := c.Store.GoalPRs()
	if err != nil {
		return err
	}
	byManager := map[string][]Project{}
	for _, p := range b.Projects {
		if p.Manager != "" {
			byManager[p.Manager] = append(byManager[p.Manager], p)
		}
	}
	var settle func([]Node) []Node
	settle = func(ns []Node) []Node {
		out := slices.Clone(ns)
		for i := range out {
			n := &out[i]
			n.Children = settle(n.Children)
			switch {
			case n.Goal == nil:
			case n.Manager.Host != "":
				n.Stats = goals[n.Manager.ID]
			default:
				n.Stats = goalStats(*n, byManager[n.Manager.ID], statuses, opened[n.Goal.ID], b.Fleet.Ready)
			}
		}
		return out
	}
	b.Tree = settle(b.Tree)
	return nil
}

// ProjectHost is the other host a mission's sessions run on, "" for this one.
func ProjectHost(p Project) string {
	for _, s := range p.Sessions {
		if s.Remote != "" {
			return s.Remote
		}
	}
	return ""
}
