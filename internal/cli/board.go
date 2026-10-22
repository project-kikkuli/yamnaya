package cli

import (
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// boardJSON is this host's board as another host's ym reads it.
type boardJSON struct {
	Sessions []sessionJSON  `json:"sessions"`
	Items    []itemJSON     `json:"items"`
	Launched []launchedJSON `json:"launched"`
	Dormant  []dormantJSON  `json:"dormant"`
	Waiting  []waitingJSON  `json:"waiting"`
	// Self is what this host calls itself; the rest is its goal tree and how each manager and chief stands by this host's reading.
	Self            string            `json:"self,omitempty"`
	Managers        []core.Manager    `json:"managers,omitempty"`
	Goals           []core.Goal       `json:"goals,omitempty"`
	ManagerSessions map[string]string `json:"manager_sessions,omitempty"`
	Standing        map[string]string `json:"standing,omitempty"`
}

type launchedJSON struct {
	Key     string `json:"key"`
	Harness string `json:"harness"`
	Session string `json:"session"`
	State   string `json:"state"`
	Path    string `json:"path"`
	Landed  string `json:"landed,omitempty"`
}

type dormantJSON struct {
	Key     string `json:"key"`
	Harness string `json:"harness"`
	Session string `json:"session"`
	Parent  string `json:"parent,omitempty"`
	Cwd     string `json:"cwd"`
	RepoKey string `json:"repo_key,omitempty"`
	RepoID  string `json:"repo_id,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Started string `json:"started,omitempty"`
}

func boardCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "board",
		Short: "Print this host's sessions, open items, launches, waiting sessions and goal tree as JSON, for another host's ym to show",
		Args:  cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if err := ensureWorkService(); err != nil {
				return err
			}
			e.core.Remotes = nil
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			items, err := e.core.ItemsIn(b, "", false)
			if err != nil {
				return err
			}
			out := boardJSON{Sessions: sessionsJSON(e.core, b), Items: []itemJSON{}, Launched: []launchedJSON{}, Dormant: []dormantJSON{}, Waiting: waitingList(b)}
			for _, it := range items {
				out.Items = append(out.Items, toJSON(it))
			}
			for _, l := range b.Launched {
				out.Launched = append(out.Launched, launchedJSON{Key: l.Key, Harness: l.Harness, Session: l.Session, State: l.State, Path: l.Path, Landed: ts(l.Landed)})
			}
			for _, p := range b.Projects {
				for _, d := range p.Dormant {
					out.Dormant = append(out.Dormant, dormantJSON{Key: d.Key, Harness: d.Harness, Session: d.Session, Parent: d.Parent, Cwd: d.Cwd,
						RepoKey: d.RepoKey, RepoID: core.RepoID(d.RepoKey), Branch: d.Branch, Title: d.Title, State: d.State, Started: ts(d.Started)})
				}
			}
			out.Self, out.ManagerSessions, out.Standing = e.core.SelfName(), b.Org.Sessions, map[string]string{}
			for _, m := range b.Org.Managers {
				out.Managers = append(out.Managers, m)
			}
			sort.Slice(out.Managers, func(i, j int) bool { return out.Managers[i].ID < out.Managers[j].ID })
			for _, g := range b.Org.Goals {
				out.Goals = append(out.Goals, g)
			}
			sort.Slice(out.Goals, func(i, j int) bool { return out.Goals[i].ID < out.Goals[j].ID })
			var stand func(core.Node)
			stand = func(n core.Node) {
				out.Standing[n.Manager.ID] = n.Standing
				for _, c := range n.Children {
					stand(c)
				}
			}
			for _, n := range b.Tree {
				stand(n)
			}
			return writeJSON(cmd.OutOrStdout(), out)
		}),
	}
}

// remoteBoard turns what host printed for ym board into board entries, every key that host numbers on its own qualified with it.
func remoteBoard(host string, j boardJSON) core.RemoteBoard {
	qualify := func(key string) string {
		if key == "" {
			return ""
		}
		return core.RemoteKey(host, key)
	}
	repoID := func(id, key string) string {
		if id == "" {
			id = key
		}
		if filepath.IsAbs(id) {
			return qualify(id)
		}
		return id
	}
	r := core.RemoteBoard{Host: host}
	for _, s := range j.Sessions {
		l := core.Live{Session: core.Session{Harness: s.Harness, ID: s.ID, PID: s.PID, TTY: s.TTY, Cwd: s.Cwd, Repo: s.Repo, RepoKey: qualify(s.RepoKey), RepoID: repoID(s.RepoID, s.RepoKey),
			Worktree: s.Worktree, Branch: s.Branch, Title: s.Title, Status: core.ParseStatus(s.Status), WaitingFor: s.WaitingFor,
			Since: parseTS(s.Since), Background: s.Background}, Project: s.Project, Remote: host, Where: s.Host}
		if s.Prompt != "" {
			l.Prompt = &core.Prompt{Tool: s.Prompt}
		}
		r.Sessions = append(r.Sessions, l)
	}
	for _, it := range j.Items {
		r.Items = append(r.Items, core.Item{Seq: it.Seq, Scope: qualify(it.Scope), ID: it.ID, Alias: it.Alias, Kind: it.Kind, Action: it.Action,
			Question: it.Question, Rec: it.Rec, Options: it.Options, Assumption: it.Assumption, Blocks: it.Blocks, Session: it.Session,
			Harness: it.Harness, CreatedAt: parseTS(it.CreatedAt), State: it.State, Answer: it.Answer, Reason: it.Reason,
			AnsweredAt: parseTS(it.AnsweredAt), Recipient: it.Recipient, Host: host})
	}
	for _, l := range j.Launched {
		r.Launched = append(r.Launched, core.Launched{Key: qualify(l.Key), Harness: l.Harness, Session: l.Session, State: l.State, Path: l.Path, Landed: parseTS(l.Landed)})
	}
	for _, d := range j.Dormant {
		r.Dormant = append(r.Dormant, core.Dormant{Key: qualify(d.Key), Harness: d.Harness, Session: d.Session, Parent: d.Parent, Cwd: d.Cwd,
			RepoKey: qualify(d.RepoKey), RepoID: repoID(d.RepoID, d.RepoKey), Branch: d.Branch, Title: d.Title, State: d.State, Started: parseTS(d.Started)})
	}
	for _, w := range j.Waiting {
		r.Waiting = append(r.Waiting, core.RemoteWaiting{Session: w.Session, Line: w.Line, Reply: w.Reply})
	}
	r.Self, r.Managers, r.Goals, r.ManagerSessions, r.Standing = j.Self, j.Managers, j.Goals, j.ManagerSessions, j.Standing
	return r
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
