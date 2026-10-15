package core

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Tokens counts one or more model responses. Input excludes cache reads and writes.
type Tokens struct {
	Input        int64
	Output       int64
	CacheRead    int64
	CacheWrite5m int64
	CacheWrite1h int64
}

func (t Tokens) Plus(o Tokens) Tokens {
	return Tokens{t.Input + o.Input, t.Output + o.Output, t.CacheRead + o.CacheRead, t.CacheWrite5m + o.CacheWrite5m, t.CacheWrite1h + o.CacheWrite1h}
}

func (t Tokens) Total() int64 {
	return t.Input + t.Output + t.CacheRead + t.CacheWrite5m + t.CacheWrite1h
}

// Spend is usage priced at public API rates. Unpriced counts tokens of models with no known price.
type Spend struct {
	USD      float64
	Tokens   Tokens
	Unpriced int64
	// Share is this spend's fraction of the API-priced usage seen inside each current limit window, keyed by Limit.Key.
	Share map[string]float64
}

func (s Spend) Plus(o Spend) Spend {
	out := Spend{s.USD + o.USD, s.Tokens.Plus(o.Tokens), s.Unpriced + o.Unpriced, s.Share}
	if len(o.Share) > 0 {
		if len(s.Share) == 0 {
			out.Share = o.Share
		} else {
			out.Share = make(map[string]float64, len(s.Share)+len(o.Share))
			for k, v := range s.Share {
				out.Share[k] = v
			}
			for k, v := range o.Share {
				out.Share[k] += v
			}
		}
	}
	return out
}

// Dollars formats the spend, with a + when some tokens had no price.
func (s Spend) Dollars() string {
	v := fmt.Sprintf("$%.2f", s.USD)
	if s.USD >= 100 {
		v = fmt.Sprintf("$%.0f", s.USD)
	}
	if s.Unpriced > 0 {
		v += "+"
	}
	return v
}

// Span is a short duration such as 3d4h, 2h10m or 45s.
func Span(d time.Duration) string {
	d = max(d, 0)
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// SessionSpend is one session's spend alone (Own) and with every subagent and child thread it started (Total).
type SessionSpend struct {
	Harness string
	ID      string
	// Parent is the session that started this one, as the harness records it.
	Parent  string
	Own     Spend
	Total   Spend
	ByModel map[string]Spend
	// Reported is the harness's own running cost total for the session, 0 when it records none.
	Reported float64
	Last     time.Time
}

// Limit is one subscription rate-limit window as a harness last reported it.
type Limit struct {
	Harness  string
	Name     string
	Used     float64
	Window   time.Duration
	ResetsAt time.Time
	Plan     string
	Seen     time.Time
}

// Label names the window without repeating the harness: Codex limit ids start with "codex".
func (l Limit) Label() string { return strings.TrimPrefix(l.Name, "codex ") }

func (l Limit) Key() string { return l.Harness + "/" + l.Name }

// Start is when the window began.
func (l Limit) Start() time.Time { return l.ResetsAt.Add(-l.Window) }

// Fleet is what every session costs and how close each subscription is to its limits.
type Fleet struct {
	Ready    bool
	At       time.Time
	Limits   []Limit
	Today    Spend
	Week     Spend
	Sessions map[string]SessionSpend
	// Agents is each Claude subagent's own spend, keyed "<session>/<agent id>".
	Agents map[string]Spend
	// Windows is the start of each limit window that Spend.Share measured, keyed by Limit.Key.
	Windows map[string]time.Time
}

// Shares estimates how much of each current limit window the spend used: the window's used percent times the spend's share of
// the usage seen on this host inside it, such as "≈3.1% 5h · ≈0.8% 7d".
func (f Fleet) Shares(s Spend) string {
	var ls []Limit
	harnesses := map[string]bool{}
	for _, l := range f.Limits {
		from, ok := f.Windows[l.Key()]
		if s.Share[l.Key()] <= 0 || !ok || from.Sub(l.Start()).Abs() > 10*time.Minute {
			continue
		}
		ls = append(ls, l)
		harnesses[l.Harness] = true
	}
	var parts []string
	for _, l := range ls {
		label := l.Label()
		if len(harnesses) > 1 {
			label = l.Harness + " " + label
		}
		pct := l.Used * s.Share[l.Key()]
		v := fmt.Sprintf("≈%.1f%%", pct)
		if pct < 0.05 {
			v = "<0.1%"
		}
		parts = append(parts, v+" "+label)
	}
	return strings.Join(parts, " · ")
}

// Meter reads usage and limits without blocking the board; Ready is false until its first scan lands.
type Meter interface {
	Fleet(now time.Time) Fleet
}

// projectSpend sums the project's sessions, skipping any whose ancestor already counts it.
func projectSpend(p Project, f Fleet) Spend {
	in := map[string]bool{}
	for _, s := range p.Sessions {
		in[s.Key()] = true
	}
	var out Spend
	for key := range in {
		counted := false
		seen := map[string]bool{key: true}
		for at := f.Sessions[key].Parent; at != "" && !seen[at]; at = f.Sessions[at].Parent {
			seen[at] = true
			if in[at] {
				counted = true
				break
			}
		}
		if !counted {
			out = out.Plus(f.Sessions[key].Total)
		}
	}
	return out
}

// ThisHost is the short hostname. Spend covers only the transcripts on this host; limits are account-wide.
func ThisHost() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "this host"
	}
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}
