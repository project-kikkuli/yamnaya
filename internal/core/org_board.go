package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// How a goal or chief stands, worst last.
const (
	StandDone   = "done"
	StandMoving = "moving"
	StandQuiet  = "quiet"
	StandDown   = "down"
	StandStuck  = "stuck"
	// StandVerifying is a goal its manager called done while ym checks it.
	StandVerifying = "verifying"
)

var standRank = map[string]int{StandDone: 0, StandVerifying: 1, StandMoving: 1, StandQuiet: 2, StandDown: 3, StandStuck: 4}

// Node is a manager or chief on the board: its goal, its live session, the agents it runs, and the reports below a chief.
type Node struct {
	Manager  Manager
	Goal     *Goal
	Live     *Live
	Agents   []Live
	Children []Node
	// Repo is the goal's repository's identity, as a mission's Project.Repo names it.
	Repo     string
	Standing string
	// Feed is the goal's news for the owner.
	Feed Feed
	// Steer is the newest revision of what the goal or chief is about, nil when it was never steered.
	Steer *Steer
	Fresh Freshness
}

// Unread is how much news the owner has not read on the node's goal, or on every goal below a chief.
func (n Node) Unread() int {
	total := n.Feed.Unread
	for _, c := range n.Children {
		total += c.Unread()
	}
	return total
}

// Count is how many goals the node stands for, itself or its reports'.
func (n Node) Count() int {
	if n.Goal != nil {
		return 1
	}
	total := 0
	for _, c := range n.Children {
		total += c.Count()
	}
	return total
}

// Find is the node with manager id at or below n.
func (n Node) Find(id string) (Node, bool) {
	if n.Manager.ID == id {
		return n, true
	}
	for _, c := range n.Children {
		if f, ok := c.Find(id); ok {
			return f, true
		}
	}
	return Node{}, false
}

// Node finds the manager or chief id anywhere in the board's tree.
func (b Board) Node(id string) (Node, bool) {
	for _, n := range b.Tree {
		if f, ok := n.Find(id); ok {
			return f, true
		}
	}
	return Node{}, false
}

// tree builds the goal tree: chiefs over managers, each manager with its goal, session and agents. A goal that ended and was
// cleared from the owner's queue leaves the tree.
func (c *Core) tree(b *Board, o Org, feeds map[string]Feed) error {
	agents := map[string][]Live{}
	for _, s := range b.Sessions {
		if m, ok := o.Managed(s.Key(), b.parents); ok && m.Session != s.Key() {
			agents[m.ID] = append(agents[m.ID], s)
		}
	}
	for i := range b.Projects {
		for _, s := range b.Projects[i].Sessions {
			if m, ok := o.Managed(s.Key(), b.parents); ok {
				b.Projects[i].Manager = m.ID
				break
			}
		}
	}
	steers, err := c.Store.LatestSteers()
	if err != nil {
		return err
	}
	fresh, err := c.Freshness(o)
	if err != nil {
		return err
	}
	shown := map[string]bool{}
	for _, m := range o.Ordered() {
		if g, ok := o.Goals[m.GoalID]; ok && (g.State == GoalDropped || (g.State == GoalDone && !g.AckedAt.IsZero())) {
			continue
		}
		shown[m.ID] = true
	}
	var build func(Manager, map[string]bool) Node
	build = func(m Manager, seen map[string]bool) Node {
		seen[m.ID] = true
		n := Node{Manager: m, Agents: agents[m.ID], Fresh: fresh[m.ID]}
		if st, ok := steers[m.ID]; ok {
			n.Steer = &st
		}
		if g, ok := o.Goals[m.GoalID]; ok {
			n.Goal = &g
			n.Feed = feeds[g.ID]
			if n.Repo = c.gitFor(g.Repo, b.At).ID(); n.Repo == "" {
				n.Repo = g.Repo
			}
		}
		if s, ok := b.Session(m.Session); ok {
			n.Live = &s
		}
		for _, child := range o.Ordered() {
			if child.ParentID == m.ID && shown[child.ID] && !seen[child.ID] {
				n.Children = append(n.Children, build(child, seen))
			}
		}
		n.Standing = c.standing(*b, n)
		return n
	}
	seen := map[string]bool{}
	for _, m := range o.Ordered() {
		if !shown[m.ID] || (m.ParentID != "" && shown[m.ParentID]) {
			continue
		}
		b.Tree = append(b.Tree, build(m, seen))
	}
	return nil
}

func (c *Core) standing(b Board, n Node) string {
	own := StandMoving
	m := n.Manager
	switch {
	case n.Goal != nil && n.Goal.State == GoalDone:
		own = StandDone
	case n.Goal != nil && n.Goal.State == GoalVerifying:
		own = StandVerifying
	case m.Stuck() || (n.Goal != nil && n.Goal.State == GoalStuck):
		own = StandStuck
	case !b.Alive(m) && !(m.LastError == "" && b.At.Sub(m.LaunchedAt) < StartGrace):
		own = StandDown
	case m.Nudges > 0 || (n.Goal != nil && b.At.Sub(n.Goal.LastMovedAt) > QuietAfter):
		own = StandQuiet
	}
	if n.Goal != nil {
		return own
	}
	worst := own
	for _, ch := range n.Children {
		if standRank[ch.Standing] > standRank[worst] {
			worst = ch.Standing
		}
	}
	return worst
}

