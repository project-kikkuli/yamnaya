package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func procOf(pid int, cmd string) proc.Proc {
	return proc.Proc{PID: pid, PPID: 1, TTY: fmt.Sprintf("/dev/t%d", pid), Args: []string{cmd}}
}

func TestWorktreesOfOneRepoCollapseAndSpawnedSessionsJoinTheirMission(t *testing.T) {
	w := newWorld(t)
	repos := map[string]GitInfo{
		"/src/app":                           {Top: "/src/app", Common: "/src/app/.git", Branch: "main"},
		"/src/app-wt-login":                  {Top: "/src/app-wt-login", Common: "/src/app/.git", Branch: "login"},
		"/src/app/.claude/worktrees/agent-1": {Top: "/src/app/.claude/worktrees/agent-1", Common: "/src/app/.git", Branch: "agent-1"},
		"/src/lib":                           {Top: "/src/lib", Common: "/src/lib.git", Branch: "main"},
	}
	w.core.Git = func(dir string) GitInfo { return repos[dir] }
	add := func(pid int, h *fakeHarness, s Session) {
		w.procs = append(w.procs, procOf(pid, h.name))
		h.sessions[pid] = s
	}
	add(10, w.claude, Session{ID: "main", Cwd: "/src/app", Status: Busy})
	add(11, w.claude, Session{ID: "login", Cwd: "/src/app-wt-login", Status: Idle})
	add(12, w.claude, Session{ID: "agent", Cwd: "/src/app/.claude/worktrees/agent-1", Status: Idle})
	add(13, w.codex, Session{ID: "lib-root", Cwd: "/src/lib", Status: Busy})
	add(14, w.codex, Session{ID: "lib-kid", Cwd: "/src/lib", Parent: "lib-root", Status: Busy})
	add(15, w.claude, Session{ID: "lead", Cwd: "/home/notes", Status: Busy, Crew: []Member{
		{ID: "a", Worktree: "/src/lib"}, {ID: "b", Worktree: "/src/lib"}, {ID: "c", Worktree: "/src/app"},
	}})
	b := w.board(t)
	got := map[string][]string{}
	for _, r := range b.Repos {
		for _, p := range r.Missions {
			got[r.Name] = append(got[r.Name], p.Key)
		}
	}
	if len(got["app"]) != 3 {
		t.Errorf("app missions = %v, want main, login and agent: every worktree is one repo", got["app"])
	}
	if len(got["lib"]) != 2 || len(b.Repos) != 2 {
		t.Errorf("repos = %v, want lib-root (with lib-kid inside) and lead, whose crew worked in lib", got)
	}
	if p, _ := b.Project("lib-root"); len(p.Sessions) != 2 {
		t.Errorf("lib-root sessions = %d, want the spawned codex thread inside its mission", len(p.Sessions))
	}
	if s, _ := b.Session("login"); s.Repo != "app" || s.Branch != "login" || s.Worktree != "/src/app-wt-login" {
		t.Errorf("login session = repo %q branch %q worktree %q", s.Repo, s.Branch, s.Worktree)
	}
}

