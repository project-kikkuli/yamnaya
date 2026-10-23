package core

import (
	"path/filepath"
	"slices"
	"testing"
)

type branchPRs struct {
	branch  PRBranch
	pr      PR
	status  PRStatus
	queries int
}

func (s *branchPRs) PRsForBranches(branches []PRBranch) (map[PRBranch][]PR, error) {
	s.queries++
	out := map[PRBranch][]PR{}
	for _, b := range branches {
		out[b] = nil
		if b == s.branch {
			out[b] = []PR{s.pr}
		}
	}
	return out, nil
}

func (s *branchPRs) PRStatus(string) (PRStatus, error) { return s.status, nil }

func TestPushedPullRequestsBelongToTheGoalAcrossBranchNamesAndSessionExit(t *testing.T) {
	for _, mode := range []string{"same name", "different upstream name", "explicit push refspec"} {
		t.Run(mode, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			dir := clone(t, root, "repo", "git@github.com:me/app.git")
			local := "topic"
			if mode != "same name" {
				local = "shepherd"
			}
			git(t, dir, "switch", "-q", "-c", local)
			git(t, dir, "commit", "-q", "--allow-empty", "-m", "work")
			head := headOf(t, dir)
			git(t, dir, "config", "branch."+local+".remote", "origin")
			git(t, dir, "config", "branch."+local+".merge", "refs/heads/topic")
			if mode == "explicit push refspec" {
				git(t, dir, "config", "--remove-section", "branch."+local)
			}
			git(t, dir, "commit", "-q", "--amend", "--allow-empty", "-m", "amended since push")
			w := newOrgWorld(t)
			w.core.Git = ReadGit
			g, m := w.goal(t, "ship work", dir)
			w.spawn("agent", m.Session)
			w.set("agent", func(s *Session) {
				s.Cwd = dir
				if mode == "explicit push refspec" {
					s.Pushed("git push origin HEAD:topic", "To https://github.com/me/app.git\n   old..new  HEAD -> topic\n")
				}
			})
			source := &branchPRs{branch: PRBranch{Repo: "me/app", Head: "topic"},
				pr:     PR{URL: "https://github.com/me/app/pull/6", Repo: "me/app", Number: 6},
				status: PRStatus{State: "OPEN", Head: "topic", Pending: 1, Merge: PRMergeFacts{OID: head}, Fetched: w.clock}}
			w.core.PRs = source
			b := w.board(t)
			if n, err := w.core.RefreshPRs(b); err != nil || n != 1 {
				t.Fatalf("refresh %d: %v", n, err)
			}
			if err := w.core.Notice(b); err != nil {
				t.Fatal(err)
			}
			tracked, err := w.core.Store.GoalPRs()
			if err != nil || !slices.Equal(tracked[g.ID], []string{source.pr.URL}) {
				t.Fatalf("tracked %v: %v", tracked, err)
			}
			b = w.board(t)
			agent, _ := b.Session("agent")
			if len(agent.PRs) != 1 {
				t.Fatalf("PR missing from board: %v", agent.PRs)
			}
			w.stop("agent")
			w.set(m.Session, func(s *Session) { s.Cwd = "" })
			path := w.core.Store.memo.files[0]
			if err := w.core.Store.Close(); err != nil {
				t.Fatal(err)
			}
			w.core.Store, err = OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { w.core.Store.Close() })
			b = w.board(t)
			if !w.core.inFlight(b, b.Org, m) {
				t.Fatal("pending CI stopped counting after session exit")
			}
			st := source.status
			st.Pending, st.Passed = 0, 1
			if err := w.core.Store.SavePRStatus(source.pr.URL, st); err != nil {
				t.Fatal(err)
			}
			if err := w.core.Notice(b); err != nil {
				t.Fatal(err)
			}
			events, _ := w.core.Store.GoalEvents(g.ID)
			if !slices.Contains(kinds(events), EventCIGreen) {
				t.Fatalf("no CI event: %v", kinds(events))
			}
			if w.core.inFlight(b, b.Org, m) {
				t.Fatal("completed CI still counts as in flight")
			}
		})
	}
}

func TestPushDestinationsComeFromSuccessfulGitUpdates(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		want         []PRBranch
	}{
		{"refspec", "To https://github.com/me/app.git\n   old..new  HEAD -> topic\n", []PRBranch{{"me/app", "topic"}}},
		{"new branch", "To git@github.com:me/app.git\n * [new branch] local -> remote\n", []PRBranch{{"me/app", "remote"}}},
		{"rejected", "To git@github.com:me/app.git\n ! [rejected] local -> remote (non-fast-forward)\n", nil},
		{"deleted", "To git@github.com:me/app.git\n - [deleted] (none) -> remote\n", nil},
		{"tag", "To git@github.com:me/app.git\n * [new tag] v1 -> v1\n", nil},
		{"other host", "To git@gitlab.com:me/app.git\n old..new HEAD -> topic\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f Facts
			f.Pushed("git push origin HEAD:topic", tc.output)
			f.Pushed("git push origin HEAD:topic", tc.output)
			if !slices.Equal(f.Pushes, tc.want) {
				t.Fatalf("pushes %v, want %v", f.Pushes, tc.want)
			}
		})
	}
}
