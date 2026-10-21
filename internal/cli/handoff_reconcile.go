package cli

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/handoff"
	"github.com/project-kikkuli/yamnaya/internal/intake"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

type existingItem struct{ ID, Kind, Question, Recommendation, State, Answer, Session, AnsweredAt string }
type existingRequest struct{ ID, Kind, Target, Question, State string }

func handoffLedger(c *core.Core, id string) (any, error) {
	w, err := c.Store.Work.Get(id)
	if err != nil {
		return nil, err
	}
	items, err := c.Store.Items(core.ItemFilter{Scopes: []string{w.Repo}})
	if err != nil {
		return nil, err
	}
	ledger := struct {
		Project, Repository string
		ExistingItems       []existingItem
		ExistingRequests    []existingRequest
	}{Project: w.Name, Repository: handoffRepo(w.Repo)}
	for _, it := range items {
		ledger.ExistingItems = append(ledger.ExistingItems, existingItem{ID: "item:" + it.Key(), Kind: it.Kind, Question: it.Question, Recommendation: it.Rec, State: it.State, Answer: it.Answer, Session: it.Session, AnsweredAt: it.AnsweredAt.String()})
	}
	for _, r := range w.Reviews {
		ledger.ExistingRequests = append(ledger.ExistingRequests, existingRequest{ID: "review:" + r.ID, Kind: r.Kind, Target: r.URL, Question: r.Question, State: r.QueueState()})
	}
	claims, err := c.Store.Handoffs.Claims(w.ID, false)
	if err != nil {
		return nil, err
	}
	for _, claim := range claims {
		if _, err := c.Store.Handoffs.Target(claim.ID); err != nil {
			continue
		}
		ledger.ExistingRequests = append(ledger.ExistingRequests, existingRequest{ID: "claim:" + claim.ID, Kind: claim.Kind, Target: claim.Target, Question: claim.Question})
	}
	return ledger, nil
}

func handoffRepo(dir string) string {
	if dir == "" {
		return ""
	}
	raw, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	remote := strings.TrimSuffix(strings.TrimSpace(string(raw)), ".git")
	for _, prefix := range []string{"https://github.com/", "git@github.com:", "ssh://git@github.com/"} {
		if strings.HasPrefix(remote, prefix) {
			repo := strings.TrimPrefix(remote, prefix)
			if len(strings.Split(repo, "/")) == 2 {
				return repo
			}
		}
	}
	return ""
}

func resolveHandoffTarget(claim handoff.Claim, repo string) (string, error) {
	target := strings.TrimSpace(claim.Target)
	if qualified, number, ok := strings.Cut(target, "#"); ok && qualified != "" && !strings.Contains(target, "://") {
		if !strings.Contains(claim.Event.Text, target) {
			return "", fmt.Errorf("qualified PR reference is absent from the source event")
		}
		candidate := "https://github.com/" + qualified + "/pull/" + number
		prs := core.PRsIn(candidate)
		if len(prs) != 1 || prs[0].URL != candidate {
			return "", fmt.Errorf("invalid qualified PR reference %s", target)
		}
		return candidate, nil
	}
	if strings.HasPrefix(target, "#") {
		if repo == "" {
			return "", fmt.Errorf("cannot resolve %s without verified repository context", target)
		}
		candidate := "https://github.com/" + repo + "/pull/" + strings.TrimPrefix(target, "#")
		prs := core.PRsIn(candidate)
		if len(prs) != 1 || prs[0].URL != candidate {
			return "", fmt.Errorf("invalid PR reference %s", target)
		}
		if !strings.Contains(claim.Event.Text, target) {
			return "", fmt.Errorf("PR reference %s is absent from the source event", target)
		}
		return candidate, nil
	}
	if target != "" && !strings.Contains(claim.Event.Text, target) && !strings.HasPrefix(claim.Existing, "review:") {
		return "", fmt.Errorf("target %s is not grounded in the source event", target)
	}
	if strings.HasPrefix(target, "https://github.com/") {
		prs := core.PRsIn(target)
		if len(prs) != 1 {
			return "", fmt.Errorf("GitHub artifact is not a pull request: %s", target)
		}
		return prs[0].URL, nil
	}
	if strings.HasPrefix(target, "file://") || filepath.IsAbs(target) {
		return intake.Identity("file", target)
	}
	if strings.Contains(target, "notion.so/") || strings.Contains(target, "notion.site/") {
		return intake.Identity("notion", target)
	}
	return target, nil
}

