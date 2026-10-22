package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DocAsk is something a person asks of the owner in a document: a discussion waiting on the owner, or a page naming the owner as its reviewer.
type DocAsk struct {
	// Key names the ask across sweeps: one per discussion, or one per page for a review request.
	Key, Kind, URL, Page, Title, Asker, Text, Evidence string
	// Version changes when the ask gains a new comment or its page changes.
	Version string
	// Refs are the pull request URLs and Notion page ids the page and its thread mention.
	Refs []string
}

// Notion sweeps the pages an integration can read for discussions and review requests waiting on the owner.
type Notion struct {
	URL, Token, Owner string
	Client            *http.Client
	// Pace is the least time between requests; Notion allows about three a second.
	Pace time.Duration
	// Window is how far back a page's last edit may be for its discussions to be swept.
	Window time.Duration
	Now    func() time.Time

	users map[string]notionUser
	last  time.Time
}

type notionUser struct {
	Type, Name string
}

var (
	NotionID    = regexp.MustCompile(`[a-fA-F0-9]{8}-?[a-fA-F0-9]{4}-?[a-fA-F0-9]{4}-?[a-fA-F0-9]{4}-?[a-fA-F0-9]{12}`)
	pullURL     = regexp.MustCompile(`https://github\.com/[\w.-]+/[\w.-]+/pull/\d+`)
	reviewProp  = regexp.MustCompile(`(?i)review|approv|sign.?off`)
	approvedBox = regexp.MustCompile(`(?i)approved|signed.?off|done|complete`)
)

// NotionKey is the id in its canonical form: lowercase hex without dashes.
func NotionKey(id string) string { return strings.ToLower(strings.ReplaceAll(id, "-", "")) }

func NewNotion(token, owner string) *Notion {
	return &Notion{URL: "https://api.notion.com/v1", Token: token, Owner: owner, Client: &http.Client{Timeout: 30 * time.Second},
		Pace: 350 * time.Millisecond, Window: 21 * 24 * time.Hour, Now: time.Now}
}

type notionError struct{ Status int }

func (e notionError) Error() string { return fmt.Sprintf("Notion returned HTTP %d", e.Status) }

func (n *Notion) call(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	if wait := n.Pace - time.Since(n.last); wait > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	n.last = time.Now()
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, n.URL+path, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+n.Token)
	req.Header.Set("Notion-Version", "2022-06-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := n.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		var e struct{ Message string }
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			return nil, fmt.Errorf("%w: %s %s: %s", notionError{res.StatusCode}, method, path, e.Message)
		}
		return nil, fmt.Errorf("%w: %s %s", notionError{res.StatusCode}, method, path)
	}
	return raw, nil
}

func missing(err error) bool {
	var e notionError
	return errors.As(err, &e) && (e.Status == http.StatusNotFound || e.Status == http.StatusForbidden)
}

type richText struct {
	Plain   string `json:"plain_text"`
	Mention struct {
		User struct{ ID string }
		Page struct{ ID string }
	}
	Href string
}

func plain(rt []richText) string {
	var b strings.Builder
	for _, t := range rt {
		b.WriteString(t.Plain)
	}
	return b.String()
}

type notionPage struct {
	ID, URL        string
	Archived       bool
	InTrash        bool                `json:"in_trash"`
	LastEditedTime string              `json:"last_edited_time"`
	CreatedBy      struct{ ID string } `json:"created_by"`
	Parent         struct {
		Type       string
		DatabaseID string `json:"database_id"`
	}
	Properties map[string]struct {
		Type     string
		Title    []richText
		People   []struct{ ID string }
		Checkbox bool
		Status   *struct{ ID, Name string }
	}
}

func (p notionPage) title() string {
	for _, prop := range p.Properties {
		if prop.Type == "title" {
			if t := plain(prop.Title); t != "" {
				return t
			}
		}
	}
	return "Untitled"
}

type comment struct {
	ID           string
	DiscussionID string              `json:"discussion_id"`
	CreatedTime  string              `json:"created_time"`
	CreatedBy    struct{ ID string } `json:"created_by"`
	RichText     []richText          `json:"rich_text"`
	Parent       struct {
		BlockID string `json:"block_id"`
	}
}

