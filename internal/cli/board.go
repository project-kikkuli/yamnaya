package cli

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// boardJSON is this host's board as another host's ym reads it.
type boardJSON struct {
	Profiles []core.LoginProfile `json:"profiles,omitempty"`
	Usage    *usageJSON          `json:"usage,omitempty"`
	// At is when ym serve took the snapshot.
	At       string         `json:"at,omitempty"`
	Sessions []sessionJSON  `json:"sessions"`
	Items    []itemJSON     `json:"items"`
	Launched []launchedJSON `json:"launched"`
	Dormant  []dormantJSON  `json:"dormant"`
	Waiting  []waitingJSON  `json:"waiting"`
	// Closed are items answered or dismissed in the last week, with their history, for another host's list of every item.
	Closed []itemJSON `json:"closed,omitempty"`
	// Self is what this host calls itself; the rest is its goal tree and how each manager and chief stands by this host's reading.
	Self            string               `json:"self,omitempty"`
	Managers        []core.Manager       `json:"managers,omitempty"`
	Goals           []core.Goal          `json:"goals,omitempty"`
	ManagerSessions map[string]string    `json:"manager_sessions,omitempty"`
	Standing        map[string]string    `json:"standing,omitempty"`
	Feeds           map[string]core.Feed `json:"feeds,omitempty"`
	// Fresh is each manager's and chief's latest brief and whether it is behind, by this host's reading.
	Fresh map[string]core.Freshness `json:"fresh,omitempty"`
	// Swollen are this host's processes holding a large share of its memory.
	Swollen []core.Swollen `json:"swollen,omitempty"`
}

type launchedJSON struct {
	Profile string `json:"profile,omitempty"`
	Account string `json:"account,omitempty"`
	Key     string `json:"key"`
	Harness string `json:"harness"`
	Session string `json:"session"`
	State   string `json:"state"`
	Path    string `json:"path"`
	Landed  string `json:"landed,omitempty"`
}

type dormantJSON struct {
	Profile string `json:"profile,omitempty"`
	Account string `json:"account,omitempty"`
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
	var build bool
	cmd := &cobra.Command{
		Use:   "board",
		Short: "Print this host's sessions, open items, launches, waiting sessions and goal tree as JSON, for another host's ym to show",
		Long: "board prints the snapshot this host's ym serve writes every cycle, with the time it was taken, and builds nothing, " +
			"so another host's read stays quick however loaded this one is. --build builds the board instead.",
		Args: cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if err := ensureWorkService(); err != nil {
				return err
			}
			if !build {
				dir, err := stateDir()
				if err != nil {
					return err
				}
				raw, err := os.ReadFile(snapshotPath(dir))
				if errors.Is(err, fs.ErrNotExist) {
					return errors.New("no board snapshot yet: this host's ym serve writes one every cycle")
				}
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(raw)
				return err
			}
			e.core.Remotes = nil
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			out, err := hostBoard(e.core, b)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), out)
		}),
	}
	cmd.Flags().BoolVar(&build, "build", false, "build the board now instead of printing ym serve's latest snapshot")
	return cmd
}

// snapshotPath is where ym serve keeps the board it last worked from, for other hosts to read.
func snapshotPath(dir string) string { return filepath.Join(dir, "board.json") }

// writeSnapshot keeps b as this host's board snapshot, stamped with when it was taken.
func writeSnapshot(c *core.Core, dir string, b core.Board) error {
	out, err := hostBoard(c, b)
	if err != nil {
		return err
	}
	out.At = ts(time.Now())
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	path := snapshotPath(dir)
	if err := os.WriteFile(path+".new", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".new", path)
}