func TestDescribeRendersToolCallsShort(t *testing.T) {
	cases := []struct {
		tool, input, want string
	}{
		{"Bash", `{"command":"cd /src/app && gh pr create --fill\necho done"}`, "Bash: gh pr create --fill"},
		{"Edit", `{"file_path":"/src/app/internal/core/board.go"}`, "Edit: internal/core/board.go"},
		{"Write", `{"file_path":"/elsewhere/x.go"}`, "Write: /elsewhere/x.go"},
		{"Agent", `{"description":"Fix the parser","prompt":"long"}`, "Agent: Fix the parser"},
		{"Grep", `{"pattern":"func main"}`, "Grep: func main"},
		{"mcp__github__create_pull_request", `{"title":"Add retries"}`, "github.create_pull_request: Add retries"},
		{"exec_command", `{"cmd":"go test ./..."}`, "Bash: go test ./..."},
		{"shell", `{"command":["bash","-lc","ls"]}`, "Bash: bash -lc ls"},
		{"exec", `"await tools.exec_command({cmd:\"git status --short\"})"`, "Bash: git status --short"},
		{"apply_patch", `"*** Begin Patch\n*** Update File: /src/app/a.go\n*** Add File: b.go\n"`, "Edit: a.go, b.go"},
		{"exec", `"text(await tools.exec_command({cmd:` + "`gh pr view ${n} --json state`" + `}))"`, "Bash: gh pr view ${n} --json state"},
		{"exec", `"const cmd = 'go vet ./...';\ntext(await tools.exec_command({cmd,max_output_tokens:4000}))"`, "Bash: go vet ./..."},
		{"exec", `"text(await tools.exec_command({\"cmd\":\"cd /src/app && make\\nmake test\"}))"`, "Bash: make"},
		{"exec", `"text(await tools.write_stdin({session_id:5,chars:\"\"}));\ntext(await tools.exec_command({cmd:\"ls\"}))"`, "Bash: ls"},
		{"exec", `"text(await tools.write_stdin({session_id:5,chars:\"\"}))"`, "Bash: waiting on a running command"},
		{"exec", `"const cmds = [\n  [\"STATE\", \"git status --short\"],\n  [\"LOG\", \"git log -3\"]\n];\nawait Promise.all(cmds.map(([k, cmd]) => tools.exec_command({cmd})))"`, "Bash: git status --short"},
		{"exec", `"const patch = \"*** Begin Patch\\n*** Add File: /src/app/c.go\\n\";\ntext(await tools.apply_patch(patch))"`, "Edit: c.go"},
		{"exec", `"const tools2 = ALL_TOOLS.filter(x => x.name);\ntext(tools2)"`, "exec: const tools2 = ALL_TOOLS.filter(x => x.name);"},
		{"exec", `"text(await tools.apply_patch(\"*** Begin Patch\\n*** Update File: /src/app/a.go\\n\"))"`, "Edit: a.go"},
		{"exec", `"text(await tools.mcp__node_repl__js({title:\"Open the login page\",code:\"x\"}))"`, "node_repl.js: Open the login page"},
		{"TodoWrite", `{}`, "TodoWrite"},
	}
	for _, c := range cases {
		if got := Describe(c.tool, json.RawMessage(c.input), "/src/app", time.Time{}).String(); got != c.want {
			t.Errorf("Describe(%s, %s) = %q, want %q", c.tool, c.input, got, c.want)
		}
	}
}

type countingSource struct {
	mu    sync.Mutex
	calls map[string]int
	fail  map[string]bool
}

func (s *countingSource) PRStatus(url string) (PRStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[url]++
	if s.fail[url] {
		return PRStatus{}, errors.New("gh: not found")
	}
	if url == "https://github.com/me/app/pull/1" {
		return PRStatus{State: "MERGED"}, nil
	}
	if strings.Contains(url, "/fixture/") {
		return PRStatus{State: PRGone}, nil
	}
	return PRStatus{State: "OPEN", Failed: 1, Passed: 3}, nil
}

func TestPRStatusIsFetchedOffTheBoardAndOnlyWhenStale(t *testing.T) {
	w := newWorld(t)
	src := &countingSource{calls: map[string]int{}, fail: map[string]bool{"https://github.com/me/app/pull/3": true}}
	clock := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	w.core.PRs, w.core.Now = src, func() time.Time { return clock }
	w.procs = append(w.procs, procOf(10, "claude"))
	w.claude.sessions[10] = Session{ID: "lead", Status: Busy, Facts: Facts{PRs: []PR{{URL: "https://github.com/me/app/pull/1"}, {URL: "https://github.com/me/app/pull/2"}}},
		Crew: []Member{{ID: "a", Facts: Facts{PRs: []PR{{URL: "https://github.com/me/app/pull/3"}}}}}}
	b := w.board(t)
	if s, _ := b.Session("lead"); s.PRs[0].Status != nil || len(src.calls) != 0 {
		t.Fatalf("building the board fetched PR status: %v", src.calls)
	}
	refresh := func() {
		t.Helper()
		_, _ = w.core.RefreshPRs(w.board(t))
	}
	refresh()
	refresh()
	clock = clock.Add(PRStale)
	refresh()
	want := map[string]int{"https://github.com/me/app/pull/1": 1, "https://github.com/me/app/pull/2": 2, "https://github.com/me/app/pull/3": 2}
	for url, n := range want {
		if src.calls[url] != n {
			t.Errorf("%s fetched %d times, want %d (merged once, open again only when stale, failures throttled too)", url, src.calls[url], n)
		}
	}
	s, _ := w.board(t).Session("lead")
	if st := s.PRs[1].Status; st == nil || st.CI() != "failing" {
		t.Fatalf("PR 2 status on the board = %+v", st)
	}
}

