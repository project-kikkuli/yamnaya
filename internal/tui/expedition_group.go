package tui

import "github.com/project-kikkuli/yamnaya/internal/core"

type expeditionGroup struct {
	region, mission string
	scale           int
	cut             int
	needs           int
	counts          [6]int
	sample          []expedition
}

func (g expeditionGroup) total() int {
	n := 0
	for _, count := range g.counts {
		n += count
	}
	return n
}

func (g *expeditionGroup) add(e expedition) {
	g.counts[e.status]++
	if e.cut {
		g.cut++
	}
	if len(g.sample) < 6 {
		g.sample = append(g.sample, e)
	}
}

func expeditionRank(e expedition) int {
	if e.cut {
		return 5
	}
	return map[core.Status]int{core.Busy: 0, core.Waiting: 1, core.Idle: 2, core.Unknown: 3, core.Done: 4}[e.status]
}