// hostBoard is b as another host's ym reads it: this host's own sessions, items, launches and goal tree.
func hostBoard(c *core.Core, b core.Board) (boardJSON, error) {
	items, err := c.ItemsIn(b, "", false)
	if err != nil {
		return boardJSON{}, err
	}
	b.Fleet.Failovers = failoverStates(c.StateDir)
	out := boardJSON{Profiles: b.Profiles, Usage: usageWire(b.Fleet), Sessions: sessionsJSON(c, b), Items: []itemJSON{}, Launched: []launchedJSON{}, Dormant: []dormantJSON{}, Waiting: waitingList(b)}
	for i := range out.Sessions {
		out.Sessions[i].Resume = nil
	}
	if out.Closed, err = closedItems(c); err != nil {
		return out, err
	}
	for _, it := range items {
		out.Items = append(out.Items, withHistory(c, it))
	}
	for _, l := range b.Launched {
		out.Launched = append(out.Launched, launchedJSON{Key: l.Key, Profile: l.Profile, Account: l.Account, Harness: l.Harness, Session: l.Session, State: l.State, Path: l.Path, Landed: ts(l.Landed)})
	}
	for _, p := range b.Projects {
		for _, d := range p.Dormant {
			out.Dormant = append(out.Dormant, dormantJSON{Key: d.Key, Profile: d.Profile, Account: d.Account, Harness: d.Harness, Session: d.Session, Parent: d.Parent, Cwd: d.Cwd,
				RepoKey: d.RepoKey, RepoID: core.RepoID(d.RepoKey), Branch: d.Branch, Title: d.Title, State: d.State, Started: ts(d.Started)})
		}
	}
	out.Self, out.ManagerSessions, out.Standing = c.SelfName(), b.Org.Sessions, map[string]string{}
	if out.Feeds, err = c.Store.Feeds(); err != nil {
		return out, err
	}
	if out.Fresh, err = c.Freshness(b.Org); err != nil {
		return out, err
	}
	out.Swollen = b.Machine.Swollen
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
	return out, nil
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
	r := core.RemoteBoard{Host: host, Profiles: append([]core.LoginProfile(nil), j.Profiles...), Usage: usageFromWire(j.Usage)}
	for i := range r.Profiles {
		r.Profiles[i].Host = host
	}
	for _, s := range j.Sessions {
		l := core.Live{Session: core.Session{Harness: s.Harness, Profile: s.Profile, ProfileLabel: s.ProfileLabel, Account: s.Account, ID: s.ID, PID: s.PID, TTY: s.TTY, Cwd: s.Cwd, Repo: s.Repo, RepoKey: qualify(s.RepoKey), RepoID: repoID(s.RepoID, s.RepoKey),
			Worktree: s.Worktree, Branch: s.Branch, Title: s.Title, Status: core.ParseStatus(s.Status), WaitingFor: s.WaitingFor,
			Since: parseTS(s.Since), Background: s.Background}, Project: s.Project, Remote: host, Where: s.Host}
		if s.Prompt != "" {
			l.Prompt = &core.Prompt{Tool: s.Prompt}
		}
		r.Sessions = append(r.Sessions, l)
	}
	for _, it := range j.Items {
		r.Items = append(r.Items, fromJSON(host, qualify, it))
	}
	for _, it := range j.Closed {
		r.Closed = append(r.Closed, fromJSON(host, qualify, it))
	}
	for _, l := range j.Launched {
		r.Launched = append(r.Launched, core.Launched{Key: qualify(l.Key), Profile: l.Profile, Account: l.Account, Harness: l.Harness, Session: l.Session, State: l.State, Path: l.Path, Landed: parseTS(l.Landed)})
	}
	for _, d := range j.Dormant {
		r.Dormant = append(r.Dormant, core.Dormant{Key: qualify(d.Key), Profile: d.Profile, Account: d.Account, Harness: d.Harness, Session: d.Session, Parent: d.Parent, Cwd: d.Cwd,
			RepoKey: qualify(d.RepoKey), RepoID: repoID(d.RepoID, d.RepoKey), Branch: d.Branch, Title: d.Title, State: d.State, Started: parseTS(d.Started)})
	}
	for _, w := range j.Waiting {
		r.Waiting = append(r.Waiting, core.RemoteWaiting{Session: w.Session, Line: w.Line, Reply: w.Reply})
	}
	r.Self, r.Managers, r.Goals, r.ManagerSessions, r.Standing, r.Feeds, r.Fresh, r.Swollen = j.Self, j.Managers, j.Goals, j.ManagerSessions, j.Standing, j.Feeds, j.Fresh, j.Swollen
	return r
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// fromJSON is an item another host printed, its scope qualified with that host.
func fromJSON(host string, qualify func(string) string, it itemJSON) core.Item {
	return core.Item{Seq: it.Seq, Scope: qualify(it.Scope), ID: it.ID, Alias: it.Alias, Kind: it.Kind, Action: it.Action,
		Question: it.Question, Rec: it.Rec, Options: it.Options, Assumption: it.Assumption, Evidence: it.Evidence, Tried: it.Tried,
		Blocks: it.Blocks, Session: it.Session, Harness: it.Harness, CreatedAt: parseTS(it.CreatedAt), State: it.State, Answer: it.Answer,
		Reason: it.Reason, AnsweredAt: parseTS(it.AnsweredAt), Recipient: it.Recipient, Host: host, History: it.History}
}

// withHistory is it as JSON with every state change it went through.
func withHistory(c *core.Core, it core.Item) itemJSON {
	if full, err := c.Store.Item(it.Key()); err == nil {
		it.History = full.History
	}
	return toJSON(it)
}

// closedWindow is how far back another host's list of every item reaches into answered and dismissed ones.
const closedWindow = 7 * 24 * time.Hour

func closedItems(c *core.Core) ([]itemJSON, error) {
	all, err := c.Store.Items(core.ItemFilter{})
	if err != nil {
		return nil, err
	}
	out := []itemJSON{}
	for _, it := range all {
		at := it.AnsweredAt
		if at.IsZero() {
			at = it.CreatedAt
		}
		if it.State != core.ItemOpen && time.Since(at) < closedWindow {
			out = append(out, withHistory(c, it))
		}
	}
	return out, nil
}
