// Package gh fetches pull request status with the gh CLI.
package gh

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// Source reads a status from Claude Code's PR status cache when that file is fresh, else asks gh.
type Source struct {
	// Shared is a JSON map of PR URL to status that another tool keeps; "" skips it.
	Shared string
	Run    func(url string) ([]byte, error)
	Now    func() time.Time
}

func New() *Source {
	home, _ := os.UserHomeDir()
	return &Source{Shared: filepath.Join(home, ".claude", "gh-pr-status-cache.json"), Run: view, Now: time.Now}
}

func view(url string) ([]byte, error) {
	return exec.Command("gh", "pr", "view", url, "--json", "state,statusCheckRollup,reviewDecision,title").Output()
}

type check struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

type viewJSON struct {
	State          string  `json:"state"`
	Title          string  `json:"title"`
	ReviewDecision string  `json:"reviewDecision"`
	Checks         []check `json:"statusCheckRollup"`
}

type sharedJSON struct {
	State  string `json:"state"`
	Title  string `json:"title"`
	Review string `json:"review"`
	Checks struct {
		Passed  int `json:"passed"`
		Failed  int `json:"failed"`
		Pending int `json:"pending"`
	} `json:"checks"`
}

func (s *Source) PRStatus(url string) (core.PRStatus, error) {
	if st, ok := s.shared(url); ok {
		return st, nil
	}
	out, err := s.Run(url)
	if err != nil {
		return core.PRStatus{}, err
	}
	var v viewJSON
	if err := json.Unmarshal(out, &v); err != nil {
		return core.PRStatus{}, err
	}
	st := core.PRStatus{State: v.State, Title: v.Title, Review: v.ReviewDecision, Fetched: s.Now()}
	for _, c := range v.Checks {
		switch result(c) {
		case "pass":
			st.Passed++
		case "fail":
			st.Failed++
		default:
			st.Pending++
		}
	}
	return st, nil
}

// result folds a check run's conclusion or a status context's state into pass, fail or pending.
func result(c check) string {
	v := c.Conclusion
	if v == "" {
		v = c.State
	}
	switch v {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return "pass"
	case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
		return "fail"
	}
	return "pending"
}

func (s *Source) shared(url string) (core.PRStatus, bool) {
	if s.Shared == "" {
		return core.PRStatus{}, false
	}
	info, err := os.Stat(s.Shared)
	if err != nil || s.Now().Sub(info.ModTime()) >= core.PRStale {
		return core.PRStatus{}, false
	}
	b, err := os.ReadFile(s.Shared)
	if err != nil {
		return core.PRStatus{}, false
	}
	var all map[string]sharedJSON
	if json.Unmarshal(b, &all) != nil {
		return core.PRStatus{}, false
	}
	v, ok := all[url]
	if !ok {
		return core.PRStatus{}, false
	}
	return core.PRStatus{State: v.State, Title: v.Title, Review: v.Review, Passed: v.Checks.Passed, Failed: v.Checks.Failed,
		Pending: v.Checks.Pending, Fetched: info.ModTime()}, true
}
