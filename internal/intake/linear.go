package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

var ErrLinearUnavailable = errors.New("Linear issue is unavailable")

type Linear struct {
	URL, Token string
	Client     *http.Client
	Now        func() time.Time
	Next       time.Time
}

type LinearIdentity struct {
	ID, Name, Email string
	Organization    struct{ ID, Name string }
}
type linearPageInfo struct {
	HasNextPage bool
	EndCursor   *string
}
type linearIssueWire struct {
	ID, Identifier, Title, Description, URL, UpdatedAt string
	Priority                                           int
	State                                              struct{ Name, Type string }
	Team                                               struct{ Name, Key string }
	Project                                            *struct{ Name string }
	Assignee                                           *struct{ ID, Name string }
}
type linearVars struct {
	First int     `json:"first,omitempty"`
	After *string `json:"after,omitempty"`
	Owner string  `json:"owner,omitempty"`
	ID    string  `json:"id,omitempty"`
}

const linearFields = `id identifier title description url updatedAt priority state { name type } team { key name } project { name } assignee { id name }`

func NewLinear(token string) *Linear {
	return &Linear{URL: "https://api.linear.app/graphql", Token: token, Client: &http.Client{Timeout: 30 * time.Second}, Now: time.Now}
}

func (l *Linear) query(ctx context.Context, q string, vars linearVars, out any) error {
	now := l.Now()
	if now.Before(l.Next) {
		return fmt.Errorf("Linear reads paused until %s", l.Next.UTC().Format(time.RFC3339))
	}
	raw, err := json.Marshal(struct {
		Query     string     `json:"query"`
		Variables linearVars `json:"variables"`
	}{q, vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.URL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", l.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := l.Client.Do(req)
	if err != nil {
		l.Next = now.Add(time.Minute)
		return errors.New("Linear request failed; cached state retained")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusTooManyRequests {
		l.Next = now.Add(time.Minute)
		if n, e := strconv.Atoi(res.Header.Get("Retry-After")); e == nil && n > 0 {
			l.Next = now.Add(time.Duration(n) * time.Second)
		}
	}
	if res.Header.Get("X-RateLimit-Requests-Remaining") == "0" {
		if n, e := strconv.ParseInt(res.Header.Get("X-RateLimit-Requests-Reset"), 10, 64); e == nil {
			at := time.UnixMilli(n)
			if at.After(l.Next) {
				l.Next = at
			}
		}
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return errors.New("Linear authentication or scope refused; reconnect with a Read key")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if l.Next.Before(now.Add(time.Minute)) {
			l.Next = now.Add(time.Minute)
		}
		return fmt.Errorf("Linear HTTP %d; cached state retained", res.StatusCode)
	}
	raw, err = io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 4<<20 {
		return errors.New("Linear response exceeded 4 MiB")
	}
	var envelope struct {
		Data   json.RawMessage
		Errors []struct {
			Message    string
			Extensions struct{ Code, Type string }
		}
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return errors.New("Linear returned invalid JSON")
	}
	if len(envelope.Errors) > 0 {
		if len(envelope.Errors) == 1 && (envelope.Errors[0].Extensions.Code == "ENTITY_NOT_FOUND" || envelope.Errors[0].Extensions.Type == "EntityNotFound") {
			return ErrLinearUnavailable
		}
		if envelope.Errors[0].Extensions.Code == "RATELIMITED" {
			l.Next = now.Add(time.Minute)
		}
		return errors.New("Linear GraphQL request incomplete; cached state retained")
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("Linear response has no data")
	}
	return json.Unmarshal(envelope.Data, out)
}

func (l *Linear) Identity(ctx context.Context) (LinearIdentity, error) {
	var out struct{ Viewer *LinearIdentity }
	err := l.query(ctx, `query YamnayaViewer { viewer { id name email organization { id name } } }`, linearVars{}, &out)
	if err != nil {
		return LinearIdentity{}, err
	}
	if out.Viewer == nil || out.Viewer.ID == "" || out.Viewer.Organization.ID == "" {
		return LinearIdentity{}, errors.New("Linear did not identify the account and workspace")
	}
	return *out.Viewer, nil
}

func issueFromWire(w linearIssueWire) core.LinearIssue {
	i := core.LinearIssue{ID: w.ID, Identifier: w.Identifier, Title: w.Title, Description: w.Description, URL: w.URL, UpdatedAt: w.UpdatedAt, Priority: w.Priority, State: w.State.Name, StateType: w.State.Type, Team: w.Team.Key}
	if w.Project != nil {
		i.Project = w.Project.Name
	}
	if w.Assignee != nil {
		i.AssigneeID, i.Assignee = w.Assignee.ID, w.Assignee.Name
	}
	return i
}

func advanceLinear(p linearPageInfo, seen map[string]bool) (*string, error) {
	if !p.HasNextPage {
		return nil, nil
	}
	if p.EndCursor == nil || *p.EndCursor == "" || seen[*p.EndCursor] {
		return nil, errors.New("Linear pagination is incomplete or repeated a cursor")
	}
	seen[*p.EndCursor] = true
	return p.EndCursor, nil
}

func (l *Linear) assigned(ctx context.Context, owner string) ([]core.LinearIssue, error) {
	var after *string
	seen := map[string]bool{}
	out := []core.LinearIssue{}
	for {
		var data struct {
			Issues *struct {
				Nodes    []linearIssueWire
				PageInfo linearPageInfo
			}
		}
		q := `query YamnayaAssigned($first:Int!,$after:String,$owner:ID!){ issues(first:$first,after:$after,includeArchived:true,filter:{assignee:{id:{eq:$owner}},state:{type:{nin:["completed","canceled"]}}},orderBy:updatedAt) { nodes { ` + linearFields + ` } pageInfo { hasNextPage endCursor } } }`
		if err := l.query(ctx, q, linearVars{First: 50, After: after, Owner: owner}, &data); err != nil {
			return nil, err
		}
		if data.Issues == nil {
			return nil, errors.New("Linear assigned issue coverage unavailable")
		}
		for _, w := range data.Issues.Nodes {
			i := issueFromWire(w)
			i.Assigned = true
			out = append(out, i)
		}
		next, err := advanceLinear(data.Issues.PageInfo, seen)
		if err != nil {
			return nil, err
		}
		if next == nil {
			return out, nil
		}
		after = next
	}
}

func (l *Linear) Issue(ctx context.Context, id string) (core.LinearIssue, error) {
	var data struct{ Issue *linearIssueWire }
	if err := l.query(ctx, `query YamnayaIssue($id:String!){ issue(id:$id) { `+linearFields+` } }`, linearVars{ID: id}, &data); err != nil {
		return core.LinearIssue{}, err
	}
	if data.Issue == nil {
		return core.LinearIssue{}, ErrLinearUnavailable
	}
	return issueFromWire(*data.Issue), nil
}

func (l *Linear) Hydrate(ctx context.Context, i *core.LinearIssue) error {
	i.Comments = nil
	i.Relations = nil
	var after *string
	seen := map[string]bool{}
	for {
		var data struct {
			Issue *struct {
				Comments *struct {
					Nodes []struct {
						ID, Body, URL, CreatedAt, UpdatedAt string
						User                                *struct{ Name string }
					}
					PageInfo linearPageInfo
				}
			}
		}
		q := `query YamnayaComments($id:String!,$first:Int!,$after:String){ issue(id:$id){ comments(first:$first,after:$after){ nodes { id body url createdAt updatedAt user { name } } pageInfo { hasNextPage endCursor } } } }`
		if err := l.query(ctx, q, linearVars{ID: i.ID, First: 50, After: after}, &data); err != nil {
			return err
		}
		if data.Issue == nil || data.Issue.Comments == nil {
			return errors.New("Linear comment coverage unavailable")
		}
		for _, w := range data.Issue.Comments.Nodes {
			c := core.LinearComment{ID: w.ID, Body: w.Body, URL: w.URL, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt}
			if w.User != nil {
				c.Author = w.User.Name
			}
			i.Comments = append(i.Comments, c)
		}
		next, err := advanceLinear(data.Issue.Comments.PageInfo, seen)
		if err != nil {
			return err
		}
		if next == nil {
			break
		}
		after = next
	}
	for _, inverse := range []bool{false, true} {
		after = nil
		seen = map[string]bool{}
		field := "relations"
		if inverse {
			field = "inverseRelations"
		}
		for {
			var data struct {
				Issue *struct {
					Relations *struct {
						Nodes []struct {
							ID, Type            string
							Issue, RelatedIssue *linearIssueWire
						}
						PageInfo linearPageInfo
					}
				}
			}
			q := `query YamnayaRelations($id:String!,$first:Int!,$after:String){ issue(id:$id){ relations: ` + field + `(first:$first,after:$after){ nodes { id type issue { id identifier title url } relatedIssue { id identifier title url } } pageInfo { hasNextPage endCursor } } } }`
			if err := l.query(ctx, q, linearVars{ID: i.ID, First: 50, After: after}, &data); err != nil {
				return err
			}
			if data.Issue == nil || data.Issue.Relations == nil {
				return errors.New("Linear relation coverage unavailable")
			}
			for _, w := range data.Issue.Relations.Nodes {
				target := w.RelatedIssue
				if inverse {
					target = w.Issue
				}
				if target != nil {
					i.Relations = append(i.Relations, core.LinearRelation{ID: w.ID, Type: w.Type, IssueID: target.ID, Identifier: target.Identifier, Title: target.Title, URL: target.URL, Inverse: inverse})
				}
			}
			next, err := advanceLinear(data.Issue.Relations.PageInfo, seen)
			if err != nil {
				return err
			}
			if next == nil {
				break
			}
			after = next
		}
	}
	i.HydratedAt = l.Now()
	return nil
}

func (l *Linear) Snapshot(ctx context.Context, previous core.LinearSnapshot) (core.LinearSnapshot, error) {
	who, err := l.Identity(ctx)
	if err != nil {
		return core.LinearSnapshot{}, err
	}
	if previous.OwnerID != "" && (who.ID != previous.OwnerID || who.Organization.ID != previous.WorkspaceID) {
		return core.LinearSnapshot{}, errors.New("Linear credentials identify a different account or workspace")
	}
	out := core.LinearSnapshot{OwnerID: who.ID, OwnerName: who.Name, WorkspaceID: who.Organization.ID, Workspace: who.Organization.Name, At: l.Now()}
	assigned, err := l.assigned(ctx, who.ID)
	if err != nil {
		return out, err
	}
	old := map[string]core.LinearIssue{}
	seen := map[string]bool{}
	for _, i := range previous.Issues {
		old[i.ID] = i
	}
	candidates := append([]core.LinearIssue{}, assigned...)
	for _, i := range assigned {
		seen[i.ID] = true
	}
	for _, i := range previous.Issues {
		if seen[i.ID] {
			continue
		}
		if !i.Assigned && !i.Watched && i.Goal == "" && i.Attention == "" {
			continue
		}
		next, e := l.Issue(ctx, i.ID)
		if errors.Is(e, ErrLinearUnavailable) {
			i.Unavailable = true
			i.Assigned = false
			i.ReadAt = l.Now()
			out.Issues = append(out.Issues, i)
			continue
		}
		if e != nil {
			return out, e
		}
		next.Assigned = next.AssigneeID == who.ID && next.Active()
		candidates = append(candidates, next)
	}
	for _, i := range candidates {
		if i.ID == "" || i.Identifier == "" {
			return out, errors.New("Linear returned an issue without identity")
		}
		prev, ok := old[i.ID]
		i.Watched = prev.Watched
		if ok && prev.UpdatedAt == i.UpdatedAt && l.Now().Sub(prev.HydratedAt) < 15*time.Minute {
			i.Comments, i.Relations, i.HydratedAt = prev.Comments, prev.Relations, prev.HydratedAt
		} else if err = l.Hydrate(ctx, &i); err != nil {
			return out, err
		}
		i.ReadAt = l.Now()
		out.Issues = append(out.Issues, i)
	}
	return out, nil
}
