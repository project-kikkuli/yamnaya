package scene

// Peak is one piece of work: a mission or a goal. Its team climbs it, its pull requests are camps on it.
type Peak struct {
	ID, Name string
	Weather  Weather
	Team     []Climber
	Camps    []Camp
	// Progress is how far up the work is, 0 at the foot and 1 on top.
	Progress float64
	// NeedsYou marks work waiting on the owner; a flare goes up from it.
	NeedsYou bool
}

// Climber is one agent on a peak.
type Climber struct {
	Name   string
	Stance Stance
}

type Stance int

const (
	// Climbing agents are busy and move.
	Climbing Stance = iota
	// Waiting agents are stopped on the owner and signal.
	Waiting
	// Resting agents are idle at base camp.
	Resting
)

// Camp is a pull request pitched on the peak at the height of the gates it passed.
type Camp struct {
	Label string
	State CampState
	// Stage is how many of the six gates from branch to merged it passed.
	Stage int
}

type CampState int

const (
	Open CampState = iota
	Green
	Failing
	Merged
)