// People checks the token and maps each person the integration can see to their lowercase email and name, tab separated.
func (n *Notion) People(ctx context.Context) (map[string]string, error) {
	if _, err := n.call(ctx, http.MethodGet, "/users/me", nil); err != nil {
		return nil, fmt.Errorf("Notion rejected the token: %w", err)
	}
	out := map[string]string{}
	cursor := ""
	for {
		path := "/users?page_size=100"
		if cursor != "" {
			path += "&start_cursor=" + url.QueryEscape(cursor)
		}
		raw, err := n.call(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var list struct {
			Results []struct {
				ID, Type, Name string
				Person         struct{ Email string }
			}
			HasMore bool   `json:"has_more"`
			Next    string `json:"next_cursor"`
		}
		if err = json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		for _, u := range list.Results {
			if u.Type == "person" {
				out[u.ID] = strings.ToLower(u.Person.Email) + "\t" + u.Name
			}
		}
		if !list.HasMore || list.Next == "" || list.Next == cursor {
			return out, nil
		}
		cursor = list.Next
	}
}

// Asks sweeps every page edited within the window plus the pages in keep, and returns every ask waiting on the owner, failing
// rather than returning a partial list. Pages counts the pages swept.
func (n *Notion) Asks(ctx context.Context, keep []string) (asks []DocAsk, pages int, err error) {
	if n.users == nil {
		n.users = map[string]notionUser{}
	}
	since := n.Now().Add(-n.Window)
	var found []notionPage
	seen := map[string]bool{}
	cursor := ""
	for done := false; !done; {
		body := map[string]any{"filter": map[string]string{"property": "object", "value": "page"},
			"sort": map[string]string{"direction": "descending", "timestamp": "last_edited_time"}, "page_size": 100}
		if cursor != "" {
			body["start_cursor"] = cursor
		}
		raw, err := n.call(ctx, http.MethodPost, "/search", body)
		if err != nil {
			return nil, 0, err
		}
		var list struct {
			Results []notionPage
			HasMore bool   `json:"has_more"`
			Next    string `json:"next_cursor"`
		}
		if err = json.Unmarshal(raw, &list); err != nil {
			return nil, 0, err
		}
		for _, p := range list.Results {
			at, err := time.Parse(time.RFC3339, p.LastEditedTime)
			if err != nil {
				return nil, 0, err
			}
			if at.Before(since) {
				done = true
				break
			}
			if !seen[NotionKey(p.ID)] && !p.Archived && !p.InTrash {
				seen[NotionKey(p.ID)] = true
				found = append(found, p)
			}
		}
		if !list.HasMore {
			break
		}
		if list.Next == "" || list.Next == cursor {
			return nil, 0, fmt.Errorf("Notion search pagination did not advance")
		}
		cursor = list.Next
	}
	for _, id := range keep {
		if seen[NotionKey(id)] {
			continue
		}
		seen[NotionKey(id)] = true
		raw, err := n.call(ctx, http.MethodGet, "/pages/"+id, nil)
		if missing(err) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		var p notionPage
		if err = json.Unmarshal(raw, &p); err != nil {
			return nil, 0, err
		}
		if !p.Archived && !p.InTrash {
			found = append(found, p)
		}
	}
	statuses := map[string]map[string]bool{}
	for _, p := range found {
		got, err := n.page(ctx, p, statuses)
		if err != nil {
			return nil, 0, err
		}
		asks = append(asks, got...)
	}
	return asks, len(found), nil
}

func (n *Notion) page(ctx context.Context, p notionPage, statuses map[string]map[string]bool) ([]DocAsk, error) {
	var text strings.Builder
	blockText := map[string]string{}
	blocks := []string{p.ID}
	for queue := []string{p.ID}; len(queue) > 0; {
		parent := queue[0]
		queue = queue[1:]
		cursor := ""
		for {
			path := "/blocks/" + parent + "/children?page_size=100"
			if cursor != "" {
				path += "&start_cursor=" + url.QueryEscape(cursor)
			}
			raw, err := n.call(ctx, http.MethodGet, path, nil)
			if missing(err) && parent == p.ID {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			var list struct {
				Results []map[string]json.RawMessage
				HasMore bool   `json:"has_more"`
				Next    string `json:"next_cursor"`
			}
			if err = json.Unmarshal(raw, &list); err != nil {
				return nil, err
			}
			for _, b := range list.Results {
				var id, kind string
				var children bool
				_ = json.Unmarshal(b["id"], &id)
				_ = json.Unmarshal(b["type"], &kind)
				_ = json.Unmarshal(b["has_children"], &children)
				if kind == "child_page" || kind == "child_database" {
					continue
				}
				var content struct {
					RichText []richText `json:"rich_text"`
				}
				_ = json.Unmarshal(b[kind], &content)
				line := plain(content.RichText)
				for _, t := range content.RichText {
					if t.Mention.Page.ID != "" {
						line += " " + t.Mention.Page.ID
					}
					line += " " + t.Href
				}
				blockText[NotionKey(id)] = plain(content.RichText)
				text.WriteString(line + "\n")
				blocks = append(blocks, id)
				if children {
					queue = append(queue, id)
				}
			}
			if !list.HasMore {
				break
			}
			if list.Next == "" || list.Next == cursor {
				return nil, fmt.Errorf("Notion block pagination did not advance")
			}
			cursor = list.Next
		}
	}
	threads := map[string][]comment{}
	var order []string
	for _, block := range blocks {
		cursor := ""
		for {
			path := "/comments?block_id=" + block + "&page_size=100"
			if cursor != "" {
				path += "&start_cursor=" + url.QueryEscape(cursor)
			}
			raw, err := n.call(ctx, http.MethodGet, path, nil)
			if err != nil {
				return nil, err
			}
			var list struct {
				Results []comment
				HasMore bool   `json:"has_more"`
				Next    string `json:"next_cursor"`
			}
			if err = json.Unmarshal(raw, &list); err != nil {
				return nil, err
			}
			for _, c := range list.Results {
				d := NotionKey(c.DiscussionID)
				if _, ok := threads[d]; !ok {
					order = append(order, d)
				}
				threads[d] = append(threads[d], c)
			}
			if !list.HasMore {
				break
			}
			if list.Next == "" || list.Next == cursor {
				return nil, fmt.Errorf("Notion comment pagination did not advance")
			}
			cursor = list.Next
		}
	}
	title := p.title()
	var out []DocAsk
	for _, d := range order {
		ask, ok, err := n.discussion(ctx, p, threads[d], blockText, text.String())
		if err != nil {
			return nil, err
		}
		if ok {
			ask.Key, ask.Page, ask.Title, ask.URL = "notion:"+d, NotionKey(p.ID), title, p.URL+"?d="+d
			out = append(out, ask)
		}
	}
	review, ok, err := n.review(ctx, p, statuses)
	if err != nil {
		return nil, err
	}
	if ok {
		review.Page, review.Title, review.URL, review.Refs = NotionKey(p.ID), title, p.URL, refs(text.String())
		out = append(out, review)
	}
	return out, nil
}

func (n *Notion) owner(id string) bool { return id != "" && NotionKey(id) == NotionKey(n.Owner) }

func (n *Notion) user(ctx context.Context, id string) (notionUser, error) {
	if u, ok := n.users[id]; ok {
		return u, nil
	}
	raw, err := n.call(ctx, http.MethodGet, "/users/"+id, nil)
	u := notionUser{Type: "person", Name: "someone"}
	if err == nil {
		err = json.Unmarshal(raw, &u)
	} else if missing(err) {
		err = nil
	}
	if err != nil {
		return u, err
	}
	n.users[id] = u
	return u, nil
}

// discussion is an ask when its last comment is a person's other than the owner's and it is addressed to the owner: it mentions
// the owner since the owner last wrote, the owner wrote in it, or it sits on a page the owner created.
func (n *Notion) discussion(ctx context.Context, p notionPage, thread []comment, blockText map[string]string, pageText string) (DocAsk, bool, error) {
	sort.SliceStable(thread, func(i, j int) bool { return thread[i].CreatedTime < thread[j].CreatedTime })
	people := make([]notionUser, len(thread))
	for i, c := range thread {
		u, err := n.user(ctx, c.CreatedBy.ID)
		if err != nil {
			return DocAsk{}, false, err
		}
		people[i] = u
	}
	last := len(thread) - 1
	if last < 0 || n.owner(thread[last].CreatedBy.ID) || people[last].Type != "person" {
		return DocAsk{}, false, nil
	}
	ownerAt, asking := -1, -1
	for i, c := range thread {
		if n.owner(c.CreatedBy.ID) {
			ownerAt, asking = i, -1
			continue
		}
		for _, t := range c.RichText {
			if n.owner(t.Mention.User.ID) && people[i].Type == "person" {
				asking = i
			}
		}
	}
	kind := "mention"
	switch {
	case asking >= 0:
	case ownerAt >= 0:
		kind, asking = "reply", last
	case n.owner(p.CreatedBy.ID):
		kind, asking = "comment", last
	default:
		return DocAsk{}, false, nil
	}
	var ev strings.Builder
	if on := blockText[NotionKey(thread[0].Parent.BlockID)]; on != "" {
		fmt.Fprintf(&ev, "on %q\n", truncate(on, 160))
	}
	all := pageText
	for i, c := range thread {
		name := people[i].Name
		if n.owner(c.CreatedBy.ID) {
			name = "you"
		}
		fmt.Fprintf(&ev, "%s: %s\n", name, plain(c.RichText))
		all += "\n" + plain(c.RichText)
		for _, t := range c.RichText {
			all += " " + t.Mention.Page.ID + " " + t.Href
		}
	}
	return DocAsk{Kind: kind, Asker: people[asking].Name, Text: plain(thread[asking].RichText), Evidence: strings.TrimSpace(ev.String()),
		Version: thread[last].ID, Refs: refs(all)}, true, nil
}

// review is an ask when a people property named for review or approval lists the owner and the page's status is not complete.
func (n *Notion) review(ctx context.Context, p notionPage, statuses map[string]map[string]bool) (DocAsk, bool, error) {
	var named string
	for name, prop := range p.Properties {
		if prop.Type != "people" || !reviewProp.MatchString(name) {
			continue
		}
		for _, who := range prop.People {
			if n.owner(who.ID) {
				named = name
			}
		}
	}
	if named == "" {
		return DocAsk{}, false, nil
	}
	state := ""
	for name, prop := range p.Properties {
		if prop.Type == "checkbox" && prop.Checkbox && approvedBox.MatchString(name) {
			return DocAsk{}, false, nil
		}
		if prop.Type == "status" && prop.Status != nil {
			complete, err := n.complete(ctx, p.Parent.DatabaseID, statuses)
			if err != nil {
				return DocAsk{}, false, err
			}
			if complete[prop.Status.ID] {
				return DocAsk{}, false, nil
			}
			state = prop.Status.Name
		}
	}
	text := named + " lists you"
	if state != "" {
		text += "; status " + state
	}
	return DocAsk{Key: "notion-review:" + NotionKey(p.ID), Kind: "review", Asker: "Notion", Text: text, Evidence: text, Version: p.LastEditedTime}, true, nil
}

// complete is the status options in a database's Complete group.
func (n *Notion) complete(ctx context.Context, database string, cache map[string]map[string]bool) (map[string]bool, error) {
	if c, ok := cache[database]; ok || database == "" {
		return c, nil
	}
	raw, err := n.call(ctx, http.MethodGet, "/databases/"+database, nil)
	if err != nil {
		return nil, err
	}
	var db struct {
		Properties map[string]struct {
			Type   string
			Status struct {
				Groups []struct {
					Name      string
					OptionIDs []string `json:"option_ids"`
				}
			}
		}
	}
	if err = json.Unmarshal(raw, &db); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, prop := range db.Properties {
		for _, g := range prop.Status.Groups {
			if strings.EqualFold(g.Name, "complete") {
				for _, id := range g.OptionIDs {
					out[id] = true
				}
			}
		}
	}
	cache[database] = out
	return out, nil
}

func refs(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range pullURL.FindAllString(text, -1) {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	for _, id := range NotionID.FindAllString(text, -1) {
		if k := NotionKey(id); !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
