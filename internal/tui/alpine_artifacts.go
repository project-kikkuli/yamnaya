package tui

import (
	"fmt"
	"path/filepath"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func (a *alpine) observeArtifacts(b core.Board) []alpineArtifact {
	var out []alpineArtifact
	next := map[string]alpineArtifact{}
	for _, p := range b.Projects {
		heads := map[string]bool{}
		for _, s := range p.Sessions {
			for _, pr := range s.AllPRs() {
				if pr.Status != nil && pr.Status.Head != "" {
					heads[filepath.Base(pr.Repo)+"\x00"+pr.Status.Head] = true
				}
			}
		}
		add := func(team, session, repo, def, branch string, ahead int, status core.Status, prs []core.PR) {
			base := alpineArtifact{project: p.Key, region: p.Repo, team: team, working: status == core.Busy,
				scopes: [4]string{"r:" + p.Repo, "m:" + p.Key, "s:" + session, "a:" + team}}
			for _, pr := range prs {
				artifact := base
				artifact.key, artifact.label, artifact.pr = pr.URL, fmt.Sprintf("#%d %s", pr.Number, filepath.Base(pr.Repo)), true
				if pr.Status != nil {
					artifact.status = *pr.Status
					artifact.commits = pr.Status.Commits
					artifact.known = pr.Status.State == "OPEN" || pr.Status.State == "CLOSED" || pr.Status.State == "MERGED"
				}
				out = append(out, artifact)
			}
			git := core.GitInfo{Common: repo, Default: def}
			if branch != "" && branch != "HEAD" && !git.IsDefault(branch) && !heads[git.Name()+"\x00"+branch] {
				artifact := base
				artifact.key, artifact.label, artifact.commits = "branch:"+repo+"/"+branch, branch, ahead
				out = append(out, artifact)
			}
		}
		for _, s := range p.Sessions {
			add(s.Key(), s.Key(), s.RepoKey, s.Default, s.Branch, s.Ahead, s.Status, s.PRs)
			for _, c := range s.Crew {
				team := s.Key() + "/" + c.ID
				if live, ok := a.teams[c.ID]; ok {
					team = live.id
				}
				add(team, s.Key(), c.RepoKey, c.Default, c.Branch, c.Ahead, c.Status, c.PRs)
			}
		}
	}
	for _, artifact := range out {
		key := artifact.project + "\x00" + artifact.key
		if current, exists := next[key]; exists {
			current.working = current.working || artifact.working
			next[key] = current
			continue
		}
		next[key] = artifact
		old, existed := a.artifacts[key]
		if !a.ready || !existed {
			continue
		}
		kind := ""
		switch {
		case old.known && old.state() != artifactMerged && artifact.state() == artifactMerged:
			kind = "merge"
		case old.known && old.state() != artifactClosed && old.state() != artifactMerged && old.state() != artifactFailed && artifact.state() == artifactFailed:
			kind = "fracture"
		case (!artifact.pr || old.known && artifact.known) && artifact.state() != artifactClosed && artifact.state() != artifactMerged && artifact.commits > old.commits:
			kind = "work"
		}
		if kind != "" {
			a.emit(alpineEvent{kind: kind, mountain: artifact.project, team: artifact.team, artifact: artifact.key, frame: a.frame, people: 1})
		}
	}
	a.artifacts = next
	return out
}

func (a *alpine) foldArtifacts(artifacts []alpineArtifact) {
	events := map[string][]alpineEvent{}
	for _, e := range a.events {
		if e.artifact != "" {
			key := e.mountain + "\x00" + e.artifact
			events[key] = append(events[key], e)
		}
	}
	seen := map[string]bool{}
	for _, artifact := range artifacts {
		for _, key := range append([]string{"*"}, artifact.scopes[:]...) {
			g := a.groups[key]
			if g == nil {
				continue
			}
			for _, e := range events[artifact.project+"\x00"+artifact.key] {
				a.indexEffect(key, e)
			}
			identity := key + "\x00" + artifact.key
			if seen[identity] {
				for i, sample := range g.artifacts {
					if sample.key == artifact.key {
						g.artifacts[i].working = sample.working || artifact.working
					}
				}
				continue
			}
			seen[identity] = true
			g.addArtifact(artifact)
		}
	}
}
