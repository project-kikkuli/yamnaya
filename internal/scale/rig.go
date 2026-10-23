// Package scale builds a synthetic ym installation of a given size, for benchmarks that load the board and serve cycle at scale.
package scale

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/gh"
	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// Spec sizes the installation: Goals managers under chiefs Fanout wide, each running Agents sessions with Subagents each.
type Spec struct {
	Goals     int
	Fanout    int
	Agents    int
	Subagents int
	// Lines is how many transcript records each session has written.
	Lines int
	Repos int
	// Others are processes on the machine that run no agent.
	Others int
}

// Thousand is a thousand goals under two levels of chiefs, three thousand sessions with two subagents each.
var Thousand = Spec{Goals: 1000, Fanout: 10, Agents: 2, Subagents: 2, Lines: 60, Repos: 10, Others: 800}

// Rig is a built installation: its core reads only the rig's files, its process table and its fake terminals.
type Rig struct {
	Spec
	Dir      string
	Core     *core.Core
	Sessions []Session
	Procs    []proc.Proc
	// GitHub counts the gh invocations the core made.
	GitHub *Calls
	// clock is the time every part of the rig reads; Advance moves it.
	clock atomic.Int64
}

// Now is the rig's clock.
func (r *Rig) Now() time.Time { return time.Unix(0, r.clock.Load()) }

// Advance moves the rig's clock on by d, as time passing between board reloads.
func (r *Rig) Advance(d time.Duration) { r.clock.Add(int64(d)) }

// Session is one agent process of the rig and its transcript.
type Session struct {
	ID, Cwd, Transcript string
	PID                 int
	Manager             bool
}

// Calls counts gh invocations by kind.
type Calls struct{ GraphQL, REST atomic.Int64 }

func (c *Calls) Total() int64 { return c.GraphQL.Load() + c.REST.Load() }

// manifest is what Build wrote, so a later Build of the same spec in the same directory reuses it.
type manifest struct {
	Spec     Spec
	Dir      string
	Sessions []Session
	Procs    []proc.Proc
}

