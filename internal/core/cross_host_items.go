package core

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ItemRef is an item as a ref names it: [<host>/]<project>:<ID>, [<host>/]<ID>, or the durable [<host>/]#<seq>.
type ItemRef struct {
	Host, Project, ID string
	Seq               int64
}

func (c *Core) ParseItemRef(ref string) ItemRef {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, c.SelfName()+"/")
	host, rest, ok := c.RemoteRef(ref)
	if !ok {
		rest = ref
	}
	r := parseItemRef(rest)
	r.Host = host
	return r
}

func parseItemRef(ref string) ItemRef {
	if n, ok := strings.CutPrefix(ref, "#"); ok {
		if seq, err := strconv.ParseInt(n, 10, 64); err == nil && seq > 0 {
			return ItemRef{Seq: seq}
		}
	}
	if project, id, ok := strings.Cut(ref, ":"); ok {
		return ItemRef{Project: project, ID: strings.ToUpper(id)}
	}
	return ItemRef{ID: strings.ToUpper(ref)}
}

func (r ItemRef) Matches(it Item) bool {
	if r.Seq != 0 {
		return it.Seq == r.Seq
	}
	if !strings.EqualFold(it.ID, r.ID) && !strings.EqualFold(it.Alias, r.ID) {
		return false
	}
	return r.Project == "" || strings.EqualFold(ScopeName(it.Scope), r.Project) || it.Scope == r.Project
}

func (c *Core) FindItem(scope, ref string) (Item, error) {
	ref = strings.TrimSpace(ref)
	if rest, self := strings.CutPrefix(ref, c.SelfName()+"/"); self {
		return c.Store.Find(scope, rest)
	}
	r := c.ParseItemRef(ref)
	if r.Host != "" {
		found := c.mirrored(r)
		if len(found) == 0 {
			return Item{}, Final(fmt.Errorf("item %s: not found on %s: %w", ref, r.Host, ErrNotFound))
		}
		if len(found) == 1 {
			return found[0], nil
		}
		return Item{}, ambiguousItems(ref, found)
	}
	it, err := c.Store.Find(scope, ref)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return it, err
	}
	if r.Seq != 0 || err == nil && !c.numberedHere(r.Project) {
		return it, err
	}
	found := c.mirrored(r)
	if err == nil {
		found = append([]Item{it}, found...)
	}
	switch len(found) {
	case 0:
		return it, err
	case 1:
		return found[0], nil
	}
	return Item{}, ambiguousItems(ref, found)
}

func ambiguousItems(ref string, found []Item) error {
	var names []string
	for _, f := range found {
		names = append(names, f.Ref())
	}
	return Final(fmt.Errorf("item %s is ambiguous across hosts: %s; name it with its host, as %s", ref, strings.Join(names, ", "), names[len(names)-1]))
}

func (it Item) Ref() string {
	if it.Host != "" {
		return RemoteKey(it.Host, it.Qualified())
	}
	return it.Qualified()
}

func (c *Core) numberedHere(project string) bool {
	if project == "" {
		return false
	}
	o, err := c.LoadOrg()
	if err != nil {
		return false
	}
	_, ok := o.Managers[strings.ToLower(project)]
	return ok
}

func (c *Core) mirrored(r ItemRef) []Item {
	var out []Item
	for _, b := range c.remoteBoards() {
		if r.Host != "" && b.Host != r.Host {
			continue
		}
		for _, it := range slices.Concat(b.Items, b.Closed) {
			if r.Matches(it) {
				it.Host = b.Host
				out = append(out, it)
			}
		}
	}
	return out
}

// Path is who an ask from session climbs through toward the owner, nearest first: a manager's own ask starts at its chief, an
// agent's at its manager. The climb ends at a chief on another host, which that host carries on.
func (o Org) Path(session string, parents map[string]string) []string {
	id := ""
	if m, ok := o.ManagerOf(session); ok {
		id = m.ParentID
	} else if m, _ := o.Managed(session, parents); m.ID != "" {
		id = m.ID
	}
	var path []string
	for id != "" && !slices.Contains(path, id) {
		path = append(path, id)
		m, ok := o.Managers[id]
		if !ok {
			break
		}
		id = m.ParentID
	}
	return path
}

func (c *Core) PathOf(b Board) (func(Item) ([]string, error), error) {
	o, err := c.LoadOrg()
	if err != nil {
		return nil, err
	}
	parents := c.parentsFor(b, nil)
	self := c.SelfName()
	return func(it Item) ([]string, error) {
		if it.Host != "" {
			return it.Path, nil
		}
		session := c.Resolve(it.Session)
		if _, local := o.Managed(session, parents); !local {
			owner, err := c.Store.remoteOwner(session)
			if err == nil && owner.Host == self {
				return []string{RemoteKey(owner.Origin, owner.Manager)}, nil
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
		return o.Path(session, parents), nil
	}, nil
}

func (c *Core) ToMe(b Board, m Manager) ([]Item, error) {
	path, err := c.PathOf(b)
	if err != nil {
		return nil, err
	}
	o, err := c.LoadOrg()
	if err != nil {
		return nil, err
	}
	parents := map[string]string{}
	for id, manager := range o.Managers {
		parents[id] = manager.ParentID
	}
	self := c.SelfName()
	canonical := func(host, id string) string {
		if host == "" || id == "" {
			return id
		}
		return qualifyFrom(self, host, id)
	}
	remotes := c.remoteBoards()
	for _, r := range remotes {
		for _, manager := range r.Managers {
			parents[canonical(r.Host, manager.ID)] = canonical(r.Host, manager.ParentID)
		}
		for id, parent := range r.ItemParents {
			parents[canonical(r.Host, id)] = canonical(r.Host, parent)
		}
	}
	extend := func(host string, path []string) []string {
		var out []string
		for _, id := range path {
			for at := canonical(host, id); at != "" && !slices.Contains(out, at); at = parents[at] {
				out = append(out, at)
			}
		}
		return out
	}
	items, err := c.Store.Items(ItemFilter{States: []string{ItemOpen}})
	if err != nil {
		return nil, err
	}
	out := []Item{}
	for _, it := range items {
		it.Path, err = path(it)
		if err != nil {
			return nil, err
		}
		it.Path = extend("", it.Path)
		if waitsOn(it, m.ID) {
			out = append(out, it)
		}
	}
	for _, r := range remotes {
		for _, it := range r.Items {
			it.Host = r.Host
			it.Recipient = canonical(r.Host, it.Recipient)
			it.Path = extend(r.Host, it.Path)
			if it.State == ItemOpen && waitsOn(it, m.ID) {
				out = append(out, it)
			}
		}
	}
	return out, nil
}

func waitsOn(it Item, id string) bool {
	return it.Recipient == id || it.Recipient == Owner && slices.Contains(it.Path, id)
}

func (c *Core) NewAsks(session string) ([]Item, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return nil, err
	}
	m, ok := o.ManagerOf(c.Resolve(session))
	if !ok {
		return nil, nil
	}
	asks, err := c.ToMe(Board{}, m)
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, it := range asks {
		res, err := c.Store.db.Exec(`INSERT INTO ym_forwarded (id, at) VALUES (?, ?) ON CONFLICT(id) DO NOTHING`, "ask\x00"+c.Resolve(session)+"\x00"+it.Key(), now())
		if err != nil {
			return out, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			out = append(out, it)
		}
	}
	return out, nil
}