type scriptedSource struct{ next []PRStatus }

func (s *scriptedSource) PRStatus(string) (PRStatus, error) {
	st := s.next[0]
	s.next = s.next[1:]
	return st, nil
}

func TestAFetchThatCannotReadChecksKeepsTheLastReadOfTheSameHead(t *testing.T) {
	read := PRStatus{State: "OPEN", Failed: 1, Passed: 3, Threads: 2, Unresolved: 1, Review: "CHANGES_REQUESTED", Merge: PRMergeFacts{OID: "h1", Mergeable: "MERGEABLE"}}
	limited := func(oid string) PRStatus {
		return PRStatus{State: "OPEN", Title: "Retries", Merge: PRMergeFacts{OID: oid, Error: "gh: API rate limit already exceeded"}}
	}
	for _, tc := range []struct {
		name               string
		then               PRStatus
		failed, unresolved int
	}{
		{"same head", limited("h1"), 1, 1},
		{"a new head", limited("h2"), 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			clock := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
			w.core.PRs, w.core.Now = &scriptedSource{next: []PRStatus{read, tc.then}}, func() time.Time { return clock }
			url := "https://github.com/me/app/pull/2"
			w.procs = append(w.procs, procOf(10, "claude"))
			w.claude.sessions[10] = Session{ID: "lead", Status: Busy, Facts: Facts{PRs: []PR{{URL: url}}}}
			_, _ = w.core.RefreshPRs(w.board(t))
			clock = clock.Add(PRStale)
			_, _ = w.core.RefreshPRs(w.board(t))
			all, err := w.core.Store.PRStatuses()
			if err != nil {
				t.Fatal(err)
			}
			st := all[url]
			if st.Failed != tc.failed || st.Unresolved != tc.unresolved || st.Title != "Retries" || st.Merge.Error == "" || st.ReadyToMerge() {
				t.Fatalf("status after an unreadable fetch %+v", st)
			}
		})
	}
}

func TestAPullRequestLinkIntoARepoGitHubLacksLeavesTheBoard(t *testing.T) {
	w := newWorld(t)
	src := &countingSource{calls: map[string]int{}}
	w.core.PRs = src
	w.procs = append(w.procs, procOf(10, "claude"))
	fixture, real := "https://github.com/fixture/web/pull/9", "https://github.com/me/app/pull/2"
	w.claude.sessions[10] = Session{ID: "lead", Status: Busy, Facts: Facts{PRs: []PR{{URL: fixture}, {URL: real}}},
		Crew: []Member{{ID: "a", Facts: Facts{PRs: []PR{{URL: fixture}}}}}}
	_, _ = w.core.RefreshPRs(w.board(t))
	_, _ = w.core.RefreshPRs(w.board(t))
	s, _ := w.board(t).Session("lead")
	if len(s.PRs) != 1 || s.PRs[0].URL != real || len(s.Crew[0].PRs) != 0 {
		t.Fatalf("lead PRs %+v, crew PRs %+v; want only %s", s.PRs, s.Crew[0].PRs, real)
	}
	if src.calls[fixture] != 1 {
		t.Fatalf("asked GitHub about %s %d times, want once", fixture, src.calls[fixture])
	}
}