// Build writes the installation under dir, or reuses the one an earlier Build of the same spec left there, and returns a core wired to it.
func Build(dir string, s Spec) (*Rig, error) {
	r := &Rig{Spec: s, Dir: dir, GitHub: &Calls{}}
	r.clock.Store(time.Now().UnixNano())
	home := filepath.Join(dir, "claude")
	var kept manifest
	raw, err := os.ReadFile(filepath.Join(dir, "rig.json"))
	reuse := err == nil && json.Unmarshal(raw, &kept) == nil && kept.Spec == s && kept.Dir == dir
	if entries, _ := os.ReadDir(dir); !reuse && len(entries) > 0 {
		return nil, fmt.Errorf("%s holds something other than a rig of this spec; give Build an empty directory", dir)
	}
	st, err := core.OpenStore(filepath.Join(dir, "state", "ym.db"))
	if err != nil {
		return nil, err
	}
	if reuse {
		r.Sessions, r.Procs = kept.Sessions, kept.Procs
		host := &Terminals{panes: map[string]core.Handle{}}
		for i, p := range r.Procs {
			if i < len(r.Sessions) {
				host.panes[p.TTY] = core.Handle{ID: fmt.Sprintf("%%%d", i), Label: "ym:" + r.Sessions[i].ID[:8]}
			}
		}
		r.wire(st, home, host)
		return r, nil
	}
	repos, err := r.repos()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	host := &Terminals{panes: map[string]core.Handle{}}
	pid := 100000
	wt := 0
	worktree := func(repo int) (string, error) {
		wt++
		return r.worktree(repos[repo], wt)
	}
	start := func(cwd string, manager bool, status string, prs []string, seed int) (Session, error) {
		pid++
		sess := Session{ID: uuid.NewString(), Cwd: cwd, PID: pid, Manager: manager}
		sess.Transcript = filepath.Join(home, "projects", encode(cwd), sess.ID+".jsonl")
		if err := writeTranscript(sess.Transcript, cwd, s.Lines, prs, now, seed); err != nil {
			return sess, err
		}
		for k := range s.Subagents {
			base := filepath.Join(strings.TrimSuffix(sess.Transcript, ".jsonl"), "subagents", fmt.Sprintf("agent-a%d", k))
			if err := writeFile(base+".meta.json", fmt.Sprintf(`{"agentType":"general-purpose","description":"Check part %d"}`, k)); err != nil {
				return sess, err
			}
			if err := writeTranscript(base+".jsonl", cwd, s.Lines/4, nil, now, seed+k+1); err != nil {
				return sess, err
			}
		}
		state := map[string]any{"pid": pid, "sessionId": sess.ID, "cwd": cwd, "kind": "interactive", "status": status, "statusUpdatedAt": now.Add(-time.Duration(seed%600) * time.Second).UnixMilli()}
		raw, _ := json.Marshal(state)
		if err := writeFile(filepath.Join(home, "sessions", fmt.Sprint(pid)+".json"), string(raw)); err != nil {
			return sess, err
		}
		tty := fmt.Sprintf("/dev/ttys%04d", len(r.Sessions))
		r.Procs = append(r.Procs, proc.Proc{PID: pid, PPID: 1, TTY: tty, Elapsed: time.Hour, Args: []string{"claude", "--dangerously-skip-permissions", "--resume", sess.ID}})
		host.panes[tty] = core.Handle{ID: fmt.Sprintf("%%%d", len(r.Sessions)), Label: "ym:" + sess.ID[:8]}
		r.Sessions = append(r.Sessions, sess)
		return sess, nil
	}
	statusOf := func(i int) string {
		switch {
		case i%20 == 0:
			return "waiting"
		case i%10 == 1:
			return "busy"
		}
		return "idle"
	}
	chiefs := map[string]string{}
	chief := func(name, parent string, repo int) (string, error) {
		m, err := st.AddManager(core.Manager{Role: core.RoleChief, Name: name, ParentID: parent, Repo: repos[repo]})
		if err != nil {
			return "", err
		}
		cwd, err := worktree(repo)
		if err != nil {
			return "", err
		}
		sess, err := start(cwd, true, "idle", nil, len(r.Sessions))
		if err != nil {
			return "", err
		}
		m.Session, m.LaunchedAt, m.ParentID = sess.ID, now.Add(-time.Hour), parent
		return m.ID, st.SaveManager(m)
	}
	pr := 0
	tx := now.Add(-time.Hour)
	for g := range s.Goals {
		repo := g % s.Repos
		group := g / s.Fanout
		top := group / s.Fanout
		topKey := fmt.Sprintf("top%d", top)
		if chiefs[topKey] == "" {
			if chiefs[topKey], err = chief(fmt.Sprintf("area %d", top), "", repo); err != nil {
				return nil, err
			}
		}
		subKey := fmt.Sprintf("sub%d", group)
		if chiefs[subKey] == "" {
			if chiefs[subKey], err = chief(fmt.Sprintf("team %d", group), chiefs[topKey], repo); err != nil {
				return nil, err
			}
		}
		goal, err := st.AddGoal(core.Goal{Ask: fmt.Sprintf("Ship feature %d end to end with tests", g), Repo: repos[repo], CreatedAt: tx})
		if err != nil {
			return nil, err
		}
		goal.BriefPath = filepath.Join(dir, "state", "goals", goal.ID, "brief.md")
		if err := writeFile(goal.BriefPath, "# feature\n\nMoving.\n"); err != nil {
			return nil, err
		}
		m, err := st.AddManager(core.Manager{Role: core.RoleManager, Name: fmt.Sprintf("feature %d", g), ParentID: chiefs[subKey], GoalID: goal.ID, Repo: repos[repo], BriefPath: goal.BriefPath})
		if err != nil {
			return nil, err
		}
		cwd, err := worktree(repo)
		if err != nil {
			return nil, err
		}
		ms, err := start(cwd, true, statusOf(g), nil, len(r.Sessions))
		if err != nil {
			return nil, err
		}
		m.Session, m.LaunchedAt = ms.ID, tx
		if err := st.SaveManager(m); err != nil {
			return nil, err
		}
		goal.ManagerID = m.ID
		if err := st.SaveGoal(goal); err != nil {
			return nil, err
		}
		for a := range s.Agents {
			pr++
			url := fmt.Sprintf("https://github.com/acme/%s/pull/%d", filepath.Base(repos[repo]), pr)
			cwd, err := worktree(repo)
			if err != nil {
				return nil, err
			}
			as, err := start(cwd, false, statusOf(g*s.Agents+a+3), []string{url}, len(r.Sessions))
			if err != nil {
				return nil, err
			}
			if err := st.Link(as.ID, ms.ID); err != nil {
				return nil, err
			}
			state := "OPEN"
			if pr%3 == 0 {
				state = "MERGED"
			}
			if err := st.SavePRStatus(url, core.PRStatus{State: state, Title: "feature", Passed: 5, Head: "feature", Commits: 3, Fetched: now.Add(-time.Duration(pr%120) * time.Second)}); err != nil {
				return nil, err
			}
			if _, err := st.AddItem(core.Item{Kind: "decision", Question: fmt.Sprintf("Which schema for part %d?", a), Session: as.ID, Harness: "claude", Recipient: m.ID}); err != nil {
				return nil, err
			}
			done, err := st.AddItem(core.Item{Kind: "default", Question: "Keep the old flag?", Session: as.ID, Harness: "claude", Recipient: m.ID})
			if err != nil {
				return nil, err
			}
			if _, err := st.Transition(done.Key(), core.ItemAnswered, "yes"); err != nil {
				return nil, err
			}
			if _, err := st.ClaimDelivery(done.Seq); err != nil {
				return nil, err
			}
		}
		if g%20 == 0 {
			if _, err := st.AddItem(core.Item{Kind: "decision", Question: "Spend the budget on the migration?", Session: ms.ID, Harness: "claude", Recipient: core.Owner}); err != nil {
				return nil, err
			}
		}
	}
	for i := range s.Others {
		r.Procs = append(r.Procs, proc.Proc{PID: 200000 + i, PPID: 1, TTY: fmt.Sprintf("/dev/ttys9%03d", i%40), Elapsed: time.Hour, Args: []string{"-zsh"}})
	}
	raw, err = json.Marshal(manifest{s, dir, r.Sessions, r.Procs})
	if err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(dir, "rig.json"), string(raw)); err != nil {
		return nil, err
	}
	r.wire(st, home, host)
	return r, nil
}