// queue adds the owner's other duties to the needs list: goals that finished or got stuck, pull requests ready to merge that no
// manager may merge, review requests GitHub addresses to the owner personally, and document asks no goal claims.
func (c *Core) queue(b *Board, o Org) error {
	dismissed, err := c.Store.queueDismissed()
	if err != nil {
		return err
	}
	var extra []Need
	for _, m := range o.Ordered() {
		g, ok := o.Goals[m.GoalID]
		if !ok || !g.AckedAt.IsZero() || (g.State != GoalDone && g.State != GoalStuck) {
			continue
		}
		extra = append(extra, Need{Goal: &g})
	}
	steers, err := c.Store.LatestSteers()
	if err != nil {
		return err
	}
	for _, m := range o.Ordered() {
		st, ok := steers[m.ID]
		if !ok || st.Adopted() || st.RaisedAt.IsZero() || !o.Active(m) || raisedTo(o, st) != Owner {
			continue
		}
		n := Need{Steer: &st, Manager: m.ID}
		if !dismissed[n.Key()] {
			extra = append(extra, n)
		}
	}
	statuses, err := c.Store.PRStatuses()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := range b.Sessions {
		s := &b.Sessions[i]
		m, managed := o.Managed(s.Key(), b.parents)
		for _, pr := range s.AllPRs() {
			st, ok := statuses[pr.URL]
			if seen[pr.URL] || !ok || !st.ReadyToMerge() || !st.OwnedPR() {
				continue
			}
			seen[pr.URL] = true
			if cleared, _ := c.Store.CanMerge(prRepoName(pr)); cleared && managed {
				continue
			}
			pr.Status = &st
			n := Need{PR: &pr, Project: s.Project}
			if managed {
				n.Manager = m.ID
			}
			if !dismissed[n.Key()] {
				extra = append(extra, n)
			}
		}
	}
	reviews, err := c.Store.OpenReviews()
	if err != nil {
		return err
	}
	for _, r := range reviews {
		n := Need{Review: &r}
		if !dismissed[n.Key()] {
			extra = append(extra, n)
		}
	}
	docs, err := c.Store.OpenDocAsks()
	if err != nil {
		return err
	}
	pages := map[string][]DocAsk{}
	var order []string
	for _, a := range docs {
		if a.Recipient != Owner || a.Routed != a.Version {
			continue
		}
		if _, ok := pages[a.Page]; !ok {
			order = append(order, a.Page)
		}
		pages[a.Page] = append(pages[a.Page], a)
	}
	for _, p := range order {
		threads := SortDocAsks(pages[p])
		n := Need{Doc: &threads[0], Docs: threads}
		if !dismissed[n.Key()] {
			extra = append(extra, n)
		}
	}
	b.Needs = append(extra, b.Needs...)
	return nil
}

// Key names the row for dismissal; a new head or review update gives the same duty a new key.
func (n Need) Key() string {
	switch {
	case n.Goal != nil:
		return "goal:" + n.Goal.ID
	case n.Steer != nil:
		return fmt.Sprintf("steer:%s#%d", n.Steer.Manager, n.Steer.Revision)
	case n.PR != nil:
		head := ""
		if n.PR.Status != nil {
			head = n.PR.Status.Merge.OID
		}
		return "merge:" + n.PR.URL + "@" + head
	case n.Review != nil:
		return "review:" + n.Review.URL + "@" + n.Review.Updated
	case n.Doc != nil:
		var vs []string
		for _, a := range n.Docs {
			vs = append(vs, a.Key+"@"+a.Version)
		}
		sort.Strings(vs)
		sum := sha256.Sum256([]byte(strings.Join(vs, ",")))
		return "doc:" + n.Doc.Page + "@" + hex.EncodeToString(sum[:8])
	case len(n.Asks) > 0:
		return "asks:" + n.Item.Session
	case n.Item != nil:
		return "item:" + n.Item.Key()
	case n.Session != nil:
		return "session:" + n.Session.Key()
	}
	return ""
}

// collapse folds owner items into one row per session no manager runs, oldest ask first; a manager's or chief's escalation
// keeps its own row.
func collapse(ns []Need, o Org, b Board) []Need {
	var out []Need
	at := map[string]int{}
	for _, n := range ns {
		if n.Item == nil || o.Sessions[n.Item.Session] != "" {
			out = append(out, n)
			continue
		}
		key := n.Item.Session
		if s, ok := b.Session(key); ok {
			key = s.Key()
		}
		if i, ok := at[key]; ok {
			out[i].Asks = append(out[i].Asks, *n.Item)
			continue
		}
		n.Asks = []Item{*n.Item}
		at[key] = len(out)
		out = append(out, n)
	}
	for i := range out {
		sort.SliceStable(out[i].Asks, func(a, c int) bool { return out[i].Asks[a].CreatedAt.Before(out[i].Asks[c].CreatedAt) })
	}
	return out
}

// Escalation reports whether the need is an item a manager or chief sent up to the owner.
func (n Need) Escalation(o Org) bool {
	return n.Item != nil && o.Sessions[n.Item.Session] != ""
}
