package core

import "testing"

func TestProjectSpendCountsEachSessionOnce(t *testing.T) {
	spend := func(usd float64) Spend { return Spend{USD: usd} }
	fleet := Fleet{Ready: true, Sessions: map[string]SessionSpend{
		"lead":  {ID: "lead", Total: spend(10)},
		"child": {ID: "child", Parent: "lead", Total: spend(4)},
		"grand": {ID: "grand", Parent: "child", Total: spend(1)},
		"solo":  {ID: "solo", Total: spend(2)},
	}}
	cases := []struct {
		name     string
		sessions []string
		want     float64
	}{
		{"a lead already includes its live children and grandchildren", []string{"lead", "child", "grand"}, 10},
		{"a child whose lead is elsewhere counts on its own", []string{"child", "solo"}, 6},
		{"sessions with no recorded usage add nothing", []string{"solo", "unknown"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var p Project
			for _, id := range c.sessions {
				p.Sessions = append(p.Sessions, Live{Session: Session{ID: id}})
			}
			if got := projectSpend(p, fleet).USD; got != c.want {
				t.Errorf("spend = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDollarsNeverShowsASpendAboveZeroAsNothing(t *testing.T) {
	for _, c := range []struct {
		spend Spend
		want  string
	}{
		{Spend{}, "$0.00"},
		{Spend{USD: 0.0026}, "<$0.01"},
		{Spend{USD: 0.0099}, "<$0.01"},
		{Spend{USD: 0.01}, "$0.01"},
		{Spend{USD: 0.0026, Unpriced: 1}, "<$0.01+"},
		{Spend{USD: 138.4}, "$138"},
	} {
		if got := c.spend.Dollars(); got != c.want {
			t.Errorf("%v: %q, want %q", c.spend.USD, got, c.want)
		}
	}
}