func (r *Rig) wire(st *core.Store, home string, host *Terminals) {
	a := &claude.Adapter{Dir: home, Bin: "claude", Cwd: func(int) string { return "" }, Now: r.Now, Cache: st}
	procs := r.Procs
	r.Core = &core.Core{
		Store:     st,
		Harnesses: []core.Harness{a},
		Hosts:     []core.Host{host},
		Procs:     func() ([]proc.Proc, error) { return procs, nil },
		Git:       core.ReadGit,
		Now:       r.Now,
		PRs:       r.github(),
		StateDir:  filepath.Join(r.Dir, "state"),
	}
}

// Close closes the rig's store.
func (r *Rig) Close() error { return r.Core.Store.Close() }

// Touch appends a turn to session i's transcript, as an agent at work does.
func (r *Rig) Touch(i int) error {
	f, err := os.OpenFile(r.Sessions[i].Transcript, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line(time.Now(), r.Sessions[i].Cwd, i, 3))
	return err
}

var prPath = regexp.MustCompile(`github\.com/([^/]+)/([^/]+)/pull/(\d+)`)

// github is the real gh source with gh itself replaced by canned replies that count each call.
func (r *Rig) github() *gh.Source {
	reply := func(url string) prReply {
		m := prPath.FindStringSubmatch(url)
		return prReply{Owner: m[1], Repo: m[2], Number: m[3]}
	}
	return &gh.Source{
		Now: r.Now,
		Run: func(url string) ([]byte, error) {
			r.GitHub.GraphQL.Add(1)
			return []byte(reply(url).graphQL()), nil
		},
		REST: func(endpoint string) ([]byte, error) {
			r.GitHub.REST.Add(1)
			if endpoint == "user" {
				return []byte(`{"login":"owner"}`), nil
			}
			return []byte(`{"state":"open","title":"feature","merged":false,"draft":false,"user":{"login":"owner"},"head":{"sha":"abc123","ref":"feature"},"commits":3}`), nil
		},
		Repo: func(string) (bool, error) { r.GitHub.REST.Add(1); return true, nil },
		Batch: func(q string, vars map[string]string) ([]byte, error) {
			r.GitHub.GraphQL.Add(1)
			var sb strings.Builder
			sb.WriteString(`{"data":{"viewer":{"login":"owner"}`)
			for k, url := range vars {
				fmt.Fprintf(&sb, `,"p%s":%s`, strings.TrimPrefix(k, "u"), reply(url).resource())
			}
			sb.WriteString(`}}`)
			return []byte(sb.String()), nil
		},
	}
}

type prReply struct{ Owner, Repo, Number string }

func (p prReply) graphQL() string { return `{"data":{"resource":` + p.resource() + `}}` }

func (p prReply) resource() string {
	return `{"state":"OPEN","title":"feature","reviewDecision":"","headRefName":"feature","headRefOid":"abc123","isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN",` +
		`"author":{"login":"owner"},"commits":{"totalCount":3},"reviewThreads":{"totalCount":1,"nodes":[{"isResolved":true}]},` +
		`"last":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"checkRunCountsByState":[{"state":"SUCCESS","count":5}],"statusContextCountsByState":[]}}}}]}}`
}

