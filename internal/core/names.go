package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
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
	if s := Slug(ask, 4); s != "" {
		return s
	}
	return "goal"
}

// Shown is how the goal shows: its name, or its id while it has none.
func (g Goal) Shown() string {
	if g.Name != "" {
		return g.Name
	}
	return g.ID
}

// Shown is how a manager or chief shows: its name when that is a name, else its id.
func (m Manager) Shown() string {
	if m.Name != "" && validName.MatchString(m.Name) {
		return m.Name
	}
	return m.ID
}

// taken lists the names in use on this host by goals and chiefs, except the goal or manager except names.
func (o Org) taken(except string) map[string]bool {
	out := map[string]bool{}
	skip := map[string]bool{except: true}
	if g, ok := o.Goals[except]; ok {
		skip[g.ManagerID] = true
	}
	if m, ok := o.Managers[except]; ok {
		skip[m.GoalID] = true
	}
	for id, g := range o.Goals {
		if g.Host == "" && !skip[id] && g.Name != "" {
			out[g.Name] = true
		}
	}
	for id, m := range o.Managers {
		if m.Host == "" && !skip[id] && validName.MatchString(m.Name) {
			out[m.Name] = true
		}
	}
	return out
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

// goalName is the name a new goal for ask gets: given when set, else the namer's slug, else the ask's first words.
func (c *Core) goalName(o Org, given, ask string) (string, error) {
	if given != "" {
		if err := CheckName(given); err != nil {
			return "", err
		}
		if o.taken("")[given] {
			return "", fmt.Errorf("%s already names a goal or chief here", given)
		}
		return given, nil
	}
	return unique(c.suggest(ask), o.taken("")), nil
}

func (c *Core) suggest(ask string) string {
	if c.Namer != nil {
		if s := Slug(c.Namer(ask), 4); s != "" {
			return s
		}
	}
	return askSlug(ask)
}

// HaikuName asks a small Claude model for a 2 to 4 word name for ask; "" when it cannot.
func HaikuName(ask string) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	prompt := "Name this task in 2 to 4 lowercase words joined by hyphens, like fix-export-streaming. Reply with the name only.\n\nTask:\n" + clip(ask, 2000)
	out, err := exec.CommandContext(ctx, "claude", "-p", "--model", "haiku", prompt).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Rename gives the goal or chief ref names the name name; a goal's manager takes the same name.
func (c *Core) Rename(ref, name string) (string, error) {
	if host, _, ok := SplitRemote(ref); ok {
		return "", fmt.Errorf("%s runs on %s; rename it there", ref, host)
	}
	if err := CheckName(name); err != nil {
		return "", err
	}
	o, err := c.LoadOrg()
	if err != nil {
		return "", err
	}
	m, err := o.lookup(ref)
	if err != nil {
		return "", err
	}
	if o.taken(m.ID)[name] {
		return "", fmt.Errorf("%s already names a goal or chief here", name)
	}
	old := m.Shown()
	if g, ok := o.Goals[m.GoalID]; ok {
		old = g.Shown()
		g.Name = name
		if err := c.Store.SaveGoal(g); err != nil {
			return "", err
		}
	} else if m.Role != RoleChief {
		return "", errors.New(m.ID + " is neither a goal's manager nor a chief")
	}
	m.Name = name
	return old, c.Store.SaveManager(m)
}

// NameUnnamed names every goal here that has none, and its manager, as a new goal would be named; a chief keeps its own name
// made kebab-case.
func (c *Core) NameUnnamed() (int, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return 0, err
	}
	named := 0
	for _, id := range sortedGoalIDs(o) {
		g := o.Goals[id]
		if g.Host != "" || g.Name != "" {
			continue
		}
		g.Name = unique(c.suggest(g.Ask), o.taken(g.ID))
		if err := c.Store.SaveGoal(g); err != nil {
			return named, err
		}
		o.Goals[id] = g
		if m, ok := o.Managers[g.ManagerID]; ok {
			m.Name = g.Name
			if err := c.Store.SaveManager(m); err != nil {
				return named, err
			}
			o.Managers[m.ID] = m
		}
		named++
	}
	for _, m := range o.Managers {
		if m.Host != "" || m.Role != RoleChief || validName.MatchString(m.Name) {
			continue
		}
		base := Slug(m.Name, 6)
		if base == "" {
			base = "chief"
		}
		m.Name = unique(base, o.taken(m.ID))
		if err := c.Store.SaveManager(m); err != nil {
			return named, err
		}
		o.Managers[m.ID] = m
		named++
	}
	return named, nil
}

func sortedGoalIDs(o Org) []string {
	ids := slices.Collect(maps.Keys(o.Goals))
	n := func(id string) int64 { v, _ := numOf(id, "goal"); return v }
	slices.SortFunc(ids, func(a, b string) int { return cmp.Compare(n(a), n(b)) })
	return ids
}

// Ref names the manager or chief for a link or command: its name, qualified by its host when it runs on another one.
func (m Manager) Ref() string {
	if m.Host != "" && validName.MatchString(m.Name) {
		return RemoteKey(m.Host, m.Name)
	}
	return m.Shown()
}
