package health

import (
	"database/sql"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Day is one local calendar day's rollup.
type Day struct {
	Day      string `json:"day"`
	Reprints int    `json:"reprints"`
	// Blocked is agent time waiting on the owner; Permission is the part spent on permission prompts.
	Blocked    time.Duration `json:"blocked_ns"`
	Permission time.Duration `json:"permission_ns"`
}

// Week rolls up the days from Start, a Monday, through Days days.
type Week struct {
	Start time.Time `json:"start"`
	Days  int       `json:"days"`
	// Answered counts items answered in the week; Median and P90 are their times from filed to answered.
	Answered int           `json:"answered"`
	Median   time.Duration `json:"median_ns"`
	P90      time.Duration `json:"p90_ns"`
	// Stale counts items filed in the week that were dismissed or sat open past a day; Settled adds those answered.
	Stale      int           `json:"stale"`
	Settled    int           `json:"settled"`
	Reprints   int           `json:"reprints"`
	Blocked    time.Duration `json:"blocked_ns"`
	Permission time.Duration `json:"permission_ns"`
}

type Report struct {
	Weeks []Week `json:"weeks"`
	Days  []Day  `json:"days"`
}

// item is what the item metrics need of one item.
type item struct {
	Created  time.Time
	State    string
	Answered time.Time
}

// staleAfter is how long an item may sit open before it counts as stale.
const staleAfter = 24 * time.Hour

// Report rolls the stored days and the item history into up to weeks weeks ending with the one holding now.
func (h *Health) Report(now time.Time, weeks int) (Report, error) {
	days, err := h.days()
	if err != nil {
		return Report{}, err
	}
	items, err := h.items()
	if err != nil {
		return Report{}, err
	}
	return rollup(now, weeks, days, items), nil
}

func (h *Health) days() (map[string]*Day, error) {
	out := map[string]*Day{}
	get := func(d string) *Day {
		if out[d] == nil {
			out[d] = &Day{Day: d}
		}
		return out[d]
	}
	rows, err := h.db.Query(`SELECT day, count(*) FROM ym_health_reprints GROUP BY day`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d string
		var n int
		if err := rows.Scan(&d, &n); err != nil {
			rows.Close()
			return nil, err
		}
		get(d).Reprints = n
	}
	rows.Close()
	rows, err = h.db.Query(`SELECT day, kind, sum(ms) FROM ym_health_waits GROUP BY day, kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d, kind string
		var ms int64
		if err := rows.Scan(&d, &kind, &ms); err != nil {
			return nil, err
		}
		x := get(d)
		x.Blocked += time.Duration(ms) * time.Millisecond
		if kind == WaitPermission {
			x.Permission += time.Duration(ms) * time.Millisecond
		}
	}
	return out, rows.Err()
}

func (h *Health) items() ([]item, error) {
	rows, err := h.db.Query(`SELECT i.created_at, i.state,
		(SELECT min(e.at) FROM ym_item_events e WHERE e.item = '#' || i.seq AND e.to_state = 'answered')
		FROM ym_items i`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []item
	for rows.Next() {
		var created, state string
		var answered sql.NullString
		if err := rows.Scan(&created, &state, &answered); err != nil {
			return nil, err
		}
		it := item{State: state}
		it.Created, _ = time.Parse(time.RFC3339Nano, created)
		if answered.Valid {
			it.Answered, _ = time.Parse(time.RFC3339Nano, answered.String)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func monday(t time.Time) time.Time {
	y, m, d := t.Local().Date()
	t = time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	return t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
}

// rollup builds weekly rows back to the first week with any data, at most weeks of them.
func rollup(now time.Time, weeks int, days map[string]*Day, items []item) Report {
	first := now
	for d := range days {
		if t, err := time.ParseInLocation(time.DateOnly, d, time.Local); err == nil && t.Before(first) {
			first = t
		}
	}
	for _, it := range items {
		if it.Created.Before(first) {
			first = it.Created
		}
	}
	this := monday(now)
	start := this.AddDate(0, 0, -7*(weeks-1))
	if f := monday(first); f.After(start) {
		start = f
	}
	var r Report
	for ws := start; !ws.After(this); ws = ws.AddDate(0, 0, 7) {
		w := Week{Start: ws, Days: 7}
		if ws.Equal(this) {
			w.Days = int(now.Sub(ws)/(24*time.Hour)) + 1
		}
		for i := range w.Days {
			d := ws.AddDate(0, 0, i).Format(time.DateOnly)
			x := days[d]
			if x == nil {
				x = &Day{Day: d}
			}
			r.Days = append(r.Days, *x)
			w.Reprints += x.Reprints
			w.Blocked += x.Blocked
			w.Permission += x.Permission
		}
		end := ws.AddDate(0, 0, 7)
		var lat []time.Duration
		for _, it := range items {
			if !it.Answered.IsZero() && !it.Answered.Before(ws) && it.Answered.Before(end) {
				lat = append(lat, it.Answered.Sub(it.Created))
			}
			if it.Created.Before(ws) || !it.Created.Before(end) {
				continue
			}
			switch {
			case !it.Answered.IsZero():
				w.Settled++
			case it.State == "dismissed", now.Sub(it.Created) > staleAfter:
				w.Stale++
				w.Settled++
			}
		}
		w.Answered = len(lat)
		w.Median, w.P90 = quantile(lat, 0.5), quantile(lat, 0.9)
		r.Weeks = append(r.Weeks, w)
	}
	return r
}

// quantile is the nearest-rank q-quantile of ds, or 0 when there are none.
func quantile(ds []time.Duration, q float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(q*float64(len(s))+0.999999) - 1
	return s[min(max(i, 0), len(s)-1)]
}

// Render writes the weekly table, newest week last.
func Render(out io.Writer, r Report) {
	fmt.Fprintf(out, "%-10s  %4s  %8s  %8s  %11s  %13s  %13s  %12s\n", "week of", "days", "answered", "median", "p90", "stale", "reprints/day", "blocked h/day")
	for _, w := range r.Weeks {
		stale := "-"
		if w.Settled > 0 {
			stale = fmt.Sprintf("%d/%d %d%%", w.Stale, w.Settled, 100*w.Stale/w.Settled)
		}
		med, p90 := "-", "-"
		if w.Answered > 0 {
			med, p90 = span(w.Median), span(w.P90)
		}
		fmt.Fprintf(out, "%-10s  %4d  %8d  %8s  %11s  %13s  %13.1f  %6.1f (%s perm)\n", w.Start.Format(time.DateOnly), w.Days, w.Answered, med, p90, stale,
			float64(w.Reprints)/float64(w.Days), w.Blocked.Hours()/float64(w.Days), strconv.FormatFloat(w.Permission.Hours()/float64(w.Days), 'f', 1, 64))
	}
}

func span(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%.1fd", d.Hours()/24)
	case d >= time.Hour:
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return strings.TrimSuffix(d.Round(time.Minute).String(), "0s")
}

// Legend defines the columns.
const Legend = `answered      items answered that week; median and p90 time from filed to answered
stale         items filed that week that were dismissed or sat open over a day, of those plus the answered ones
reprints/day  assistant messages in chat holding a non-empty block of questions for you (Open for you and similar)
blocked h/day agent time from a turn that ended asking you (question block, filed item, question tool) or a
              permission prompt you answered, to your next typed message; summed over sessions
`
