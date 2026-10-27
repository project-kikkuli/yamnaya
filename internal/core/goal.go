package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// NewGoal records the owner's ask for repo and starts the manager that owns it.
func (c *Core) NewGoal(ask, repo, harness string, selected ...string) (Goal, Manager, error) {
	ask = strings.TrimSpace(ask)
	if ask == "" {
		return Goal{}, Manager{}, errors.New("a goal needs the ask in your words")
	}
	harness, err := managerHarness(harness)
	if err != nil {
		return Goal{}, Manager{}, err
	}
	repo = MainCheckout(repo)
	g, err := c.Store.AddGoal(Goal{Ask: ask, Repo: repo, CreatedAt: c.now()})
	if err != nil {
		return g, Manager{}, err
	}
	g.BriefPath = filepath.Join(c.StateDir, "goals", g.ID, "brief.md")
	if err := writeBrief(g.BriefPath, "# "+oneLine(ask)+"\n\nNot started yet: the manager writes this brief.\n"); err != nil {
		return g, Manager{}, err
	}
	m, err := c.Store.AddManager(Manager{Role: RoleManager, Name: shortName(ask), GoalID: g.ID, Harness: harness, Profile: firstProfile(selected), Repo: repo, BriefPath: g.BriefPath, LaunchedAt: c.now()})
	if err != nil {
		return g, m, err
	}
	g.ManagerID = m.ID
	if err := c.Store.SaveGoal(g); err != nil {
		return g, m, err
	}
	if err := c.Store.PickRepo(repo, c.now()); err != nil {
		return g, m, err
	}
	b, _ := c.Board()
	o, err := c.LoadOrg()
	if err != nil {
		return g, m, err
	}
	m = c.launch(b, o, m)
	return g, m, nil
}

// managerHarness is the harness a manager or chief runs on, claude when unnamed.
func managerHarness(harness string) (string, error) {
	switch harness {
	case "":
		return "claude", nil
	case "claude", "codex":
		return harness, nil
	}
	return "", fmt.Errorf("a manager runs on claude or codex, not %s", harness)
}

// NewChief records a chief over children, managers or chiefs by id, and starts its session in repo on harness.
func (c *Core) NewChief(name, remit, repo, harness string, children []string, selected ...string) (Manager, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Manager{}, errors.New("a chief needs a name")
	}
	harness, err := managerHarness(harness)
	if err != nil {
		return Manager{}, err
	}
	repo = MainCheckout(repo)
	m, err := c.Store.AddManager(Manager{Role: RoleChief, Name: name, Harness: harness, Profile: firstProfile(selected), Repo: repo, LaunchedAt: c.now()})
	if err != nil {
		return m, err
	}
	m.BriefPath = filepath.Join(c.StateDir, "chiefs", m.ID, "brief.md")
	if err := writeBrief(m.BriefPath, "# "+name+"\n\n"+remit+"\n"); err != nil {
		return m, err
	}
	if err := c.Store.SaveManager(m); err != nil {
		return m, err
	}
	for _, child := range children {
		if err := c.Attach(child, m.ID); err != nil {
			return m, err
		}
	}
	b, _ := c.Board()
	o, err := c.LoadOrg()
	if err != nil {
		return m, err
	}
	return c.launch(b, o, m), nil
}

// Attach puts child, a manager or chief, under chief; an empty chief reports child to the owner again. Either may run on another
// host, named host/id: a child there is attached on its host, and a chief there is recorded as the child's parent here.
func (c *Core) Attach(child, chief string) error {
	if host, rest, ok := c.remoteHost(child); ok {
		args := []string{"chief", "detach", rest}
		if chief != "" {
			to := RemoteKey(c.SelfName(), chief)
			if h, r, remote := SplitRemote(chief); remote {
				to = chief
				if h == host {
					to = r
				}
			}
			args = []string{"chief", "attach", rest, to}
		}
		_, err := c.RunOrRelay(host, args...)
		return err
	}
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	m, err := o.lookup(child)
	if err != nil {
		return err
	}
	if _, _, remote := SplitRemote(chief); remote {
		boss, err := c.checkRemoteChief(m, chief)
		if err != nil {
			return err
		}
		m.ParentID = boss
		return c.Store.SaveManager(m)
	}
	if chief != "" {
		boss, err := o.lookup(chief)
		if err != nil {
			return err
		}
		if boss.Role != RoleChief {
			return fmt.Errorf("%s is a manager; only a chief has reports", boss.ID)
		}
		for _, below := range o.Subtree(m.ID) {
			if below.ID == boss.ID {
				return fmt.Errorf("%s is under %s already; a chief cannot report to its own report", boss.ID, m.ID)
			}
		}
		chief = boss.ID
	}
	m.ParentID = chief
	return c.Store.SaveManager(m)
}

