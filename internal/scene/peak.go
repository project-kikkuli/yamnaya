package scene

// Peak is one goal: its climbers are the agents on it, Progress how far up they are.
type Peak struct {
	ID, Name string
	Weather  Weather
	Climbers int
	Progress float64
}
