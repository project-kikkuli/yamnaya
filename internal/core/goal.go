package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// NewGoal records the owner's ask for repo and starts the manager that owns it, reporting to the owner.
func (c *Core) NewGoal(ask, repo, harness string, selected ...string) (Goal, Manager, error) {
	return c.NewGoalUnder(ask, repo, harness, "", selected...)
}

// NewGoalUnder is NewGoal with the manager reporting to under, a chief here or host/id on another host; empty reports to the owner.
func (c *Core) NewGoalUnder(ask, repo, harness, under string, selected ...string) (Goal, Manager, error) {
	return c.NewGoalNamed("", ask, repo, harness, under, selected...)
}

// NewGoalNamed is NewGoalUnder with the goal named name; empty names it from its ask.
func (c *Core) NewGoalNamed(name, ask, repo, harness, under string, selected ...string) (Goal, Manager, error) {
	ask = strings.TrimSpace(ask)
	if ask == "" {
		return Goal{}, Manager{}, errors.New("a goal needs the ask in your words")
	}
	harness, err := managerHarness(harness)
	if err != nil {
		return Goal{}, Manager{}, err
	}
	repo = MainCheckout(repo)
	parent, err := c.goalChief(under)
	if err != nil {
		return Goal{}, Manager{}, err
	}
	if host, ok := c.RepoHost(repo); ok {
		out, err := c.RunOrRelay(host, goalArgs(name, ask, repo, harness, firstProfile(selected), c.parentOn(host, parent))...)
		return Goal{Name: name, Ask: ask, Repo: repo, Host: host, Evidence: strings.TrimSpace(out)}, Manager{Host: host, Repo: repo}, err
	}
	name, source, err := c.goalName(name, ask)
	if err != nil {
		return Goal{}, Manager{}, err
	}
	g, err := c.Store.AddGoal(Goal{Name: name, NameSource: source, Ask: ask, Repo: repo, CreatedAt: c.now()})
	if err != nil {
		return g, Manager{}, err
	}
	g.BriefPath = filepath.Join(c.StateDir, "goals", g.ID, "brief.md")
	name = g.Name
	if err := writeBrief(g.BriefPath, "# "+name+": "+shortName(ask)+"\n\nNot started yet: the manager writes this brief.\n"+askSection(ask)); err != nil {
		return g, Manager{}, err
	}
	m, err := c.Store.AddManager(Manager{Role: RoleManager, Name: name, GoalID: g.ID, ParentID: parent, Harness: harness, Profile: firstProfile(selected), Repo: repo, BriefPath: g.BriefPath, LaunchedAt: c.now()})
	if err != nil {
		return g, m, err
	}
	g.ManagerID = m.ID
	if err := c.Store.SaveGoal(g); err != nil {
		return g, m, err
	}
	g.Version++
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

// NewGoalOn starts a goal named name (empty names it from ask) for ask on host, in the repository repo names there, its manager reporting to under. A host ym does not
// reach now gets it through a relay; if that host refuses it for good, requester, a session here, hears of it in an item.
func (c *Core) NewGoalOn(host, name, ask, repo, harness, under, requester string, selected ...string) (Goal, error) {
	ask = strings.TrimSpace(ask)
	if ask == "" {
		return Goal{}, errors.New("a goal needs the ask in your words")
	}
	if repo == "" {
		return Goal{}, fmt.Errorf("a goal on %s needs --repo naming the repository there", host)
	}
	if IsRepoPath(repo) && !filepath.IsAbs(repo) && !strings.HasPrefix(repo, "~") {
		return Goal{}, fmt.Errorf("--repo %s is relative to this directory, which %s does not share; name the repository, or give its full path there", repo, host)
	}
	harness, err := managerHarness(harness)
	if err != nil {
		return Goal{}, err
	}
	parent, err := c.goalChief(under)
	if err != nil {
		return Goal{}, err
	}
	out, err := c.runOrRelay(host, relayYM{Args: goalArgs(name, ask, repo, harness, firstProfile(selected), c.parentOn(host, parent)), Requester: requester})
	return Goal{Ask: ask, Repo: repo, Host: host, Evidence: strings.TrimSpace(out)}, err
}

// goalChief resolves under, a chief here or host/id on another host, to the id a new manager records as its parent.
func (c *Core) goalChief(under string) (string, error) {
	if under == "" {
		return "", nil
	}
	under = c.Store.Unnicked(under)
	if host, rest, remote := SplitRemote(under); remote && host == c.SelfName() {
		under = rest
	} else if remote {
		id, err := c.checkRemoteChief(Manager{}, under)
		if err != nil {
			return "", err
		}
		return id, c.noSuchChief(id)
	}
	o, err := c.LoadOrg()
	if err != nil {
		return "", err
	}
	boss, err := o.lookup(under)
	if err != nil {
		return "", err
	}
	if boss.Role != RoleChief {
		return "", fmt.Errorf("%s is a manager; only a chief has reports", boss.Shown())
	}
	return boss.ID, nil
}

// parentOn is how host names parent, a chief id here or host/id: qualified by this host, or bare when parent is on host itself.
func (c *Core) parentOn(host, parent string) string {
	if parent == "" {
		return ""
	}
	h, r, remote := SplitRemote(parent)
	switch {
	case !remote:
		return RemoteKey(c.SelfName(), parent)
	case h == host:
		return r
	}
	return parent
}

// ChiefOf is the id of the chief session runs as, empty when it is not a chief's.
func (c *Core) ChiefOf(session string) string {
	o, err := c.LoadOrg()
	if err != nil {
		return ""
	}
	if m, ok := o.ManagerOf(c.Resolve(session)); ok && m.Role == RoleChief {
		return m.ID
	}
	return ""
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
	name, remit = strings.TrimSpace(name), strings.TrimSpace(remit)
	if name == "" {
		return Manager{}, errors.New("a chief needs a name")
	}
	harness, err := managerHarness(harness)
	if err != nil {
		return Manager{}, err
	}
	if repo != "" {
		repo = MainCheckout(repo)
	}
	if host, ok := c.RepoHost(repo); ok {
		_, err := c.RunOrRelay(host, c.chiefArgs(name, remit, repo, harness, firstProfile(selected), children)...)
		return Manager{Role: RoleChief, Name: name, Remit: remit, Repo: repo, Host: host}, err
	}
	if CheckName(name) != nil {
		if name = Slug(name, 6); name == "" {
			name = "chief"
		}
	}
	m, err := c.Store.AddManager(Manager{Role: RoleChief, Name: name, Remit: remit, Harness: harness, Profile: firstProfile(selected), Repo: repo, LaunchedAt: c.now()})
	if err != nil {
		return m, err
	}
	m.BriefPath = filepath.Join(c.StateDir, "chiefs", m.ID, "brief.md")
	m.Directory = filepath.Join(c.StateDir, "chiefs", m.ID, "workspace")
	if err := writeBrief(m.BriefPath, "# "+m.Name+"\n\n"+remit+"\n"); err != nil {
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
			var to string
			if h, r, remote := SplitRemote(chief); remote {
				to = chief
				if h == host {
					to = r
				}
			} else {
				o, err := c.LoadOrg()
				if err != nil {
					return err
				}
				boss, err := o.lookup(chief)
				if err != nil {
					return err
				}
				to = RemoteKey(c.SelfName(), boss.ID)
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
			return fmt.Errorf("%s is a manager; only a chief has reports", boss.Shown())
		}
		for _, below := range o.Below(m.ID) {
			if below.ID == boss.ID {
				return fmt.Errorf("%s is under %s already; a chief cannot report to its own report", boss.Shown(), m.Shown())
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
	boss, err := o.lookup(chief)
	if err != nil {
		return chief, nil
	}
	if boss.Role != RoleChief {
		return "", fmt.Errorf("%s is a manager; only a chief has reports", chief)
	}
	seen := map[string]bool{}
	for at := boss.ID; at != "" && !seen[at]; at = o.Managers[at].ParentID {
		seen[at] = true
		if at == child.ID {
			return "", fmt.Errorf("%s is under %s already; a chief cannot report to its own report", chief, child.Shown())
		}
	}
	return boss.ID, nil
}

// noSuchChief refuses id, a chief on another host, when that host just answered with chiefs and id is not among them. A host that did
// not answer, or that ym only hears through relays, has no list to check.
func (c *Core) noSuchChief(id string) error {
	if c.Remotes == nil {
		return nil
	}
	host, rest, _ := SplitRemote(id)
	for _, b := range c.Remotes.Boards() {
		if !strings.EqualFold(b.Host, host) || b.Err != nil || b.At.IsZero() {
			continue
		}
		var known []string
		for _, m := range b.Managers {
			if m.ID == rest {
				return nil
			}
			if m.Role == RoleChief && !m.Retired {
				known = append(known, m.Shown())
			}
		}
		if len(known) > 0 {
			return Final(fmt.Errorf("%s has no chief %s; its chiefs are %s", b.Host, rest, strings.Join(known, ", ")))
		}
	}
	return nil
}

// lookup finds a manager or chief by its id or the id of its goal.
// Lookup is the manager or chief ref names: its id, its goal's id, or its or its goal's name.
func (o Org) Lookup(ref string) (Manager, error) { return o.lookup(ref) }

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
	for _, g := range o.Goals {
		if g.Host == "" && g.Name == ref {
			if m, ok := o.Managers[g.ManagerID]; ok {
				return m, nil
			}
		}
	}
	host, rest, qualified := SplitRemote(ref)
	var named []Manager
	for _, m := range o.Managers {
		if strings.EqualFold(m.Name, ref) || qualified && m.Host == host && strings.EqualFold(m.Name, rest) {
			named = append(named, m)
		}
	}
	if i := slices.IndexFunc(named, func(m Manager) bool { return m.Host == "" }); i >= 0 {
		return named[i], nil
	}
	if len(named) > 0 {
		return named[0], nil
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
			targets = o.Below(m.ID)
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
			if sent[t.ID] || t.Session == "" || subtree && t.Retired {
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
	defer c.Store.invalidate()
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	m, ok := o.ManagerOf(c.Resolve(session))
	if !ok {
		return fmt.Errorf("session %s is not a manager or chief", Short(session))
	}
	if g, ok := o.Goals[m.GoalID]; ok {
		_, err := c.Store.db.Exec(`UPDATE ym_goals SET artifact_url=?,version=version+1 WHERE num=?`, url, strings.TrimPrefix(g.ID, "goal"))
		return err
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
	g, err := c.GoalByRef(id)
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
		return g, fmt.Errorf("%s is a chief and owns no goal", m.Shown())
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
	if host, ok := c.RepoHost(m.Repo); ok {
		return c.handOff(o, m, host)
	}
	if m.Role == RoleChief && m.Directory == "" {
		m.Directory = filepath.Join(c.StateDir, "chiefs", m.ID, "workspace")
	}
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
		prompt += "\n\nWhile you were down, " + c.missedText(o, m, events)
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
	st, err := c.Launches.Start(NewLaunch{Prompt: prompt, Harness: m.Harness, Profile: m.Profile, ConfigRoot: m.ConfigRoot, Repo: m.Repo, Directory: m.Directory, Slug: m.Shown(),
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
	return strings.TrimPrefix(chiefText(o, events), "ym: ")
}

// missedText is what a manager or chief coming back reads of what it missed: a chief's as one line per goal.
func (c *Core) missedText(o Org, m Manager, events []GoalEvent) string {
	if _, ok := o.Goals[m.GoalID]; ok {
		return wakeOrChief(o, m, events)
	}
	return strings.TrimPrefix(c.chiefDigest(o, m, events), "ym: ")
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
		return g, fmt.Errorf("%s is a chief and owns no goal", m.Shown())
	}
	return g, nil
}

func firstProfile(selected []string) string {
	if len(selected) > 0 {
		return selected[0]
	}
	return ""
}