// checkRemoteChief resolves chief, a manager, chief or goal on another host, to the chief's id, refusing one the board shows is not a
// chief or reports to child already; a chief the board does not show yet is taken as named.
func (c *Core) checkRemoteChief(child Manager, chief string) (string, error) {
	chief = strings.ToLower(chief)
	if c.Remotes == nil {
		return chief, nil
	}
	o, err := c.LoadOrg()
	if err != nil {
		return "", err
	}
	c.mergeRemoteOrg(&o, c.Remotes.Boards(), nil)
	boss, ok := o.Managers[chief]
	if g, isGoal := o.Goals[chief]; !ok && isGoal {
		boss, ok = o.Managers[g.ManagerID]
	}
	if !ok {
		return chief, nil
	}
	if boss.Role != RoleChief {
		return "", fmt.Errorf("%s is a manager; only a chief has reports", chief)
	}
	seen := map[string]bool{}
	for at := boss.ID; at != "" && !seen[at]; at = o.Managers[at].ParentID {
		seen[at] = true
		if at == child.ID {
			return "", fmt.Errorf("%s is under %s already; a chief cannot report to its own report", chief, child.ID)
		}
	}
	return boss.ID, nil
}

// lookup finds a manager or chief by its id or the id of its goal.
func (o Org) lookup(ref string) (Manager, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if m, ok := o.Managers[ref]; ok {
		return m, nil
	}
	if g, ok := o.Goals[ref]; ok {
		if m, ok := o.Managers[g.ManagerID]; ok {
			return m, nil
		}
	}
	for _, m := range o.Managers {
		if strings.EqualFold(m.Name, ref) {
			return m, nil
		}
	}
	return Manager{}, fmt.Errorf("no manager, chief or goal %q: %w", ref, ErrNotFound)
}

// Steer sends text to each manager or chief refs names, and with subtree to everyone below a chief too; owner marks it the owner's
// own words, as against a chief's.
func (c *Core) Steer(b Board, refs []string, subtree, owner bool, text string) (int, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return 0, err
	}
	sent := map[string]bool{}
	var errs []error
	remote := func(host, id string, below bool) {
		if sent[RemoteKey(host, id)] {
			return
		}
		sent[RemoteKey(host, id)] = true
		args := []string{"goal", "send"}
		if below {
			args = append(args, "--subtree")
		}
		if !owner {
			args = append(args, "--relayed")
		}
		if _, err := c.RunOrRelay(host, append(args, id, text)...); err != nil {
			errs = append(errs, fmt.Errorf("%s/%s: %w", host, id, err))
		}
	}
	for _, ref := range refs {
		if host, rest, ok := c.remoteHost(ref); ok {
			remote(host, rest, subtree)
			continue
		}
		m, err := o.lookup(ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		targets := []Manager{m}
		if subtree {
			targets = o.Subtree(m.ID)
		}
		for _, t := range targets {
			if subtree {
				for _, r := range b.Org.Managers {
					if r.Host != "" && r.ParentID == t.ID && !r.Retired {
						_, id, _ := SplitRemote(r.ID)
						remote(r.Host, id, true)
					}
				}
			}
			if sent[t.ID] || t.Session == "" {
				continue
			}
			sent[t.ID] = true
			if err := c.tell(b, t.Session, text, owner); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", t.ID, err))
			}
		}
	}
	return len(sent), errors.Join(errs...)
}

// SetArtifact registers the URL of the richer brief the calling manager publishes.
func (c *Core) SetArtifact(session, url string) error {
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	m, ok := o.ManagerOf(c.Resolve(session))
	if !ok {
		return fmt.Errorf("session %s is not a manager or chief", Short(session))
	}
	if g, ok := o.Goals[m.GoalID]; ok {
		g.ArtifactURL = url
		return c.Store.SaveGoal(g)
	}
	m.ArtifactURL = url
	return c.Store.SaveManager(m)
}

// ReplaceManager ends the session of the manager or chief ref names, if it runs, and starts a fresh one with its prompt and brief.
func (c *Core) ReplaceManager(b Board, ref string) (Manager, error) {
	if host, rest, ok := c.remoteHost(ref); ok {
		_, err := c.RunOrRelay(host, "goal", "relaunch", rest)
		return Manager{ID: ref, Host: host}, err
	}
	o, err := c.LoadOrg()
	if err != nil {
		return Manager{}, err
	}
	m, err := o.lookup(ref)
	if err != nil {
		return m, err
	}
	if s, ok := b.Session(m.Session); ok {
		if _, err := c.Stop(s); err != nil {
			return m, err
		}
	}
	m.Failures, m.NextAttempt = 0, timeZero
	m = c.launch(b, o, m)
	if m.LastError != "" {
		return m, errors.New(m.LastError)
	}
	return m, nil
}

// AckGoal clears a stuck goal's row from the owner's queue, or a done goal from the goal tree.
func (c *Core) AckGoal(id string) error {
	if host, rest, ok := c.remoteHost(id); ok {
		_, err := c.RunOrRelay(host, "goal", "ack", rest)
		return err
	}
	g, err := c.Store.Goal(id)
	if err != nil {
		return err
	}
	g.AckedAt = c.now()
	return c.Store.SaveGoal(g)
}

