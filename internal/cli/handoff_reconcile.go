package cli

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/handoff"
)

type existingItem struct{ ID, Kind, Question, Recommendation, State, Answer, Session, AnsweredAt string }
type existingRequest struct{ ID, Kind, Target, Question string }

// handoffLedger is what the classifier matches requests against: the session's open and answered items and its prior applied claims.
func handoffLedger(c *core.Core, e handoff.Event) (any, error) {
	items, err := c.Store.Items(core.ItemFilter{Sessions: []string{e.Session}, States: []string{core.ItemOpen, core.ItemAnswered}})
	if err != nil {
		return nil, err
	}
	ledger := struct {
		Session, Repository string
		ExistingItems       []existingItem
		ExistingRequests    []existingRequest
	}{Session: e.Session, Repository: handoffRepo(e.Dir)}
	for _, it := range items {
		ledger.ExistingItems = append(ledger.ExistingItems, existingItem{ID: "item:" + it.Key(), Kind: it.Kind, Question: it.Question, Recommendation: it.Rec, State: it.State, Answer: it.Answer, Session: it.Session, AnsweredAt: it.AnsweredAt.String()})
	}
	claims, err := c.Store.Handoffs.Claims(e.Work, false)
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
	if target != "" && !strings.Contains(claim.Event.Text, target) {
		return "", fmt.Errorf("target %s is not grounded in the source event", target)
	}
	if strings.HasPrefix(target, "https://github.com/") {
		prs := core.PRsIn(target)
		if len(prs) != 1 {
			return "", fmt.Errorf("GitHub artifact is not a pull request: %s", target)
		}
		return prs[0].URL, nil
	}
	if path := strings.TrimPrefix(target, "file://"); path != target || filepath.IsAbs(target) {
		return "file://" + filepath.Clean(path), nil
	}
	return target, nil
}

func applyHandoffs(c *core.Core, b core.Board) error {
	claims, err := c.Store.Handoffs.Claims("", true)
	if err != nil {
		return err
	}
	for i, claim := range claims {
		if i == 16 {
			break
		}
		target, err := applyHandoff(c, b, claim)
		if err != nil {
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

func applyHandoff(c *core.Core, b core.Board, claim handoff.Claim) (string, error) {
	existing := claim.Existing
	if prior, ok := strings.CutPrefix(existing, "claim:"); ok {
		claims, err := c.Store.Handoffs.Claims(claim.Event.Work, false)
		if err != nil {
			return "", err
		}
		found := false
		for _, p := range claims {
			found = found || p.ID == prior
		}
		if !found {
			return "", fmt.Errorf("unknown existing handoff %s", existing)
		}
		if existing, err = c.Store.Handoffs.Target(prior); err != nil {
			return "", err
		}
	}
	if ref, ok := strings.CutPrefix(existing, "item:"); ok {
		if claim.Kind == "review" {
			return "", fmt.Errorf("review request cannot be satisfied by an authorization or design decision")
		}
		it, err := c.Store.Item(ref)
		if err != nil {
			return "", err
		}
		if it.Session != claim.Event.Session {
			return "", fmt.Errorf("matched item belongs to another session")
		}
		return "item:" + it.Key(), nil
	}
	if existing != "" {
		return "", fmt.Errorf("unsupported existing obligation %s", existing)
	}
	if claim.Kind == "chat" {
		claim.Target = ""
	}
	target, err := resolveHandoffTarget(claim, handoffRepo(claim.Event.Dir))
	if err != nil {
		return "", err
	}
	it := core.Item{Session: claim.Event.Session, Harness: claim.Event.Harness, Kind: core.KindDecision, Question: claim.Question, Rec: claim.Recommendation,
		Assumption: fmt.Sprintf("Recovered request from agent %s%s. Source %s at byte %d, event %s.\n%s", claim.Event.Agent, onBehalfOf(claim), claim.Event.Path, claim.Event.Offset, claim.Event.ID, claim.Quote)}
	switch {
	case claim.Kind == "chat":
		it.Kind, it.Action = core.KindChore, "chat"
	case claim.Kind == "authorization":
		it.Kind = core.KindGo
	case claim.Kind == "review" || claim.Kind == "proposal" && target != "":
		it.Kind = core.KindChore
		if !strings.Contains(it.Question, target) {
			it.Question = strings.TrimSpace(it.Question + " " + target)
		}
	}
	if it.Scope = c.ScopeOf(b, it.Session); it.Scope == "" {
		it.Scope = claim.Event.Dir
	}
	key := handoff.Key(claim.Event.Work, claim.Kind, strings.ToLower(strings.Join(strings.Fields(claim.Topic), " ")))
	if it, err = c.AddHandoffItem(key, it); err != nil {
		return "", err
	}
	return "item:" + it.Key(), nil
}

func onBehalfOf(claim handoff.Claim) string {
	if claim.Requester == "" {
		return ""
	}
	return " on behalf of " + claim.Requester
}
