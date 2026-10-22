package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// NewGoal records the owner's ask for repo and starts the manager that owns it.
func (c *Core) NewGoal(ask, repo, harness string) (Goal, Manager, error) {
	ask = strings.TrimSpace(ask)
	if ask == "" {
		return Goal{}, Manager{}, errors.New("a goal needs the ask in your words")
	}
	if harness == "" {
		harness = "claude"
	}
	if harness != "claude" {
		return Goal{}, Manager{}, fmt.Errorf("managers run on claude, not %s", harness)
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
	m, err := c.Store.AddManager(Manager{Role: RoleManager, Name: shortName(ask), GoalID: g.ID, Harness: harness, Repo: repo, BriefPath: g.BriefPath})
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

// NewChief records a chief over children, managers or chiefs by id, and starts its session in repo.
func (c *Core) NewChief(name, remit, repo string, children []string) (Manager, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Manager{}, errors.New("a chief needs a name")
	}
	repo = MainCheckout(repo)
	m, err := c.Store.AddManager(Manager{Role: RoleChief, Name: name, Repo: repo})
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

// Attach puts child, a manager or chief, under chief; an empty chief reports child to the owner again.
func (c *Core) Attach(child, chief string) error {
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	m, err := o.lookup(child)
	if err != nil {
		return err
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
	for _, ref := range refs {
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

// FinishGoal marks the goal the calling manager owns done; its row waits in the owner's queue until cleared.
func (c *Core) FinishGoal(session, summary string) (Goal, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return Goal{}, err
	}
	g, err := o.goalOf(c.Resolve(session))
	if err != nil {
		return g, err
	}
	g.State, g.Summary, g.DoneAt, g.Evidence, g.AckedAt = GoalDone, strings.TrimSpace(summary), c.now(), "", timeZero
	return g, c.Store.SaveGoal(g)
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

// DropGoal ends a goal the owner no longer wants; its manager stops being supervised.
func (c *Core) DropGoal(ref string) (Goal, error) {
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
		m.Retired = true
		return Goal{}, c.Store.SaveManager(m)
	}
	g.State, g.AckedAt = GoalDropped, c.now()
	if err := c.Store.SaveGoal(g); err != nil {
		return g, err
	}
	m.Retired = true
	return g, c.Store.SaveManager(m)
}

// ReplaceManager ends the session of the manager or chief ref names, if it runs, and starts a fresh one with its prompt and brief.
func (c *Core) ReplaceManager(b Board, ref string) (Manager, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return Manager{}, err
	}
	m, err := o.lookup(ref)
	if err != nil {
		return m, err
	}
	if s, ok := b.Session(m.Session); ok {
		if _, err := c.Kill(s); err != nil {
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

// AckGoal clears a done or stuck goal's row from the owner's queue.
func (c *Core) AckGoal(id string) error {
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
func (c *Core) launch(b Board, o Org, m Manager) Manager {
	m.LaunchedAt, m.Nudges, m.NudgedAt = c.now(), 0, timeZero
	if c.Launches == nil {
		m.LastError = "ym's launcher is unavailable"
		c.Store.SaveManager(m)
		return m
	}
	session := uuid.NewString()
	waiting := c.waitingFor(o, m)
	prompt := c.prompt(m, o, waiting)
	_, err := c.Launches.Start(NewLaunch{Prompt: prompt, Harness: m.Harness, Repo: m.Repo, Slug: m.ID + "-" + slugWords(m.Name), Session: session})
	if err != nil {
		m.LastError = err.Error()
		c.Store.SaveManager(m)
		return m
	}
	for _, msg := range waiting {
		c.Store.Claim(msg.Seq, MsgInbox)
	}
	m.Session, m.LastError = session, ""
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
