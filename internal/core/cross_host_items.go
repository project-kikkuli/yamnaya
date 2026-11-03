package core

import (
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

// ParseItemRef reads ref; its host is one ym reaches directly or through relays, "" for this one.
func (c *Core) ParseItemRef(ref string) ItemRef {
	ref = strings.TrimSpace(ref)
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
		if seq, err := strconv.ParseInt(n, 10, 64); err == nil {
			return ItemRef{Seq: seq}
		}
	}
	if project, id, ok := strings.Cut(ref, ":"); ok {
		return ItemRef{Project: project, ID: strings.ToUpper(id)}
	}
	return ItemRef{ID: strings.ToUpper(ref)}
}

// Matches reports whether it is the item r names, by key, else by ID or old ID within r's project.
func (r ItemRef) Matches(it Item) bool {
	if r.Seq != 0 {
		return it.Seq == r.Seq
	}
	if !strings.EqualFold(it.ID, r.ID) && !strings.EqualFold(it.Alias, r.ID) {
		return false
	}
	return r.Project == "" || strings.EqualFold(ScopeName(it.Scope), r.Project) || it.Scope == r.Project
}

// FindItem resolves ref from scope: on the host it names, else here, else on the one other host holding it. A name:ID whose name is
// a manager id, which each host numbers on its own, must name one item across every host.
func (c *Core) FindItem(scope, ref string) (Item, error) {
	r := c.ParseItemRef(ref)
	if r.Host != "" {
		found := c.mirrored(r)
		if len(found) == 0 {
			return Item{}, Final(fmt.Errorf("item %s: not found on %s: %w", ref, r.Host, ErrNotFound))
		}
		return found[0], nil
	}
	it, err := c.Store.Find(scope, ref)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return it, err
	}
	if err == nil && !c.numberedHere(r.Project) {
		return it, nil
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
	var names []string
	for _, f := range found {
		names = append(names, f.Ref())
	}
	return Item{}, Final(fmt.Errorf("item %s is ambiguous across hosts: %s; name it with its host, as %s", ref, strings.Join(names, ", "), names[len(names)-1]))
}

// Ref names the item for a person: name:ID, behind its host when another host holds it.
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
	_, ok := o.Managers[project]
	return ok
}

// mirrored are the items other hosts last reported, open or lately closed, that r names; only r's host's when it names one.
func (c *Core) mirrored(r ItemRef) []Item {
	var out []Item
	for _, b := range c.remoteBoards() {
		if r.Host != "" && b.Host != r.Host {
			continue
		}
		for _, it := range slices.Concat(b.Items, b.Closed) {
			if r.Matches(it) {
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

// PathOf is who an item climbs through toward the owner: by this host's org for its own items, as reported for another host's.
func (c *Core) PathOf(b Board) (func(Item) []string, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return nil, err
	}
	parents := c.parentsFor(b, nil)
	return func(it Item) []string {
		if it.Host != "" {
			return it.Path
		}
		return o.Path(c.Resolve(it.Session), parents)
	}, nil
}

// ToMe lists the open asks waiting on m, here and on every other host: those routed to it, and those bound for the owner whose
// path climbs through it.
func (c *Core) ToMe(b Board, m Manager) ([]Item, error) {
	path, err := c.PathOf(b)
	if err != nil {
		return nil, err
	}
	items, err := c.Store.Items(ItemFilter{States: []string{ItemOpen}})
	if err != nil {
		return nil, err
	}
	out := []Item{}
	for _, it := range items {
		if it.Path = path(it); waitsOn(it, m.ID) {
			out = append(out, it)
		}
	}
	me := RemoteKey(c.SelfName(), m.ID)
	for _, r := range c.remoteBoards() {
		for _, it := range r.Items {
			if it.State == ItemOpen && waitsOn(it, me) {
				out = append(out, it)
			}
		}
	}
	return out, nil
}

func waitsOn(it Item, id string) bool {
	return it.Recipient == id || it.Recipient == Owner && slices.Contains(it.Path, id)
}

// NewAsks is ToMe for the manager or chief session runs, less the asks an earlier call returned; none for any other session.
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
		res, err := c.Store.db.Exec(`INSERT INTO ym_forwarded (id, at) VALUES (?, ?) ON CONFLICT(id) DO NOTHING`, "ask\x00"+m.ID+"\x00"+it.Key(), now())
		if err != nil {
			return out, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			out = append(out, it)
		}
	}
	return out, nil
}

// PeerHosts are the hosts this one reaches only through relays.
func (c *Core) PeerHosts() []string { return c.peerHosts() }
