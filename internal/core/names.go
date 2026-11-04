package core

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var validName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// CheckName says why name cannot name a goal or chief: kebab-case, 2 to 40 characters, and not shaped like a goalNN or mgrNN id.
func CheckName(name string) error {
	switch {
	case len(name) < 2 || len(name) > 40:
		return fmt.Errorf("a name is 2 to 40 characters, not %d", len(name))
	case !validName.MatchString(name):
		return fmt.Errorf("%q is not kebab-case: lowercase letters and digits joined by single hyphens", name)
	case idShaped.MatchString(name):
		return fmt.Errorf("%q reads as an id; pick a name", name)
	}
	return nil
}

var idShaped = regexp.MustCompile(`^(goal|mgr)[0-9]+$`)

// Slug is text as a kebab-case name of at most words words and 40 characters, "" when nothing usable is left.
func Slug(text string, words int) string {
	var parts []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(parts) == words || len(strings.Join(append(parts, w), "-")) > 40 {
			break
		}
		parts = append(parts, w)
	}
	s := strings.Join(parts, "-")
	if CheckName(s) != nil {
		return ""
	}
	return s
}

// askSlug is the fallback name for a goal: the first words of its ask that are not boilerplate.
func askSlug(ask string) string {
	if s := Slug(substance(ask), 4); s != "" {
		return s
	}
	return "goal"
}

// boilerplate opens an ask with what any brief says: the standing preambles, whom the agent is, rules and how the ask got here.
var boilerplate = regexp.MustCompile(`(?i)^\s*(yamnaya is a personal repo\b[^\n]*(\n|$)|fix forward\.|diagnose before acting\.|no reverts\b[^.\n]*\.|never block on a wait\b[^.\n]*\.|you are\s[^.\n]*(\.|\n|$)|hard rule\b[^\n]*(\n|$)|(design|investigation) only\.|no implementation\b[^.\n]*\.|(owner approved|owner ask|owner report|urgent)\b(\s*\([^)\n]*\))?[^:\n]*:)`)

// substance is the ask from where it starts saying what this goal is about; the whole ask when nothing else is there.
func substance(ask string) string {
	rest := ask
	for {
		loc := boilerplate.FindStringIndex(rest)
		if loc == nil {
			break
		}
		rest = rest[loc[1]:]
	}
	if strings.TrimSpace(rest) == "" {
		return ask
	}
	return rest
}

// Shown is how the goal shows: its name, or its id while it has none.
func (g Goal) Shown() string {
	if g.Name != "" {
		return g.Name
	}
	return g.ID
}

// SteerBy names who wrote st.
func (o Org) SteerBy(st Steer) string {
	if st.Author == Owner {
		return "the owner"
	}
	if m, ok := o.Managers[st.Author]; ok {
		return "chief " + m.Ref()
	}
	return "chief " + st.Author
}

// Shown is how a manager or chief shows: its name when that is a name, else its id.
func (m Manager) Shown() string {
	if m.Name != "" && validName.MatchString(m.Name) {
		return m.Name
	}
	return m.ID
}

// unique is base, or base with the first numeric suffix no goal or chief here uses.
func unique(base string, taken map[string]bool) string {
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		suffix := "-" + strconv.Itoa(n)
		s := strings.TrimRight(base[:min(len(base), 40-len(suffix))], "-") + suffix
		if !taken[s] {
			return s
		}
	}
}

// goalName is the name a new goal for ask starts from: given when set, else the namer's slug, else the ask's first words.
// The store settles it against the names in use when it saves the goal, as the namer can take a while.
func (c *Core) goalName(given, ask string) (string, error) {
	if given != "" {
		return given, CheckName(given)
	}
	return c.suggest(ask), nil
}

func (c *Core) suggest(ask string) string {
	ask = substance(ask)
	if c.Namer != nil {
		if s := firstWords(strings.TrimSpace(c.Namer(ask)), 4); strings.Contains(s, "-") && CheckName(s) == nil {
			return s
		}
	}
	return askSlug(ask)
}

// firstWords is a kebab-case slug cut to its first n words; anything else comes back as it is.
func firstWords(slug string, n int) string {
	if !validName.MatchString(slug) {
		return slug
	}
	words := strings.Split(slug, "-")
	return strings.Join(words[:min(n, len(words))], "-")
}

