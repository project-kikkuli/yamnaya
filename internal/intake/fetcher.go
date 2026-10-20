package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/work"
)

type Fetcher struct {
	Discover       bool
	Client         *http.Client
	Run            func(context.Context, string, ...string) ([]byte, error)
	NotionURL      string
	NotionToken    string
	NotionReviewer string
}

func New() Fetcher {
	return Fetcher{Discover: true, Client: &http.Client{Timeout: 30 * time.Second}, Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	}, NotionURL: "https://api.notion.com/v1", NotionToken: os.Getenv("NOTION_TOKEN"), NotionReviewer: os.Getenv("NOTION_REVIEWER_ID")}
}

func (f Fetcher) Fetch(ctx context.Context, source work.Source) (work.Review, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch source.Kind {
	case "github":
		return f.github(ctx, source)
	case "notion":
		return f.notion(ctx, source)
	case "file":
		path := strings.TrimPrefix(source.URL, "file://")
		b, err := os.ReadFile(path)
		if err != nil {
			return work.Review{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return work.Review{}, err
		}
		sum := sha256.Sum256(b)
		return work.Review{ID: source.URL, URL: source.URL, Kind: "artifact", Version: hex.EncodeToString(sum[:]), Order: info.ModTime().UnixNano(), Title: info.Name(), Requester: source.Requester, Question: source.Question, Evidence: string(b)}, nil
	}
	return work.Review{}, fmt.Errorf("unsupported source %q", source.Kind)
}

func (f Fetcher) github(ctx context.Context, source work.Source) (work.Review, error) {
	u, err := url.Parse(source.URL)
	if err != nil || u.Host != "github.com" {
		return work.Review{}, fmt.Errorf("expected a github.com pull request URL")
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) != 4 || p[2] != "pull" {
		return work.Review{}, fmt.Errorf("expected owner/repo/pull/number")
	}
	b, err := f.Run(ctx, "gh", "pr", "view", source.URL, "--json", "title,body,headRefOid,updatedAt,reviewRequests,reviewDecision,statusCheckRollup,author,state")
	if err != nil {
		return work.Review{}, fmt.Errorf("GitHub source unavailable: %w", err)
	}
	var r struct {
		Title, Body, HeadRefOid, UpdatedAt, State string
		Author                                    struct{ Login string }
		ReviewRequests                            []json.RawMessage
	}
	if err = json.Unmarshal(b, &r); err != nil {
		return work.Review{}, err
	}
	at, err := time.Parse(time.RFC3339, r.UpdatedAt)
	if err != nil {
		return work.Review{}, err
	}
	semantic, _ := json.Marshal(r)
	sum := sha256.Sum256(semantic)
	return work.Review{ID: source.URL, URL: source.URL, Kind: "PR", Title: r.Title, Version: r.HeadRefOid + ":" + hex.EncodeToString(sum[:8]), Order: at.UnixNano(), Requester: source.Requester, Author: r.Author.Login, Question: source.Question, Evidence: string(b), Provisional: source.Requester == "", Closed: r.State == "CLOSED" || r.State == "MERGED"}, nil
}

var notionID = regexp.MustCompile(`[a-fA-F0-9]{32}|[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}`)

func (f Fetcher) notionRequest(ctx context.Context, path string) (json.RawMessage, error) {
	if f.NotionToken == "" {
		return nil, fmt.Errorf("Notion coverage unavailable: configure NOTION_TOKEN and share the watched page with that integration")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.NotionURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+f.NotionToken)
	req.Header.Set("Notion-Version", "2022-06-28")
	res, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Notion returned HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	return b, err
}

func (f Fetcher) notion(ctx context.Context, source work.Source) (work.Review, error) {
	u, err := url.Parse(source.URL)
	if err != nil {
		return work.Review{}, err
	}
	id := notionID.FindString(u.Path)
	if id == "" {
		return work.Review{}, fmt.Errorf("Notion page URL has no page ID")
	}
	page, err := f.notionRequest(ctx, "/pages/"+id)
	if err != nil {
		return work.Review{}, err
	}
	var p struct {
		LastEditedTime string `json:"last_edited_time"`
		Properties     map[string]struct {
			Type  string
			Title []struct {
				PlainText string `json:"plain_text"`
			}
		}
	}
	if err = json.Unmarshal(page, &p); err != nil {
		return work.Review{}, err
	}
	var title strings.Builder
	for _, property := range p.Properties {
		if property.Type == "title" {
			for _, t := range property.Title {
				title.WriteString(t.PlainText)
			}
		}
	}
	content := []json.RawMessage{page}
	queue := []string{id}
	latest, err := time.Parse(time.RFC3339, p.LastEditedTime)
	if err != nil {
		return work.Review{}, err
	}
	for len(queue) > 0 {
		block := queue[0]
		queue = queue[1:]
		cursor := ""
		for {
			path := "/blocks/" + block + "/children?page_size=100"
			if cursor != "" {
				path += "&start_cursor=" + url.QueryEscape(cursor)
			}
			body, err := f.notionRequest(ctx, path)
			if err != nil {
				return work.Review{}, err
			}
			var list struct {
				Results    []json.RawMessage
				HasMore    bool   `json:"has_more"`
				NextCursor string `json:"next_cursor"`
			}
			if err = json.Unmarshal(body, &list); err != nil {
				return work.Review{}, err
			}
			for _, raw := range list.Results {
				content = append(content, raw)
				var child struct {
					ID          string
					HasChildren bool   `json:"has_children"`
					LastEdited  string `json:"last_edited_time"`
				}
				if err = json.Unmarshal(raw, &child); err != nil {
					return work.Review{}, err
				}
				if child.HasChildren {
					queue = append(queue, child.ID)
				}
				if at, e := time.Parse(time.RFC3339, child.LastEdited); e == nil && at.After(latest) {
					latest = at
				}
			}
			if !list.HasMore {
				break
			}
			if list.NextCursor == "" || list.NextCursor == cursor {
				return work.Review{}, fmt.Errorf("Notion pagination did not advance")
			}
			cursor = list.NextCursor
		}
	}
	requester, question := source.Requester, source.Question
	cursor := ""
	for {
		path := "/comments?block_id=" + id + "&page_size=100"
		if cursor != "" {
			path += "&start_cursor=" + url.QueryEscape(cursor)
		}
		body, err := f.notionRequest(ctx, path)
		if err != nil {
			return work.Review{}, err
		}
		var comments struct {
			Results []json.RawMessage
			HasMore bool   `json:"has_more"`
			Next    string `json:"next_cursor"`
		}
		if err = json.Unmarshal(body, &comments); err != nil {
			return work.Review{}, err
		}
		for _, raw := range comments.Results {
			content = append(content, raw)
			var comment struct {
				CreatedBy struct{ ID string } `json:"created_by"`
				Edited    string              `json:"last_edited_time"`
				Text      []struct {
					Plain   string `json:"plain_text"`
					Mention struct{ User struct{ ID string } }
				} `json:"rich_text"`
			}
			if err = json.Unmarshal(raw, &comment); err != nil {
				return work.Review{}, err
			}
			if at, e := time.Parse(time.RFC3339, comment.Edited); e == nil && at.After(latest) {
				latest = at
			}
			if f.NotionReviewer != "" {
				mentioned := false
				var text strings.Builder
				for _, part := range comment.Text {
					if part.Mention.User.ID == f.NotionReviewer {
						mentioned = true
					}
					text.WriteString(part.Plain)
				}
				if mentioned {
					requester, question = comment.CreatedBy.ID, text.String()
				}
			}
		}
		if !comments.HasMore {
			break
		}
		if comments.Next == "" || comments.Next == cursor {
			return work.Review{}, fmt.Errorf("Notion comment pagination did not advance")
		}
		cursor = comments.Next
	}
	b, err := json.Marshal(content)
	if err != nil {
		return work.Review{}, err
	}
	var semantic any
	if err = json.Unmarshal(b, &semantic); err != nil {
		return work.Review{}, err
	}
	b, err = json.Marshal(semantic)
	if err != nil {
		return work.Review{}, err
	}
	sum := sha256.Sum256(b)
	return work.Review{ID: source.URL, URL: source.URL, Kind: "document", Title: title.String(), Requester: requester, Question: question, Version: hex.EncodeToString(sum[:]), Order: latest.UnixNano(), Evidence: string(b), Provisional: requester == ""}, nil
}
