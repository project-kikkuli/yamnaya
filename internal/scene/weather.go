package scene

// Weather is the sky over a peak or range, and what it says about the work under it.
type Weather int

const (
	Clear Weather = iota
	Clouds
	Storm
	Eruption
	Summit
)

// combine is the sky over a group: its worst storm, or Summit once every part is done.
func combine(own Weather, parts []Weather) Weather {
	rank := map[Weather]int{Clouds: 0, Clear: 1, Summit: 1, Storm: 2, Eruption: 3}
	w, done := own, own == Summit || len(parts) > 0
	if w == Summit || w == Clear && len(parts) > 0 {
		w = Clouds
	}
	for _, p := range parts {
		if p == Summit {
			continue
		}
		done = false
		if rank[p] > rank[w] {
			w = p
		}
	}
	if done && rank[w] < rank[Storm] {
		return Summit
	}
	return w
}