// HaikuName asks a small Claude model for a 2 to 4 word name for ask; "" when it cannot. It runs from the root directory, so the
// name comes from the ask and not from where ym happens to run.
func HaikuName(ask string) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	prompt := "Name this task in 2 to 4 lowercase words joined by hyphens, like fix-export-streaming. Reply with the name only.\n\nTask:\n" + clip(ask, 2000)
	cmd := exec.CommandContext(ctx, "claude", "-p", "--model", "haiku", prompt)
	cmd.Dir = "/"
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// setName writes the name pick settles on to a goal and its manager, or with no goal to the chief, in one write transaction, so
// two writers cannot settle on one name. pick sees the row's name now and the names others use here; "" writes nothing.
// It is the only write of a name after a row is added: a whole-row save leaves the name alone, so neither can undo the other.
func (s *Store) setName(goal, chief string, pick func(now string, taken map[string]bool) (string, error)) (string, error) {
	defer s.invalidate()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	taken, err := namesInUse(tx, goal, chief)
	if err != nil {
		return "", err
	}
	var now, name string
	if goal != "" {
		num, err := numOf(goal, "goal")
		if err != nil {
			return "", err
		}
		if err := tx.QueryRow(`SELECT name FROM ym_goals WHERE num=?`, num).Scan(&now); err != nil {
			return "", err
		}
		if name, err = pick(now, taken); err != nil || name == "" {
			return "", err
		}
		if _, err = tx.Exec(`UPDATE ym_goals SET name=? WHERE num=?`, name, num); err != nil {
			return "", err
		}
		if _, err = tx.Exec(`UPDATE ym_managers SET name=? WHERE goal_id=?`, name, goal); err != nil {
			return "", err
		}
		return name, tx.Commit()
	}
	num, err := numOf(chief, "mgr")
	if err != nil {
		return "", err
	}
	if err := tx.QueryRow(`SELECT name FROM ym_managers WHERE num=?`, num).Scan(&now); err != nil {
		return "", err
	}
	if name, err = pick(now, taken); err != nil || name == "" {
		return "", err
	}
	if _, err = tx.Exec(`UPDATE ym_managers SET name=? WHERE num=?`, name, num); err != nil {
		return "", err
	}
	return name, tx.Commit()
}

// namesInUse lists the names goals and chiefs use here, but for those of the goal, its managers, and the chief given.
func namesInUse(tx *sql.Tx, goal, chief string) (map[string]bool, error) {
	rows, err := tx.Query(`SELECT name FROM ym_goals WHERE name != '' AND 'goal' || num != ?1
		UNION SELECT name FROM ym_managers WHERE name != '' AND 'mgr' || num != ?2 AND (goal_id = '' OR goal_id != ?1)`, goal, chief)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// Rename gives the goal or chief ref names the name name; a goal's manager takes the same name, and its brief.md the new title.
func (c *Core) Rename(ref, name string) (string, error) {
	if err := CheckName(name); err != nil {
		return "", err
	}
	if host, rest, ok := SplitRemote(ref); ok {
		_, err := c.RunRemote(host, "goal", "rename", rest, name)
		return ref, err
	}
	o, err := c.LoadOrg()
	if err != nil {
		return "", err
	}
	m, err := o.lookup(ref)
	if err != nil {
		return "", err
	}
	g, isGoal := o.Goals[m.GoalID]
	if !isGoal && m.Role != RoleChief {
		return "", errors.New(m.Shown() + " is neither a goal's manager nor a chief")
	}
	old, title := m.Shown(), name
	goal, chief := "", m.ID
	if isGoal {
		old, title = g.Shown(), name+": "+shortName(g.Ask)
		goal, chief = g.ID, ""
	}
	if _, err := c.Store.setName(goal, chief, func(_ string, taken map[string]bool) (string, error) {
		if taken[name] {
			return "", fmt.Errorf("%s already names a goal or chief here", name)
		}
		return name, nil
	}); err != nil {
		return "", err
	}
	if err := retitle(m.BriefPath, title); err != nil {
		return "", err
	}
	if isGoal {
		if err := c.renameInItems(g.ID, name); err != nil {
			return "", err
		}
	}
	return old, c.renameInItems(m.ID, name)
}

// retitle puts title on the first line of the brief page at path, which a page always opens with.
func retitle(path, title string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if path == "" || errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	_, rest, _ := strings.Cut(string(raw), "\n")
	return writeFile(path, "# "+title+"\n"+rest)
}

// NameUnnamed names every goal here that has none, and its manager, as a new goal would be named; a chief keeps its own name
// made kebab-case. The fleet keeps working meanwhile: the namer is slow, and a row changed or named in the meantime keeps its change.
func (c *Core) NameUnnamed() (int, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return 0, err
	}
	named := 0
	var errs []error
	note := func(name string, err error) {
		if name != "" && err == nil {
			named++
		}
		errs = append(errs, err)
	}
	for _, id := range sortedGoalIDs(o) {
		g := o.Goals[id]
		if g.Host != "" || g.Name != "" {
			continue
		}
		base := c.suggest(g.Ask)
		note(c.Store.setName(g.ID, "", func(now string, taken map[string]bool) (string, error) {
			if now != "" {
				return "", nil
			}
			return unique(base, taken), nil
		}))
	}
	for _, m := range o.Managers {
		if m.Host != "" || m.Role != RoleChief || validName.MatchString(m.Name) {
			continue
		}
		note(c.Store.setName("", m.ID, func(now string, taken map[string]bool) (string, error) {
			if validName.MatchString(now) {
				return "", nil
			}
			base := Slug(now, 6)
			if base == "" {
				base = "chief"
			}
			return unique(base, taken), nil
		}))
	}
	return named, errors.Join(errs...)
}