func applyHandoffs(c *core.Core) error {
	claims, err := c.Store.Handoffs.Claims("", true)
	if err != nil {
		return err
	}
	for i, claim := range claims {
		if i == 16 {
			break
		}
		target, err := applyHandoff(c, claim)
		if err != nil {
			if errors.Is(err, work.ErrConflict) {
				continue
			}
			if e := c.Store.Handoffs.Issue(claim.ID, err); e != nil {
				return e
			}
			continue
		}
		if err = c.Store.Handoffs.Applied(claim.ID, target); err != nil {
			return err
		}
	}
	return nil
}

func applyHandoff(c *core.Core, claim handoff.Claim) (string, error) {
	w, err := c.Store.Work.Get(claim.Event.Work)
	if err != nil {
		return "", err
	}
	existing := claim.Existing
	if strings.HasPrefix(existing, "claim:") {
		prior, err := c.Store.Handoffs.Claims(w.ID, false)
		if err != nil {
			return "", err
		}
		found := false
		for _, p := range prior {
			if p.ID == strings.TrimPrefix(existing, "claim:") {
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("unknown existing handoff %s", existing)
		}
		existing, err = c.Store.Handoffs.Target(strings.TrimPrefix(existing, "claim:"))
		if err != nil {
			return "", err
		}
	}
	if strings.HasPrefix(existing, "item:") {
		if claim.Kind == "review" {
			return "", fmt.Errorf("review request cannot be satisfied by an authorization or design decision")
		}
		it, err := c.Store.Item(strings.TrimPrefix(existing, "item:"))
		if err != nil {
			return "", err
		}
		if it.Scope != w.Repo {
			return "", fmt.Errorf("matched item belongs to another project")
		}
		if err = deliverHandoffAnswer(c, w, claim, it); err != nil {
			return "", err
		}
		return "item:" + it.Key(), nil
	}
	if strings.HasPrefix(existing, "review:") {
		r, ok := w.Reviews[strings.TrimPrefix(existing, "review:")]
		if !ok {
			return "", fmt.Errorf("unknown existing review %s", existing)
		}
		if r.Action == "merge" {
			return "", fmt.Errorf("merge and review are different owner obligations")
		}
		if claim.Target == "" {
			claim.Target = r.URL
		}
		resolved, err := resolveHandoffTarget(claim, handoffRepo(w.Repo))
		if err != nil {
			return "", err
		}
		if resolved != r.URL {
			return "", fmt.Errorf("matched review and requested artifact differ")
		}
	}
	if claim.Kind == "chat" {
		claim.Target = ""
	}
	target, err := resolveHandoffTarget(claim, handoffRepo(w.Repo))
	if err != nil {
		return "", err
	}
	if claim.Kind == "review" || claim.Kind == "proposal" && target != "" {
		return captureHandoffReview(c, w, claim, target)
	}
	if existing != "" {
		return "", fmt.Errorf("unsupported existing obligation %s", existing)
	}
	kind := core.KindDecision
	action := ""
	if claim.Kind == "chat" {
		kind = core.KindChore
		action = "chat"
	}
	if claim.Kind == "authorization" {
		kind = core.KindGo
	}
	key := handoff.Key(w.ID, claim.Kind, strings.ToLower(strings.Join(strings.Fields(claim.Topic), " ")))
	it, err := c.Store.AddHandoffItem(key, core.Item{Scope: w.Repo, Session: claim.Event.Session, Harness: claim.Event.Harness, Kind: kind, Action: action, Question: claim.Question, Rec: claim.Recommendation, Assumption: fmt.Sprintf("Recovered request from agent %s. Source %s at byte %d, event %s.\n%s", claim.Event.Agent, claim.Event.Path, claim.Event.Offset, claim.Event.ID, claim.Quote)})
	if err != nil {
		return "", err
	}
	if err = deliverHandoffAnswer(c, w, claim, it); err != nil {
		return "", err
	}
	return "item:" + it.Key(), nil
}

func deliverHandoffAnswer(c *core.Core, w work.Scope, claim handoff.Claim, it core.Item) error {
	if it.State != core.ItemAnswered && it.State != core.ItemDismissed {
		return nil
	}
	eventID := "handoff-answer/" + handoff.Key(it.Key(), claim.Event.Session, it.Answer, it.Reason)
	if c.Store.Work.HasEvent(eventID) {
		return nil
	}
	current, err := c.Store.Work.Get(w.ID)
	if err != nil {
		return err
	}
	text := fmt.Sprintf("Recorded owner response, not an agent recommendation: %s (%s), %s.\nQuestion: %s\nOwner answer: %s\nDismissal reason: %s\nRecorded at: %s\nUse this existing ledger response; do not ask the owner to repeat it.", it.Qualified(), it.Key(), it.State, it.Question, it.Answer, it.Reason, it.AnsweredAt.String())
	_, err = c.Store.Work.Commit(work.Event{ID: eventID, Work: w.ID, Expected: current.Revision, Type: "message", Actor: "Yamnaya ledger", Target: claim.Event.Session, Text: text})
	return err
}

func captureHandoffReview(c *core.Core, w work.Scope, claim handoff.Claim, target string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("review request has no resolvable artifact target")
	}
	all, err := c.Store.Work.List()
	if err != nil {
		return "", err
	}
	var source work.Source
	registered := false
	for _, scope := range all {
		for _, s := range scope.Sources {
			if s.URL == target {
				w, source, registered = scope, s, true
				break
			}
		}
	}
	if !registered {
		source = work.Source{Kind: "github", URL: target, Origin: "agent-handoff", Requester: "Agent " + claim.Event.Agent, Question: claim.Question, Coverage: "explicit agent handoff; source and readiness independently checked"}
		if prs := core.PRsIn(target); len(prs) == 1 && prs[0].URL == target {
			source.Kind = "github"
		} else if strings.HasPrefix(target, "https://www.notion.so/") {
			source.Kind = "notion"
		} else if strings.HasPrefix(target, "https://") {
			source.Kind = "artifact"
		} else if filepath.IsAbs(target) || strings.HasPrefix(target, "file://") {
			source.Kind = "file"
		} else {
			return "", fmt.Errorf("artifact target is ambiguous: %s", target)
		}
	}
	var status *core.PRStatus
	if source.Kind == "github" {
		requireOwnership := !registered || source.Requester == "" || source.Origin == "agent-handoff"
		if c.PRs == nil && requireOwnership {
			return "", fmt.Errorf("GitHub authorship verification is unavailable")
		}
		if c.PRs != nil {
			st, err := c.PRs.PRStatus(target)
			if err != nil {
				return "", err
			}
			if requireOwnership && !st.OwnedPR() {
				return "", fmt.Errorf("PR authorship is not verified as your GitHub account: %s", target)
			}
			status = &st
			source.AgentPR = source.AgentPR || st.OwnedPR()
		}
	}
	if source.Requester == "" {
		source.Requester = "Agent " + claim.Event.Agent
		source.Question = claim.Question
	}
	var review work.Review
	if source.Kind == "artifact" {
		review = work.Review{ID: target, URL: target, Kind: "proposal", Title: claim.Question, Version: handoff.Key("unverified-artifact", target), StatusError: "Artifact content is not independently verified; this is an agent request", Requester: source.Requester, Question: source.Question, Evidence: claim.Quote}
	} else {
		if c.CheckReview == nil {
			return "", fmt.Errorf("source verification is unavailable for %s", target)
		}
		review, err = c.CheckReview(source)
		if err != nil {
			return "", err
		}
	}
	if status != nil {
		if head, _, _ := strings.Cut(review.Version, ":"); head != status.Merge.OID {
			status.Merge.Error = "PR head changed between readiness and review observations"
			status.Fetched = time.Time{}
		}
		if err = c.Store.SavePRStatus(target, *status); err != nil {
			return "", err
		}
	}
	review.Handoff = &work.Handoff{Event: claim.Event.ID, Agent: claim.Event.Agent, Session: claim.Event.Session, Path: claim.Event.Path, Offset: claim.Event.Offset, At: claim.Event.At, Quote: claim.Quote, Version: review.Version}
	eventID := "handoff/" + claim.ID
	if c.Store.Work.HasEvent(eventID) {
		return "review:" + review.ID, nil
	}
	current, err := c.Store.Work.Get(w.ID)
	if err != nil {
		return "", err
	}
	if old, exists := current.Reviews[review.ID]; exists && old.Version != review.Version && review.Order >= old.Order {
		sourceID := "handoff-source/" + claim.ID
		if !c.Store.Work.HasEvent(sourceID) {
			current, err = c.Store.Work.Commit(work.Event{ID: sourceID, Work: w.ID, Expected: current.Revision, Type: "observe", Actor: "source", Review: review, PreviousVersion: old.Version})
			if err != nil {
				return "", err
			}
		}
	}
	_, err = c.Store.Work.Commit(work.Event{ID: eventID, Work: w.ID, Expected: current.Revision, Type: "handoff", Actor: "Agent " + claim.Event.Agent, Source: source, Review: review})
	return "review:" + review.ID, err
}
