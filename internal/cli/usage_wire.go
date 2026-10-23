package cli

import (
	"github.com/project-kikkuli/yamnaya/internal/core"
	"time"
)

type usageJSON struct {
	Failovers []core.FailoverState  `json:"failovers,omitempty"`
	Version   int                   `json:"version"`
	Observed  time.Time             `json:"observed"`
	Ready     bool                  `json:"ready"`
	Today     core.Spend            `json:"today"`
	Week      core.Spend            `json:"week"`
	Limits    []core.Limit          `json:"limits"`
	Sessions  []core.SessionSpend   `json:"sessions"`
	Agents    map[string]core.Spend `json:"agents,omitempty"`
}

func usageWire(f core.Fleet) *usageJSON {
	if !f.Ready && len(f.Limits) == 0 {
		return nil
	}
	out := &usageJSON{Version: 1, Failovers: f.Failovers, Observed: f.At, Ready: f.Ready, Today: f.Today, Week: f.Week, Limits: f.Limits}
	out.Agents = f.Agents
	for _, s := range f.Sessions {
		out.Sessions = append(out.Sessions, s)
	}
	return out
}
func usageFromWire(w *usageJSON) *core.Fleet {
	if w == nil || w.Version != 1 {
		return nil
	}
	f := &core.Fleet{Ready: w.Ready, Failovers: w.Failovers, At: w.Observed, Today: w.Today, Week: w.Week, Limits: w.Limits, Sessions: map[string]core.SessionSpend{}}
	f.Agents = w.Agents
	for _, s := range w.Sessions {
		f.Sessions[s.Key()] = s
	}
	return f
}