func (o Org) goalOf(session string) (Goal, error) {
	m, ok := o.ManagerOf(session)
	if !ok {
		return Goal{}, fmt.Errorf("session %s is not a manager; only a goal's manager can finish it", Short(session))
	}
	g, ok := o.Goals[m.GoalID]
	if !ok {
		return g, fmt.Errorf("%s is a chief and owns no goal", m.ID)
	}
	return g, nil
}

// launch starts m's session, fresh with its prompt, and records the attempt; a failure is kept on the manager for the supervisor.
func (c *Core) launch(b Board, o Org, m Manager) Manager { return c.start(b, o, m, false) }

// relaunch brings m back: its own session resumed when ym launched it and can, else a fresh one as launch starts.
func (c *Core) relaunch(b Board, o Org, m Manager) Manager { return c.start(b, o, m, true) }

func (c *Core) start(b Board, o Org, m Manager, resume bool) Manager {
	return c.begin(b, o, m, resume, nil)
}

// begin starts m's session as start does or, with r, a fresh one that reads why ym restarted it, its brief and its goal's state.
func (c *Core) begin(b Board, o Org, m Manager, resume bool, r *restart) Manager {
	m.LaunchedAt, m.Nudges, m.NudgedAt = c.now(), 0, timeZero
	if c.Launches == nil {
		m.LastError = "ym's launcher is unavailable"
		c.Store.SaveManager(m)
		return m
	}
	m.LastError = ""
	if err := c.Store.SaveManager(m); err != nil {
		m.LastError = err.Error()
		return m
	}
	waiting := c.waitingFor(o, m)
	events, heard := c.missed(o, m)
	prompt := c.prompt(m, o, waiting)
	switch {
	case r != nil:
		prompt = c.launchPrompt(m, o) + "\n\n" + c.restartText(b, o, m, *r, waiting, events)
	case len(events) > 0 && m.Session != "":
		prompt += "\n\nWhile you were down, " + wakeOrChief(o, m, events)
	}
	if resume {
		if resumed, ok := c.resumeManager(o, m, waiting, events, prompt); ok {
			if resumed.LastError == "" {
				for _, msg := range waiting {
					c.Store.Claim(msg.Seq, MsgInbox)
				}
				heard(c.now())
			}
			return resumed
		}
	}
	st, err := c.Launches.Start(NewLaunch{Prompt: prompt, Harness: m.Harness, Profile: m.Profile, ConfigRoot: m.ConfigRoot, Repo: m.Repo, Slug: m.ID + "-" + slugWords(m.Name),
		Session: uuid.NewString(), Await: true})
	if err != nil {
		m.LastError = err.Error()
		c.Store.SaveManager(m)
		return m
	}
	for _, msg := range waiting {
		c.Store.Claim(msg.Seq, MsgInbox)
	}
	heard(c.now())
	m.Session, m.LastError = st.Session, ""
	m.Profile, m.ConfigRoot = st.Profile, st.ConfigRoot
	c.Store.SaveManager(m)
	return m
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

func slugWords(s string) string {
	words := strings.Fields(nonWord.ReplaceAllString(strings.ToLower(s), " "))
	if len(words) > 4 {
		words = words[:4]
	}
	return strings.Join(words, "-")
}

func shortName(ask string) string {
	words := strings.Fields(oneLine(ask))
	if len(words) > 6 {
		words = append(words[:6], "…")
	}
	return strings.Join(words, " ")
}

func writeBrief(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// missed lists the events m has not been handed: its goal's, or a chief's subtree's; heard marks them handed.
func (c *Core) missed(o Org, m Manager) ([]GoalEvent, func(time.Time)) {
	if g, ok := o.Goals[m.GoalID]; ok {
		events, _ := c.Store.undelivered(g.ID)
		return events, func(at time.Time) { c.Store.markDelivered(events, at) }
	}
	events, _ := c.chiefEvents(o, m)
	return events, func(at time.Time) { c.Store.markHeard(m.ID, events, at) }
}

func wakeOrChief(o Org, m Manager, events []GoalEvent) string {
	if g, ok := o.Goals[m.GoalID]; ok {
		return strings.TrimPrefix(wakeText(g, events), "ym: ")
	}
	return strings.TrimPrefix(chiefText(o, events, ""), "ym: ")
}

// GoalByRef is the goal a goal, manager or manager name ref names.
func (c *Core) GoalByRef(ref string) (Goal, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return Goal{}, err
	}
	m, err := o.lookup(ref)
	if err != nil {
		return Goal{}, err
	}
	g, ok := o.Goals[m.GoalID]
	if !ok {
		return g, fmt.Errorf("%s is a chief and owns no goal", m.ID)
	}
	return g, nil
}

func firstProfile(selected []string) string {
	if len(selected) > 0 {
		return selected[0]
	}
	return ""
}
