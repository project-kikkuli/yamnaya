package drift

import (
	"fmt"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)

type step func(t *Tracker, i int)

func fail(tool, command, text string) step {
	return func(t *Tracker, i int) {
		sig, detail := Failure(tool, command, text)
		t.Call()
		t.Result(sig, detail, fmt.Sprint("msg", i), t0.Add(time.Duration(i)*time.Minute))
	}
}

func ok() step {
	return func(t *Tracker, i int) {
		t.Call()
		t.Result("", "", fmt.Sprint("msg", i), t0.Add(time.Duration(i)*time.Minute))
	}
}

func wrote() step { return func(t *Tracker, i int) { t.Wrote() } }
func owner() step { return func(t *Tracker, i int) { t.Owner() } }
func compacted(after time.Duration) step {
	return func(t *Tracker, i int) { t.Compacted(t0.Add(after)) }
}

func repeat(n int, s step) []step {
	out := make([]step, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func join(parts ...[]step) []step {
	var out []step
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

const timeout = "Exit code 143\nCommand timed out after 2m 0s"

func TestSigns(t *testing.T) {
	commit := fail("Bash", "git commit --amend -F msg.txt", timeout)
	cases := []struct {
		name  string
		steps []step
		want  string
	}{
		{"the same failure five times over four minutes", repeat(5, commit), Looping},
		{"four times is not yet a loop", repeat(4, commit), ""},
		{"a success between does not break the count", join(repeat(3, commit), repeat(2, ok()), repeat(2, commit)), Looping},
		{"different failures do not add up", []step{commit, fail("Bash", "go test", "FAIL x"), commit, fail("Bash", "go vet", "vet: y"), commit}, ""},
		{"a change to a file between repeats is iteration", join(repeat(3, commit), []step{wrote()}, repeat(3, commit)), ""},
		{"the owner steering clears the count", join(repeat(4, commit), []step{owner()}, repeat(2, commit)), ""},
		{"a bare exit code is told apart by its command", []step{
			fail("Bash", "psql -c 'select 1'", "Exit code 2"), fail("Bash", "go test ./a", "Exit code 2"),
			fail("Bash", "psql -c 'select 1'", "Exit code 2"), fail("Bash", "go test ./b", "Exit code 2"), fail("Bash", "make", "Exit code 2")}, ""},
		{"a sleep before the retry keeps it the same command", []step{
			fail("Bash", "psql -c 'select 1'", "Exit code 124"), fail("Bash", "sleep 5; psql -c 'select 1'", "Exit code 124"),
			fail("Bash", "sleep 10 && psql -c 'select 1'", "Exit code 124"), fail("Bash", "psql -c 'select 1'", "Exit code 124"),
			fail("Bash", "sleep 30; psql -c 'select 1'", "Exit code 124")}, Looping},
		{"pending CI checks are a wait, not a failure", repeat(6, fail("Bash", "sleep 110 && gh pr checks 12", "Exit code 8\nbuild\tpending")), ""},
		{"a session limit the agent is woken into is a wait", repeat(6, fail("harness", "", "You've hit your session limit · resets 9am (America/New_York)")), ""},
		{"the permission classifier being rate limited is a wait", repeat(6, fail("Bash", "git push", "claude-sonnet-5[1m] is temporarily unavailable (rate-limited), so auto mode cannot determine the safety of Bash right now. Wait a moment and try again.")), ""},
		{"the API limiting requests is a wait", repeat(6, fail("harness", "", "API Error: Server is temporarily limiting requests (not your usage limit) · Rate limited")), ""},
		{"a search that finds nothing is an answer", repeat(6, fail("Bash", "rg -n '<<<<<<<' src", "Exit code 1")), ""},
		{"one blocker hit through different tools repeats", []step{
			fail("Bash", "gh api x", "This agent is isolated in the worktree /w, but this command runs gh"),
			fail("Monitor", "gh api y", "This agent is isolated in the worktree /w, but this command runs gh"),
			fail("Bash", "gh api z", "This agent is isolated in the worktree /w, but this command runs gh"),
			fail("Monitor", "gh api x", "This agent is isolated in the worktree /w, but this command runs gh"),
			fail("Bash", "gh api y", "This agent is isolated in the worktree /w, but this command runs gh")}, Looping},
		{"compactions with no work between", []step{compacted(0), compacted(time.Minute), compacted(2 * time.Minute)}, Compacting},
		{"compactions hours apart", []step{compacted(0), compacted(2 * time.Hour), compacted(4 * time.Hour)}, ""},
		{"compactions with work between", join([]step{compacted(0)}, repeat(10, ok()), []step{compacted(time.Minute), compacted(2 * time.Minute)}), ""},
	}
	for _, c := range cases {
		var tr Tracker
		for i, s := range c.steps {
			s(&tr, i)
		}
		got := ""
		if signs := tr.Signs(); len(signs) > 0 {
			got = signs[0].Kind
		}
		if got != c.want {
			t.Errorf("%s: sign %q, want %q (signs %v)", c.name, got, c.want, tr.Signs())
		}
	}
}

func TestABurstFasterThanSpanIsNotALoop(t *testing.T) {
	var tr Tracker
	for i := range 8 {
		sig, detail := Failure("Edit", "", "File has not been read yet")
		tr.Result(sig, detail, fmt.Sprint(i), t0.Add(time.Duration(i)*10*time.Second))
	}
	if s := tr.Signs(); len(s) != 0 {
		t.Fatalf("8 failures in 70s gave %v, want none", s)
	}
}

func TestFailuresInOneMessageCountOnce(t *testing.T) {
	var tr Tracker
	for i := range 3 {
		for range 4 {
			sig, detail := Failure("Agent", "", "Teammates cannot spawn other teammates")
			tr.Result(sig, detail, fmt.Sprint("msg", i), t0.Add(time.Duration(i)*2*time.Minute))
		}
	}
	if s := tr.Signs(); len(s) != 0 {
		t.Fatalf("three messages of four parallel failures gave %v, want none", s)
	}
}

func TestAnEpisodeKeepsItsStartWhileItLasts(t *testing.T) {
	var tr Tracker
	var e Episodes
	for i := range 30 {
		sig, detail := Failure("Bash", "git commit", timeout)
		tr.Result(sig, detail, fmt.Sprint(i), t0.Add(time.Duration(i)*time.Minute))
		e.See(tr.Signs())
	}
	got := e.List()
	if len(got) != 1 || !got[0].Since.Equal(t0) || !got[0].At.Equal(t0.Add(29*time.Minute)) {
		t.Fatalf("episodes %+v, want one from the first failure to the last", got)
	}
}
