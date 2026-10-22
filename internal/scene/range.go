package scene

// Range groups peaks under one chief; nested ranges stand as nearer ridgelines in front of it.
type Range struct {
	ID, Name string
	Weather  Weather
	Peaks    []Peak
	Ranges   []Range
}

func (r Range) weather() Weather {
	var parts []Weather
	for _, p := range r.Peaks {
		parts = append(parts, p.Weather)
	}
	for _, sub := range r.Ranges {
		parts = append(parts, sub.weather())
	}
	return combine(r.Weather, parts)
}
