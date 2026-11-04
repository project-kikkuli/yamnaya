package core

import (
	"strings"
	"testing"
	"time"
)

const refileReport = "`e1f19db` on main. Live `ym` is `vcs.revision` `e1f19db`, unmodified, built 11:46; serve pid 1044 runs it; highest message seq at install was 3293. Suite not started when written: worktree `goal18-land` at `e1f19db`, #4 in line, log `~/tmp/arbiter-work/goal18-suite-main.log`.\n\n- **L3-L5:** root cause confirmed. `RefName` merged only `c.Remotes.Boards()` and serve has no `Remotes`, so it never read `ym_host_boards`. Every process now reads stored boards with a live read on top (`RefName`, `checkRemoteChief`, `chiefScope`).\n- **L1:** repository skipped for an id scope.\n- **L2:** `RefName` strips this host's relay name.\n- **L6:** prompt example by name."

func TestAReplyIsCountedOnlyWhenItAddsNothing(t *testing.T) {
	for _, tc := range []struct {
		name, said, prev string
		why              string
	}{
		{name: "a refile that failed and asks for a call, re-quoting the report",
			said: "**I tried again, but the refile didn't take: #6119291 still reads `PLACEHOLDER`.** `ym goal report` returned the same report number and left the text unchanged.\n\n**Your call:** the simplest fix is to register a new assignment id for this.\n\n" + refileReport,
			prev: refileReport},
		{name: "a failing test on main in a short note with still in it",
			said: "CI confirms it on main's code against real Postgres. The only failure is the DB test (1 failed, 31 passed), and the leftover lines `(9:29:49, 1)` and `(9:29:49, 3)` are still in the loaded chart. The fix goes up as soon as the hand-run checks report.",
			prev: "The fix is written and I am running the hand-run checks."},
		{name: "a failure beside a wait",
			said: "#18552's only failure is charting-mobile-export, the same Expo dependency gate that's failing on main. Still waiting on #18880 and the #18891 rerun.",
			prev: "#18690 is green."},
		{name: "a nothing-new opening over a request to act",
			said: "Still paused, and nothing has changed here. Next: when you resume me, confirm the rebase onto D3's branch.",
			prev: "Paused until D3 lands."},
		{name: "a question to the manager",
			said: "Still waiting on the lane. Should I drop the flaky shard?",
			prev: "Paused until D3 lands."},
		{name: "a review verdict beside a wait",
			said: "The Patient identity review is done: **needs-rework**. Its main problem is that the doc re-keys identity, which breaks the rule. The other eight reviews are still running.",
			prev: "Reviews started."},
		{name: "a first run that failed",
			said: "Still waiting on the rerun; the first run failed on a timeout.",
			prev: "Reviews started."},
		{name: "a failure after a negation in the same sentence",
			said: "I did not push because the build failed. Waiting on the rerun.",
			prev: "Reviews started."},
		{name: "a failure once again",
			said: "Still waiting on the rerun. Once again CI failed.",
			prev: "Reviews started."},
		{name: "a question in bold",
			said: "Still waiting. **Use a dedicated container or the shared DSN?**",
			prev: "Reviews started."},
		{name: "waiting for the manager's word",
			said: "I'll wait for your word on which commit to probe.",
			prev: "Reviews started."},
		{name: "a failure count that changed",
			said: "The suite failed: 7 failed, 12 passed.",
			prev: "The suite failed: 1 failed, 12 passed."},
		{name: "the same failure word for word",
			said: "The DB test failed on main.",
			prev: "The DB test failed on main.",
			why:  "the same words"},
		{name: "a failure that has not happened",
			said: "Still running: no failures so far, and lint hasn't started.",
			prev: "Parser is pushed.",
			why:  "still-waiting"},
		{name: "a failure it would act on later",
			said: "The CI watcher is running in the background and stops at the first failure or when every check finishes. Any failure gets fixed on this branch.",
			prev: "Parser is pushed.",
			why:  "still-waiting"},
		{name: "a test named after a failure",
			said: "`authored-test-gap` is still adding the later-page failure test.",
			prev: "Parser is pushed.",
			why:  "still-waiting"},
		{name: "a real waiting note",
			said: "The implementer's core tests pass and its CLI cases are still running. I'm waiting on its report.",
			prev: "Parser is pushed.",
			why:  "still-waiting"},
		{name: "nothing new",
			said: "No change: the push is still on hold while the isolated run of those two test files finishes.",
			prev: "Parser is pushed.",
			why:  "nothing is new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := addsNothing(tc.said, tc.prev)
			if tc.why == "" && got != "" || tc.why != "" && !strings.Contains(got, tc.why) {
				t.Fatalf("counted because %q, want %q", got, tc.why)
			}
		})
	}
}

func TestAnUnknownHeadIsNotNoNewCommit(t *testing.T) {
	for _, tc := range []struct {
		name, remote string
		why          string
	}{
		{"a local session outside a repository", "", "no new ask, 100% the same words as its last reply"},
		{"a session on another host", "box", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			s := Live{Session: Session{Harness: "claude", ID: "agent"}, Remote: tc.remote}
			s.Result = "Full suite still running."
			s.ResultAt = w.clock
			if why, err := w.core.news(s); err != nil || why != "" {
				t.Fatalf("first reply counted because %q, err %v", why, err)
			}
			s.ResultAt = w.clock.Add(time.Minute)
			why, err := w.core.news(s)
			if err != nil {
				t.Fatal(err)
			}
			if why != tc.why {
				t.Fatalf("second reply counted because %q, want %q", why, tc.why)
			}
		})
	}
}
