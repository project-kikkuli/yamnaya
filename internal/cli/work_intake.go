package cli

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/intake"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func captureRequests(ctx context.Context, c *core.Core, f intake.Fetcher) error {
	if !c.Store.Work.Due("@github", "review-requests", time.Now()) {
		return nil
	}
	sources, fetchErr := f.Requests(ctx)
	if fetchErr != nil {
		c.Store.Work.SourceError("@github", "review-requests", fetchErr)
		return fetchErr
	}
	all, err := c.Store.Work.List()
	if err != nil {
		return err
	}
	peers, err := c.Store.Work.Peers()
	if err != nil {
		return err
	}
	remote := map[string]bool{}
	for _, peer := range peers {
		for _, w := range peer.Scopes {
			for _, source := range w.Sources {
				remote[source.URL] = true
			}
		}
	}
	byURL := map[string]work.Scope{}
	for _, w := range all {
		for _, source := range w.Sources {
			byURL[source.URL] = w
		}
	}
	for _, source := range sources {
		if remote[source.URL] {
			continue
		}
		w, found := byURL[source.URL]
		if found {
			current := false
			for _, old := range w.Sources {
				if old.URL == source.URL && old.RequestVersion == source.RequestVersion && old.Requester != "" {
					current = true
				}
			}
			if current {
				continue
			}
		}
		u, err := url.Parse(source.URL)
		if err != nil {
			return err
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 {
			continue
		}
		repo := parts[0] + "/" + parts[1]
		if !found {
			w, err = c.Store.Work.Ensure("github/"+repo, "https://github.com/"+repo, parts[1]+" reviews")
		} else {
			w, err = c.Store.Work.Get(w.ID)
		}
		if err != nil {
			return err
		}
		if _, err = c.Store.Work.Commit(work.Event{ID: "request/" + source.URL + "/" + source.RequestVersion, Work: w.ID, Expected: w.Revision, Type: "watch", Actor: "GitHub", Source: source}); err != nil {
			return err
		}
		byURL[source.URL] = w
	}
	if err = c.Store.Work.SourceError("@github", "review-requests", nil); err != nil {
		return err
	}
	return nil
}

func captureSessionPRs(c *core.Core, b core.Board) error {
	all, err := c.Store.Work.List()
	if err != nil {
		return err
	}
	bySession := map[string]work.Scope{}
	knownSources := map[string]bool{}
	for _, w := range all {
		for _, source := range w.Sources {
			knownSources[source.URL] = true
		}
		for session := range w.Bindings {
			bySession[session] = w
		}
	}
	for _, session := range b.Sessions {
		w, ok := bySession[session.Key()]
		if !ok {
			continue
		}
		known := map[string]bool{}
		for _, source := range w.Sources {
			known[source.URL] = true
		}
		for _, pr := range session.AllPRs() {
			if known[pr.URL] || knownSources[pr.URL] {
				continue
			}
			current, err := c.Store.Work.Get(w.ID)
			if err != nil {
				return err
			}
			source := work.Source{Kind: "github", URL: pr.URL, Coverage: "linked agent PR; human review request not yet confirmed"}
			if _, err = c.Store.Work.Commit(work.Event{ID: fmt.Sprintf("linked-pr/%s/%s", w.ID, pr.URL), Work: w.ID, Expected: current.Revision, Type: "watch", Actor: "discovery", Source: source}); err != nil {
				return err
			}
			known[pr.URL] = true
			knownSources[pr.URL] = true
		}
	}
	return nil
}
