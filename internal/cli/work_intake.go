package cli

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
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
			w, err = intakeDestination(c, repo)
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

func intakeDestination(c *core.Core, repo string) (work.Scope, error) {
	rules, err := c.Store.Work.Rules()
	if err != nil {
		return work.Scope{}, err
	}
	matches := map[string]bool{}
	global := map[string]bool{}
	for _, r := range rules {
		if r.Repo == "" {
			global[r.Agent] = true
			continue
		}
		identity := r.Repo
		dir := identity
		if filepath.Base(dir) == ".git" {
			dir = filepath.Dir(dir)
		}
		if raw, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output(); err == nil {
			identity = strings.TrimSpace(string(raw))
		}
		identity = strings.TrimSuffix(identity, ".git")
		identity = strings.TrimPrefix(identity, "git@github.com:")
		identity = strings.TrimPrefix(identity, "https://github.com/")
		if identity == repo {
			matches[r.Agent] = true
		}
	}
	if len(matches) == 0 {
		matches = global
	}
	if len(matches) == 1 {
		for id := range matches {
			a, err := c.Store.Work.Agent(id)
			if err != nil {
				return work.Scope{}, err
			}
			return c.Store.Work.Get(a.Workspace)
		}
	}
	return c.Store.Work.Intake()
}

func routeUnassignedIntake(c *core.Core) error {
	all, err := c.Store.Work.List()
	if err != nil {
		return err
	}
	for _, w := range all {
		if w.Kind != "intake" || w.Owner != "" || len(w.Sources) == 0 {
			continue
		}
		rules, err := c.Store.Work.Rules()
		if err != nil {
			return err
		}
		ids := map[string]bool{}
		for _, r := range rules {
			if r.Repo == "" {
				ids[r.Agent] = true
			}
		}
		if len(ids) == 1 {
			for id := range ids {
				a, err := c.Store.Work.Agent(id)
				if err != nil {
					return err
				}
				if err = c.Store.Work.TransferSources(w.ID, a.Workspace); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
