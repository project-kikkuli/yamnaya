package core

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
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

// slugTokens are the lowercase words of text.
func slugTokens(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
}

// firstOf joins the first of tokens, at most words of them and 40 characters, into a name, "" when that is not one.
func firstOf(tokens []string, words int) string {
	var parts []string
	for _, w := range tokens {
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

// withoutIDs drops tokens shaped like a goalNN or mgrNN id: a name that carries one defeats having a name.
func withoutIDs(tokens []string) []string {
	return slices.DeleteFunc(slices.Clone(tokens), func(w string) bool { return idShaped.MatchString(w) })
}

// Slug is text as a kebab-case name of at most words words and 40 characters, without ids, "" when nothing usable is left.
func Slug(text string, words int) string {
	return firstOf(withoutIDs(slugTokens(text)), words)
}

// opener are the words an ask opens with before it says anything, and ending the words that leave a name unfinished when it stops on them.
var (
	opener = fieldSet("i d ll m ve s t re we hey hi hello ok okay so please let lets can could would you need want like to be the a an there are is am was were have has been " +
		"owner approved reported request requested ask via")
	ending = fieldSet("a an the of to for in on at by via and or with from that this is are be my our")
)

func fieldSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(s) {
		out[w] = true
	}
	return out
}

// goalSlug is tokens as a goal's name: no ids, no opening filler, at most 4 words, not stopping on a word that leaves it unfinished.
func goalSlug(tokens []string) string {
	tokens = withoutIDs(tokens)
	for len(tokens) > 0 && opener[tokens[0]] {
		tokens = tokens[1:]
	}
	s := firstOf(tokens, 4)
	for {
		i := strings.LastIndex(s, "-")
		if i < 0 || !ending[s[i+1:]] {
			return s
		}
		s = s[:i]
	}
}

// askSlug is the fallback name for a goal: the first words of its ask that are not boilerplate.
func askSlug(ask string) string {
	if s := goalSlug(slugTokens(substance(ask))); s != "" {
		return s
	}
	if s := Slug(ask, 4); s != "" {
		return s
	}
	return "goal"
}

// boilerplate opens an ask with what any brief says: the standing preambles, whom the agent is, rules and how the ask got here.
var boilerplate = regexp.MustCompile(`(?i)^\s*(yamnaya is a personal repo\b[^\n]*(\n|$)|fix forward\.|diagnose before acting\.|no reverts\b[^.\n]*\.|never block on a wait\b[^.\n]*\.|you are\s[^.\n]*(\.|\n|$)|hard rule\b[^\n]*(\n|$)|(design|investigation) only\.|no implementation\b[^.\n]*\.|(owner[- ](approved|ask|report(ed)?|request(ed)?)|urgent)\b(\s*\([^)\n]*\))?[^:\n]*:)`)

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
	if st.AuthorName != "" {
		return chiefPhrase(st.AuthorName)
	}
	return chiefPhrase(o.RefOf(st.Author))
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
func (c *Core) goalName(given, ask string) (name, source string, err error) {
	if given != "" {
		return given, NamedByHand, CheckName(given)
	}
	name, source = c.suggest(ask)
	return name, source, nil
}

// suggest is the name the namer gives ask, else its first words, and which of those it is.
func (c *Core) suggest(ask string) (name, source string) {
	if s := c.namerSlug(ask); s != "" {
		return s, NamedByNamer
	}
	return askSlug(ask), NamedByFallback
}

// namerSlug is what the namer makes of ask, when that is a single slug of at least 2 words left once its ids and filler are gone; else "".
func (c *Core) namerSlug(ask string) string {
	if c.Namer == nil {
		return ""
	}
	reply := strings.TrimSpace(c.Namer(substance(ask)))
	if !validName.MatchString(reply) {
		return ""
	}
	if s := goalSlug(strings.Split(reply, "-")); strings.Contains(s, "-") {
		return s
	}
	return ""
}

// HaikuName asks a small Claude model, on the login whose config lives at root ("" for the default one), for a 2 to 4 word name for
// ask; "" when it cannot. It runs from the root directory, so the name comes from the ask and not from where ym happens to run.
func HaikuName(ask, root string) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	prompt := "Name this task in 2 to 4 lowercase words joined by hyphens, like fix-export-streaming. Reply with the name only.\n\nTask:\n" + clip(ask, 2000)
	cmd := exec.CommandContext(ctx, "claude", "-p", "--model", "haiku", prompt)
	cmd.Dir = "/"
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") })
	if root != "" {
		cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+root)
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// How a goal came by its name. A goal named before these were recorded has none.
const (
	NamedByHand     = "hand"
	NamedByNamer    = "namer"
	NamedByFallback = "fallback"
)

// setName writes the name pick settles on to a goal, with how it came by it, and its manager, or with no goal to the chief, in one write transaction, so
// two writers cannot settle on one name. pick sees the row's name now and the names others use here; "" writes nothing.
// It is the only write of a name after a row is added: a whole-row save leaves the name alone, so neither can undo the other.
func (s *Store) setName(goal, chief, source string, pick func(now string, taken map[string]bool) (string, error)) (string, error) {
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
		if _, err = tx.Exec(`UPDATE ym_goals SET name=?, name_source=? WHERE num=?`, name, source, num); err != nil {
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
	if _, err := c.Store.setName(goal, chief, NamedByHand, func(_ string, taken map[string]bool) (string, error) {
		if taken[name] {
			return "", fmt.Errorf("%s already names a goal or chief here", name)
		}
		return name, nil
	}); err != nil {
		return "", err
	}
	return old, c.renamed(m, g, isGoal, title, name, old, m.Shown())
}

// renamed carries a new name to what holds a copy of it: the brief page's title and the items from the goal or chief.
func (c *Core) renamed(m Manager, g Goal, isGoal bool, title, name string, olds ...string) error {
	if err := retitle(cmp.Or(m.BriefPath, g.BriefPath), title); err != nil {
		return err
	}
	var errs []error
	if isGoal {
		errs = append(errs, c.renameInItems(g.ID, name, olds), c.Store.renameInGoal(g.ID, name, olds))
	}
	if m.ID != "" {
		errs = append(errs, c.renameInItems(m.ID, name, olds), c.Store.renameInManager(m.ID, name, olds))
	}
	return errors.Join(errs...)
}

// nameIn is text with each old name or id of a goal or chief as name; a longer name that merely starts with one stays.
func nameIn(text, name string, olds []string) string {
	for _, old := range olds {
		if old == "" || old == name {
			continue
		}
		re := regexp.MustCompile(`(^|[^a-z0-9-])` + regexp.QuoteMeta(old) + `($|[^a-z0-9-])`)
		text = re.ReplaceAllStringFunc(text, func(hit string) string { return strings.Replace(hit, old, name, 1) })
	}
	return text
}

var nameInBrackets = regexp.MustCompile(`([a-z0-9-]+) \(([a-z0-9-]+)\)`)

// noStutter is a from line with no "manager x (x)", which a rename makes of a line from before names. An id it still holds stays stored:
// it is how a later rename finds who the line means.
func noStutter(from string) string {
	return nameInBrackets.ReplaceAllStringFunc(from, func(hit string) string {
		if m := nameInBrackets.FindStringSubmatch(hit); m[1] == m[2] {
			return m[1]
		}
		return hit
	})
}

// oneName is a from line as a person reads it, with a name once and no id.
func oneName(from string) string {
	return nameInBrackets.ReplaceAllStringFunc(noStutter(from), func(hit string) string {
		if m := nameInBrackets.FindStringSubmatch(hit); idShaped.MatchString(m[2]) {
			return m[1]
		}
		return hit
	})
}

// renameInGoal carries a new name to the text ym stored on the goal's row: why it stands as it does.
func (s *Store) renameInGoal(id, name string, olds []string) error {
	defer s.invalidate()
	var evidence string
	if err := s.db.QueryRow(`SELECT evidence FROM ym_goals WHERE 'goal' || num = ?`, id).Scan(&evidence); err != nil {
		return err
	}
	if now := nameIn(evidence, name, olds); now != evidence {
		_, err := s.db.Exec(`UPDATE ym_goals SET evidence = ? WHERE 'goal' || num = ?`, now, id)
		return err
	}
	return nil
}

// renameInManager carries a new name to the error ym stored on the manager's or chief's row.
func (s *Store) renameInManager(id, name string, olds []string) error {
	defer s.invalidate()
	var last string
	if err := s.db.QueryRow(`SELECT last_error FROM ym_managers WHERE 'mgr' || num = ?`, id).Scan(&last); err != nil {
		return err
	}
	if now := nameIn(last, name, olds); now != last {
		_, err := s.db.Exec(`UPDATE ym_managers SET last_error = ? WHERE 'mgr' || num = ?`, now, id)
		return err
	}
	return nil
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
		base, source := c.suggest(g.Ask)
		name, err := c.Store.setName(g.ID, "", source, func(now string, taken map[string]bool) (string, error) {
			if now != "" {
				return "", nil
			}
			return unique(base, taken), nil
		})
		note(name, err)
		if name != "" && err == nil {
			errs = append(errs, c.renamed(o.Managers[g.ManagerID], g, true, name+": "+shortName(g.Ask), name, g.ID, g.ManagerID, o.Managers[g.ManagerID].Name))
		}
	}
	for _, m := range o.Managers {
		if m.Host != "" || m.Role != RoleChief || validName.MatchString(m.Name) {
			continue
		}
		name, err := c.Store.setName("", m.ID, "", func(now string, taken map[string]bool) (string, error) {
			if validName.MatchString(now) {
				return "", nil
			}
			base := Slug(now, 6)
			if base == "" {
				base = "chief"
			}
			return unique(base, taken), nil
		})
		note(name, err)
		if name != "" && err == nil {
			errs = append(errs, c.renamed(m, Goal{}, false, name, name, m.ID, m.Name))
		}
	}
	return named, errors.Join(errs...)
}

// legacyBoilerplate is what askSlug skipped before names recorded how they came about, and legacySlug what it made, so a name from
// then can be told from one a person chose.
var legacyBoilerplate = regexp.MustCompile(`(?i)^\s*(yamnaya is a personal repo\b[^\n]*(\n|$)|fix forward\.|diagnose before acting\.|no reverts\b[^.\n]*\.|never block on a wait\b[^.\n]*\.|you are\s[^.\n]*(\.|\n|$)|hard rule\b[^\n]*(\n|$)|(design|investigation) only\.|no implementation\b[^.\n]*\.|(owner approved|owner ask|owner report|urgent)\b(\s*\([^)\n]*\))?[^:\n]*:)`)

func legacySlug(ask string) string {
	rest := ask
	for {
		loc := legacyBoilerplate.FindStringIndex(rest)
		if loc == nil {
			break
		}
		rest = rest[loc[1]:]
	}
	if strings.TrimSpace(rest) == "" {
		rest = ask
	}
	if s := firstOf(slugTokens(rest), 4); s != "" {
		return s
	}
	return "goal"
}

var numericSuffix = regexp.MustCompile(`-[0-9]+$`)

// autoNamed reports whether g's name came from its ask's first words: recorded so, or from before that was recorded and the same words.
// A name that cannot be told to be from the ask counts as a person's.
func autoNamed(g Goal) bool {
	if g.NameSource != "" {
		return g.NameSource == NamedByFallback
	}
	return g.Name == legacySlug(g.Ask) || numericSuffix.ReplaceAllString(g.Name, "") == legacySlug(g.Ask)
}

// giveUpAfter is how many refusals in a row end a pass of the namer: past that the namer is out, not the asks.
const giveUpAfter = 3

// BetterNames gives each goal named from its ask's first words, because the namer failed, the name the namer gives it now. A goal the namer
// has named, or a person has, stays as it is, as does every chief.
func (c *Core) BetterNames() (int, error) {
	if c.Namer == nil {
		return 0, nil
	}
	o, err := c.LoadOrg()
	if err != nil {
		return 0, err
	}
	named, refused := 0, 0
	var errs []error
	for _, id := range sortedGoalIDs(o) {
		g := o.Goals[id]
		if g.Host != "" || g.Name == "" || !autoNamed(g) {
			continue
		}
		base := c.namerSlug(g.Ask)
		if base == "" {
			if refused++; refused == giveUpAfter {
				break
			}
			continue
		}
		refused = 0
		name, err := c.Store.setName(g.ID, "", NamedByNamer, func(now string, taken map[string]bool) (string, error) {
			if now != g.Name {
				return "", nil
			}
			return unique(base, taken), nil
		})
		if err != nil || name == "" {
			errs = append(errs, err)
			continue
		}
		named++
		errs = append(errs, c.renamed(o.Managers[g.ManagerID], g, true, name+": "+shortName(g.Ask), name, g.Name))
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
func (c *Core) renameInItems(id, name string, olds []string) error {
	items, err := c.Store.Items(ItemFilter{})
	if err != nil {
		return err
	}
	for _, it := range items {
		d := it.Dossier
		owned := fromOwner(it) == id
		if d.Goal != id && d.Chief != id && !owned {
			continue
		}
		if owned {
			d = ownedBy(it, id, name)
		}
		if d.Goal == id || d.Chief == id {
			d.GoalName = name
		}
		d.From = noStutter(nameIn(d.From, name, olds))
		if err := c.Store.SaveDossier(it.Seq, d); err != nil {
			return err
		}
	}
	return nil
}

var fromWithID = regexp.MustCompile(`\b(chief|manager) .+? \((mgr[0-9]+)\)`)

// fromOwner is the manager or chief a stored item comes from when only its words say so: a from line from before names, "chief x (mgr8)",
// or the workspace of the chief that filed it. A dossier from then has no link to it.
func fromOwner(it Item) string {
	if m := fromWithID.FindStringSubmatch(it.Dossier.From); m != nil {
		return m[2]
	}
	if n := ScopeName(it.Scope); strings.HasPrefix(n, "mgr") && idShaped.MatchString(n) && it.Dossier.Goal == "" && it.Dossier.Chief == "" {
		return n
	}
	return ""
}

// ownedBy is it's dossier linked to the manager or chief id, its from line by name, so a later rename reaches it by the link.
func ownedBy(it Item, id, name string) Dossier {
	d := it.Dossier
	if m := fromWithID.FindStringSubmatch(d.From); m != nil && m[2] == id {
		d.From = strings.Replace(d.From, m[0], m[1]+" "+name, 1)
	} else if strings.HasPrefix(d.From, "chief ") {
		d.From = "chief " + name
	}
	if d.Goal == "" {
		d.Chief, d.GoalName = id, name
	}
	return d
}

// SettleItemFroms links every stored item whose from line or workspace is all that says who it comes from, once that one has a name.
func (c *Core) SettleItemFroms() (int, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return 0, err
	}
	items, err := c.Store.Items(ItemFilter{})
	if err != nil {
		return 0, err
	}
	settled := 0
	var errs []error
	for _, it := range items {
		m, ok := o.Managers[fromOwner(it)]
		if !ok || !validName.MatchString(m.Name) {
			continue
		}
		err := c.Store.SaveDossier(it.Seq, ownedBy(it, m.ID, m.Name))
		if errs = append(errs, err); err == nil {
			settled++
		}
	}
	return settled, errors.Join(errs...)
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

// RefOf is how a person reads the goal, manager or chief with this id: its name, under its host when that is another one. A
// chief on another host with no name known anywhere reads as "chief on <host>", never as its id.
func (o Org) RefOf(id string) string {
	if g, ok := o.Goals[id]; ok {
		return g.Ref()
	}
	if m, ok := o.Managers[id]; ok {
		return m.Ref()
	}
	host, rest, remote := SplitRemote(id)
	if !remote || !idShaped.MatchString(rest) {
		return id
	}
	if host == o.self {
		return o.RefOf(rest)
	}
	if name, ok := o.hostNames[id]; ok {
		return RemoteKey(host, name)
	}
	if strings.HasPrefix(rest, "goal") {
		return "goal on " + host
	}
	return "chief on " + host
}

// chiefPhrase is a chief as RefOf reads it, with the word chief once.
func chiefPhrase(ref string) string {
	if strings.HasPrefix(ref, "chief on ") {
		return ref
	}
	return "chief " + ref
}

// noteHostNames remembers what other hosts' managers, chiefs and goals are called, by host/id: a board or a relay may not come again.
func (s *Store) noteHostNames(names map[string]string) error {
	changed := false
	for ref, name := range names {
		_, id, remote := SplitRemote(ref)
		if !remote || !idShaped.MatchString(id) || CheckName(name) != nil {
			continue
		}
		res, err := s.db.Exec(`INSERT INTO ym_host_names (ref, name) VALUES (?, ?) ON CONFLICT(ref) DO UPDATE SET name = excluded.name WHERE name != excluded.name`, ref, name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			changed = true
		}
	}
	if changed {
		s.invalidate()
	}
	return nil
}

func (s *Store) hostNames() (map[string]string, error) {
	return remembered(s, "host names", cloneMap[string, string], func() (map[string]string, error) {
		rows, err := s.db.Query(`SELECT ref, name FROM ym_host_names`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var ref, name string
			if err := rows.Scan(&ref, &name); err != nil {
				return nil, err
			}
			out[ref] = name
		}
		return out, rows.Err()
	})
}

var chiefFrom = regexp.MustCompile(`\bchief (.+?) \((mgr[0-9]+)\)`)

// namesInSnapshot is what host's item snapshot says its goals and chiefs are called: the names it sends, and those its items carry.
func namesInSnapshot(host string, snap ItemSnapshot) map[string]string {
	out := map[string]string{}
	for id, name := range snap.Names {
		out[RemoteKey(host, id)] = name
	}
	for _, it := range snap.Items {
		d := it.Dossier
		if m := chiefFrom.FindStringSubmatch(d.From); m != nil {
			out[RemoteKey(host, m[2])] = Slug(m[1], 6)
		}
		if d.GoalName != "" && d.Chief != "" {
			out[RemoteKey(host, d.Chief)] = d.GoalName
		} else if d.GoalName != "" && d.Goal != "" {
			out[RemoteKey(host, d.Goal)] = d.GoalName
		}
	}
	return out
}

// LearnHostNames remembers every name the item snapshots this host holds carry.
func (c *Core) LearnHostNames() error {
	snapshots, err := c.Store.itemSnapshots()
	if err != nil {
		return err
	}
	var errs []error
	for host, snap := range snapshots {
		errs = append(errs, c.Store.noteHostNames(namesInSnapshot(host, snap)))
	}
	return errors.Join(errs...)
}

// RefName is how a person reads the goal, manager or chief with this id, here or on another host whose board this one has.
func (c *Core) RefName(id string) string {
	o, err := c.orgWithHosts()
	if err != nil {
		return id
	}
	return o.RefOf(id)
}

// orgWithHosts is the org with every other host's managers, chiefs and goals as its stored board last had them, and as a live read has
// them when this process has one, so any process can name them.
func (c *Core) orgWithHosts() (Org, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return o, err
	}
	boards, err := c.Store.keptOrgBoards()
	if err != nil {
		return o, err
	}
	if c.Remotes != nil {
		boards = append(boards, slices.DeleteFunc(slices.Clone(c.Remotes.Boards()), func(b RemoteBoard) bool { return len(b.Managers) == 0 })...)
	}
	c.mergeRemoteOrg(&o, boards, nil)
	return o, nil
}

// keptOrgBoards are each stored board's managers and goals.
func (s *Store) keptOrgBoards() ([]RemoteBoard, error) {
	rows, err := s.db.Query(`SELECT host, board FROM ym_host_boards`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RemoteBoard
	for rows.Next() {
		var host string
		var raw []byte
		if err := rows.Scan(&host, &raw); err != nil {
			return nil, err
		}
		var kept struct {
			Managers []Manager `json:"managers"`
			Goals    []Goal    `json:"goals"`
		}
		if json.Unmarshal(raw, &kept) == nil {
			out = append(out, RemoteBoard{Host: host, Managers: kept.Managers, Goals: kept.Goals})
		}
	}
	return out, rows.Err()
}

// actorName is how the author of a steer or close carried from another host shows: the name it came with, under that host, else what this
// host knows it by.
func (c *Core) actorName(author, name string) string {
	if name == "" {
		return c.RefName(author)
	}
	if host, _, remote := SplitRemote(author); remote && !strings.Contains(name, "/") {
		if CheckName(name) == nil {
			c.Store.noteHostNames(map[string]string{author: name})
		}
		return RemoteKey(host, name)
	}
	return name
}

// freeFlags take a person's words, or a value that is no goal or chief, never a ref.
var freeFlags = fieldSet("--text --why --reason --summary --remit --evidence --question --rec --name --do --tried --option --as --impact --minutes --repo --host")

// NameArgs is a ym command for host as a person reads it: each argument that is only an id as its name, and without --by and --by-name,
// which are ym's own. A person's words, in a flag's value, an ask or the text of a send, stay as they wrote them. The stored command is
// not this; it keeps its ids.
func (o Org) NameArgs(host string, args []string) []string {
	var out []string
	send := len(args) > 1 && args[1] == "send" || len(args) > 0 && args[0] == "send"
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(out, args[i:]...)
		case a == "--by" || a == "--by-name":
			i++
			continue
		case strings.HasPrefix(a, "--by=") || strings.HasPrefix(a, "--by-name="):
			continue
		case freeFlags[a]:
			out = append(out, a)
			if i++; i < len(args) {
				out = append(out, args[i])
			}
			continue
		}
		ask := i == 1 && args[0] == "goal"
		if !idShaped.MatchString(a) || ask || send && i == len(args)-1 {
			out = append(out, a)
			continue
		}
		ref := a
		if host != "" {
			ref = RemoteKey(host, a)
		}
		shown := o.RefOf(ref)
		if h, name, remote := SplitRemote(shown); remote && h == host {
			shown = name
		}
		out = append(out, shown)
	}
	return out
}

// IDShaped reports whether s reads as a goalNN or mgrNN id.
func IDShaped(s string) bool { return idShaped.MatchString(s) }

// ActorShown is who a goal's closure says closed it, a chief by its name.
func (o Org) ActorShown(actor string) string {
	if id, ok := strings.CutPrefix(actor, "chief "); ok && !strings.HasPrefix(id, "on ") {
		return chiefPhrase(o.RefOf(id))
	}
	return actor
}
