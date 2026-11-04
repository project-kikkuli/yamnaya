package core

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	mergeAskWords = regexp.MustCompile(`(?i)^\s*(review and )?merge\b`)
	askedNumber   = regexp.MustCompile(`#(\d+)`)
)

// MergeAskPR is the pull request an item asks the owner to merge: a chore whose words open with merge and whose evidence links one
// pull request, the same one its words number if they number any.
func MergeAskPR(it Item) (PR, bool) {
	if it.Kind != KindChore || !mergeAskWords.MatchString(it.Question) {
		return PR{}, false
	}
	var found PR
	for _, ev := range it.Evidence {
		for _, pr := range PRsIn(ev) {
			if found.URL != "" && found.URL != pr.URL {
				return PR{}, false
			}
			found = pr
		}
	}
	if found.URL == "" {
		return PR{}, false
	}
	if m := askedNumber.FindStringSubmatch(it.Question); m != nil && m[1] != strconv.Itoa(found.Number) {
		return PR{}, false
	}
	return found, true
}

// holdReason is why the owner cannot merge the pull request now, in the words a row shows.
func (s PRStatus) holdReason(now time.Time) string {
	switch {
	case s.State != "OPEN":
		return strings.ToLower(s.State)
	case now.Sub(s.Fetched) >= prMergeFresh:
		return "read stale"
	case s.Merge.Draft:
		return "draft"
	case s.Review == "CHANGES_REQUESTED":
		return "changes asked"
	case s.Review == "REVIEW_REQUIRED":
		return "needs review"
	case s.Failed > 0:
		return "CI failing"
	case s.Pending > 0:
		return "CI running"
	case s.Unresolved > 0:
		return "open threads"
	case s.Merge.Mergeable == "CONFLICTING":
		return "conflict"
	case s.Merge.MergeState != "" && s.Merge.MergeState != "CLEAN":
		return strings.ToLower(s.Merge.MergeState)
	case s.Merge.Error != "":
		return "read failed"
	}
	return "not ready"
}

// foldMergeAsks makes each ask to merge a pull request follow that pull request: an ask the owner can merge now leaves the list and
// is returned by URL for the pull request's own row, one it cannot folds into later with the reason, a merged one shows nowhere.
func foldMergeAsks(live, later []Need, statuses map[string]PRStatus, b *Board) ([]Need, []Need, map[string][]Item) {
	asks := map[string][]Item{}
	var liveOut, laterOut []Need
	for _, n := range later {
		if n.Item != nil {
			if pr, ok := MergeAskPR(*n.Item); ok {
				st, known := statuses[pr.URL]
				if known && st.State == "MERGED" {
					continue
				}
				n.HeldPR, n.Held = &pr, "not read yet"
				if known {
					n.Held = "ready"
					if !st.ReadyToMerge(b.At) || !st.OwnedPR() {
						n.Held = st.holdReason(b.At)
					}
				}
			}
		}
		laterOut = append(laterOut, n)
	}
	for _, n := range live {
		pr, ok := PR{}, false
		if n.Item != nil {
			pr, ok = MergeAskPR(*n.Item)
		}
		if !ok {
			liveOut = append(liveOut, n)
			continue
		}
		st, known := statuses[pr.URL]
		switch {
		case !known:
			n.HeldPR, n.Held = &pr, "not read yet"
			laterOut = append(laterOut, n)
		case st.State == "MERGED":
		case st.State == "OPEN" && st.Foreign():
			pr.Status = &st
			n.HeldPR = &pr
			liveOut = append(liveOut, n)
		case st.ReadyToMerge(b.At) && st.OwnedPR():
			asks[pr.URL] = append(asks[pr.URL], *n.Item)
		default:
			pr.Status = &st
			n.HeldPR, n.Held = &pr, st.holdReason(b.At)
			laterOut = append(laterOut, n)
		}
	}
	return liveOut, laterOut, asks
}

// attachMergeAsks puts each ask on its pull request's row, making the row when the board's gate did not, as a pull request that
// requires no review; an ask on a row the owner dismissed waits in later.
func attachMergeAsks(rows, later []Need, asks map[string][]Item, statuses map[string]PRStatus, dismissed map[string]bool) ([]Need, []Need) {
	var urls []string
	for url := range asks {
		urls = append(urls, url)
	}
	slices.Sort(urls)
	for _, url := range urls {
		i := slices.IndexFunc(rows, func(n Need) bool { return n.PR != nil && n.PR.URL == url })
		if i < 0 {
			found := PRsIn(url)
			st := statuses[url]
			if st.Title == "" {
				st.Title = oneLine(asks[url][0].Question)
			}
			found[0].Status = &st
			rows = append(rows, Need{PR: &found[0]})
			i = len(rows) - 1
		}
		rows[i].MergeAsks = append(rows[i].MergeAsks, asks[url]...)
		if dismissed[rows[i].Key()] {
			for j := range asks[url] {
				later = append(later, Need{Item: &asks[url][j], HeldPR: rows[i].PR, Held: "row dismissed"})
			}
			rows = slices.Delete(rows, i, i+1)
		}
	}
	return rows, later
}

// CloseMergedAsks closes each open ask to merge a pull request that has merged and tells the agent that filed it why.
func (c *Core) CloseMergedAsks(b Board) error {
	items, err := c.Store.Items(ItemFilter{States: []string{ItemOpen, ItemSnoozed}})
	if err != nil {
		return err
	}
	statuses, err := c.Store.PRStatuses()
	if err != nil {
		return err
	}
	for _, it := range items {
		pr, ok := MergeAskPR(it)
		if !ok || it.Recipient != Owner || statuses[pr.URL].State != "MERGED" {
			continue
		}
		closed, err := c.Store.Transition(it.Key(), ItemDismissed, fmt.Sprintf("%s#%d has merged, so ym closed this ask", prRepoName(pr), pr.Number))
		if err != nil {
			return err
		}
		c.deliver(b, closed.Session, []Item{closed})
	}
	return nil
}

// WithHostRows adds the owner's rows from other hosts to a board that does not show those hosts, from the item snapshots this host
// keeps, so it costs no read of another host. A row acts on its host only where this host routes there.
func (c *Core) WithHostRows(b Board) (Board, error) {
	if c.ShowRemotes || c.Store == nil {
		return b, nil
	}
	boards, err := c.itemBoards()
	if err != nil {
		return b, err
	}
	var live []Need
	for _, r := range boards {
		for i := range r.Items {
			it := &r.Items[i]
			if it.Recipient != Owner {
				continue
			}
			_, _, routed := c.RemoteRef(it.Key())
			live = append(live, Need{Item: it, Project: sessionOn(it.Host, it.Session), Read: r.At, NoAct: !routed})
		}
	}
	if len(live) == 0 {
		return b, nil
	}
	statuses, err := c.Store.PRStatuses()
	if err != nil {
		return b, err
	}
	dismissed, err := c.Store.queueDismissed()
	if err != nil {
		return b, err
	}
	live, later, asks := foldMergeAsks(live, nil, statuses, &b)
	b.Needs = append(slices.Clone(b.Needs), collapse(live, b.Org, b)...)
	b.Later = append(slices.Clone(b.Later), later...)
	b.Needs, b.Later = attachMergeAsks(b.Needs, b.Later, asks, statuses, dismissed)
	return b, c.foldLater(&b)
}
