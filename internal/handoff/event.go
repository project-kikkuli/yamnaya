package handoff

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

// File is a transcript to watch; Work is the key of the live session it belongs to and Dir that session's directory.
type File struct {
	Work, Harness, Session, Agent, Path, Dir string
}

type Event struct {
	ID, Work, Harness, Session, Agent, Path, Dir string
	Offset                                       int64
	At                                           time.Time
	Text                                         string
	Error                                        string
}

type Claim struct {
	ID                                                                        string
	Event                                                                     Event
	Kind, Target, Topic, Question, Quote, Existing, Recommendation, Requester string
}

func Key(parts ...string) string {
	raw, _ := json.Marshal(parts)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