// repos writes Repos main checkouts with an origin default branch, the files ym reads to place a directory.
func (r *Rig) repos() ([]string, error) {
	var out []string
	for i := range r.Repos {
		top := filepath.Join(r.Dir, "src", fmt.Sprintf("repo%d", i))
		git := filepath.Join(top, ".git")
		for path, text := range map[string]string{
			"HEAD":                     "ref: refs/heads/main\n",
			"refs/remotes/origin/HEAD": "ref: refs/remotes/origin/main\n",
			"refs/heads/main":          "0000000000000000000000000000000000000001\n",
		} {
			if err := writeFile(filepath.Join(git, path), text); err != nil {
				return nil, err
			}
		}
		out = append(out, top)
	}
	return out, nil
}

// worktree adds checkout n of repo on the default branch, as git worktree add leaves it.
func (r *Rig) worktree(repo string, n int) (string, error) {
	name := fmt.Sprintf("wt%d", n)
	top := filepath.Join(repo+"-wt", name)
	gitDir := filepath.Join(repo, ".git", "worktrees", name)
	if err := writeFile(filepath.Join(gitDir, "HEAD"), "ref: refs/heads/main\n"); err != nil {
		return "", err
	}
	if err := writeFile(filepath.Join(gitDir, "commondir"), "../..\n"); err != nil {
		return "", err
	}
	return top, writeFile(filepath.Join(top, ".git"), "gitdir: "+gitDir+"\n")
}

var nonPath = regexp.MustCompile(`[^A-Za-z0-9]`)

func encode(cwd string) string { return nonPath.ReplaceAllString(cwd, "-") }

func writeFile(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// writeTranscript writes lines records of turns: a prompt, a tool call and its result, and a reply that ends the turn.
func writeTranscript(path, cwd string, lines int, prs []string, now time.Time, seed int) error {
	var sb strings.Builder
	at := now.Add(-time.Duration(lines+seed%300) * time.Minute)
	sb.WriteString(fmt.Sprintf(`{"type":"user","origin":{"kind":"human"},"timestamp":%q,"message":{"role":"user","content":"Build part %d of the feature and open a pull request"}}`+"\n", at.Format(time.RFC3339Nano), seed))
	sb.WriteString(fmt.Sprintf(`{"type":"ai-title","aiTitle":"Feature part %d"}`+"\n", seed))
	for i := range lines {
		sb.WriteString(line(at.Add(time.Duration(i)*time.Minute), cwd, seed, i))
	}
	for _, url := range prs {
		m := prPath.FindStringSubmatch(url)
		sb.WriteString(fmt.Sprintf(`{"type":"pr-link","prUrl":%q,"prNumber":%s,"prRepository":"%s/%s"}`+"\n", url, m[3], m[1], m[2]))
	}
	return writeFile(path, sb.String())
}

func line(at time.Time, cwd string, seed, i int) string {
	ts := at.Format(time.RFC3339Nano)
	switch i % 4 {
	case 0:
		return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"model":"claude-opus-5-5","role":"assistant","content":[{"type":"tool_use","id":"t%d-%d","name":"Bash","input":{"command":"go test ./internal/... -run TestPart%d"}}],"stop_reason":"tool_use"}}`+"\n", ts, seed, i, i)
	case 1:
		return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t%d-%d","content":"ok  \tgithub.com/acme/app/internal/part\t0.412s"}]}}`+"\n", ts, seed, i-1)
	case 2:
		return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"model":"claude-opus-5-5","role":"assistant","content":[{"type":"tool_use","id":"e%d-%d","name":"Edit","input":{"file_path":"%s/part%d.go","old_string":"a","new_string":"b"}}],"stop_reason":"tool_use"}}`+"\n", ts, seed, i, cwd, i)
	}
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"Part %d passes its tests; moving to the next part."}],"stop_reason":"end_turn"}}`+"\n", ts, i)
}

// Terminals stands in for ym's tmux server: every rig session has a pane, located by its terminal.
type Terminals struct{ panes map[string]core.Handle }

func (t *Terminals) Name() string { return "tmux" }
func (t *Terminals) Locate(tty string) (core.Handle, bool) {
	h, ok := t.panes[tty]
	return h, ok
}
func (t *Terminals) Focus(core.Handle) (string, error)  { return "", nil }
func (t *Terminals) Paste(core.Handle, string) error    { return nil }
func (t *Terminals) Enter(core.Handle) error            { return nil }
func (t *Terminals) Screen(core.Handle) (string, error) { return "", nil }