func sortedGoalIDs(o Org) []string {
	ids := slices.Collect(maps.Keys(o.Goals))
	n := func(id string) int64 { v, _ := numOf(id, "goal"); return v }
	slices.SortFunc(ids, func(a, b string) int { return cmp.Compare(n(a), n(b)) })
	return ids
}

// ShownOf is how the goal, manager or chief with this id shows.
func (o Org) ShownOf(id string) string {
	if g, ok := o.Goals[id]; ok {
		return g.Shown()
	}
	if m, ok := o.Managers[id]; ok {
		return m.Shown()
	}
	return id
}

// Ref names the manager or chief for a link or command: its name, qualified by its host when it runs on another one.
func (m Manager) Ref() string {
	if m.Host != "" && validName.MatchString(m.Name) {
		return RemoteKey(m.Host, m.Name)
	}
	return m.Shown()
}

// Ref names the goal for a link or message: its name, qualified by its host when it runs on another one.
func (g Goal) Ref() string {
	if g.Host != "" && g.Name != "" {
		return RemoteKey(g.Host, g.Name)
	}
	return g.Shown()
}

// Lead is how a row opens on the item: the goal or chief it comes from by name, else its project, else its reference.
func (it Item) Lead() string {
	if n := it.name(); n != "" {
		return n
	}
	return it.Ref()
}

func (it Item) name() string {
	if it.Dossier.GoalName != "" {
		return it.Dossier.GoalName
	}
	if n := ScopeName(it.Scope); !idShaped.MatchString(n) {
		return n
	}
	return ""
}

// ItemLead is it.Lead by the names the org holds now, so a row follows a rename however old the item.
func (o Org) ItemLead(it Item) string {
	if n := o.ItemName(it); n != "" {
		return n
	}
	return it.Ref()
}

// ItemName is the name ItemLead opens with, "" when the item has none of its own and a row should use its project's.
func (o Org) ItemName(it Item) string {
	on := func(id string) string {
		if _, _, remote := SplitRemote(id); it.Host == "" || remote {
			return id
		}
		return RemoteKey(it.Host, id)
	}
	show := func(name, ref string) string {
		if it.Host != "" {
			return name
		}
		return ref
	}
	if g, ok := o.Goals[on(it.Dossier.Goal)]; ok && g.Name != "" {
		return show(g.Name, g.Ref())
	}
	id := it.Dossier.Chief
	if n := ScopeName(it.Scope); id == "" && idShaped.MatchString(n) {
		id = n
	}
	if m, ok := o.Managers[on(id)]; ok && validName.MatchString(m.Name) {
		return show(m.Name, m.Ref())
	}
	return it.name()
}

// renameInItems keeps the name saved with each item from the goal or chief id, which ym item show prints, in step with a rename.
func (c *Core) renameInItems(id, name string) error {
	items, err := c.Store.Items(ItemFilter{})
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.Dossier.Goal == id || it.Dossier.Chief == id {
			d := it.Dossier
			d.GoalName = name
			if err := c.Store.SaveDossier(it.Seq, d); err != nil {
				return err
			}
		}
	}
	return nil
}

// settleParents turns a chief on another host, stored by name because its board had not shown it yet, into its id once o, merged
// with that board, does. A name that host's board does not have stays as it is.
func (c *Core) settleParents(o Org) error {
	var errs []error
	for id, m := range o.Managers {
		host, rest, remote := SplitRemote(m.ParentID)
		if m.Host != "" || !remote || idShaped.MatchString(rest) {
			continue
		}
		if boss, err := o.lookup(m.ParentID); err == nil && boss.Role == RoleChief && boss.Host == host {
			_, err := c.Store.db.Exec(`UPDATE ym_managers SET parent_id=? WHERE 'mgr' || num = ? AND parent_id=?`, boss.ID, id, m.ParentID)
			c.Store.invalidate()
			errs = append(errs, err)
			m.ParentID = boss.ID
			o.Managers[id] = m
		}
	}
	return errors.Join(errs...)
}

// RefOf is how the manager or chief with this id is named across hosts.
func (o Org) RefOf(id string) string {
	if m, ok := o.Managers[id]; ok {
		return m.Ref()
	}
	return id
}

// ActorShown is who a goal's closure says closed it, a chief by its name.
func (o Org) ActorShown(actor string) string {
	if id, ok := strings.CutPrefix(actor, "chief "); ok {
		return "chief " + o.ShownOf(id)
	}
	return actor
}
