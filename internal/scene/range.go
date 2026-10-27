package scene

// Range is a group: a repository, or a chief whose goals and nested chiefs stand inside it.
type Range struct {
	ID, Name string
	Weather  Weather
	Peaks    []Peak
	Ranges   []Range
}

// Sky is the weather over the whole group: its worst storm, or Summit once every part is done.
func (r Range) Sky() Weather {
	var parts []Weather
	for _, p := range r.Peaks {
		parts = append(parts, p.Weather)
	}
	for _, sub := range r.Ranges {
		parts = append(parts, sub.Sky())
	}
	return Combine(r.Weather, parts)
}
