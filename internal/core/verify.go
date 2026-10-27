package core

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	VerdictPass = "pass"
	VerdictFail = "fail"
	// VerdictError ends an attempt that could not reach a verdict: facts that never settled, or a verifier that kept dying.
	VerdictError = "error"
	// VerdictSuperseded ends an attempt the manager replaced by calling ym goal done again.
	VerdictSuperseded = "superseded"

	// VerifyTimeout is how long a verifier session may run without a verdict before ym ends it and starts a fresh one.
	VerifyTimeout = 30 * time.Minute
	// VerifierLaunches is how many verifier sessions in a row may end without a verdict before the goal shows as stuck.
	VerifierLaunches = 3
	// FactsTimeout is how long facts may stay pending, as CI on a merge commit still running, before the goal shows as stuck.
	FactsTimeout = 2 * time.Hour
	factsEvery   = time.Minute
)

const verifyEvidence = "verify: "

// Landing is what GitHub says about a pull request the goal claims: whether it merged, CI on its merge commit, and open threads.
type Landing struct {
	State       string
	MergeCommit string
	Passed      int
	Failed      int
	Pending     int
	Unresolved  int
}

// Landings reads a pull request's landing from GitHub.
type Landings interface {
	Landing(url string) (Landing, error)
}

// FinishGoal hands the goal the calling manager owns to verification: it is done only once its facts check and a fresh verifier
// session passes it.
func (c *Core) FinishGoal(session, summary string, evidence []string) (Goal, Verification, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return Goal{}, Verification{}, err
	}
	g, err := o.goalOf(c.Resolve(session))
	if err != nil {
		return g, Verification{}, err
	}
	if g.Ended() {
		return g, Verification{}, fmt.Errorf("%s is %s already", g.ID, g.State)
	}
	if open, err := c.Store.OpenVerification(g.ID); err == nil {
		open.Verdict, open.DecidedAt = VerdictSuperseded, c.now()
		if err := c.Store.SaveVerification(open); err != nil {
			return g, open, err
		}
	} else if !errors.Is(err, ErrNotFound) {
		return g, open, err
	}
	var lines []string
	for _, e := range evidence {
		if e = strings.TrimSpace(e); e != "" {
			lines = append(lines, e)
		}
	}
	v, err := c.Store.AddVerification(Verification{Goal: g.ID, Summary: strings.TrimSpace(summary), Evidence: lines, CreatedAt: c.now()})
	if err != nil {
		return g, v, err
	}
	g.State, g.Summary, g.Evidence, g.AckedAt = GoalVerifying, v.Summary, "", timeZero
	if err := c.Store.SaveGoal(g); err != nil {
		return g, v, err
	}
	return g, v, c.goalEvent(g, EventVerifying, fmt.Sprintf("attempt %d: %s", v.Attempt, v.Summary), true)
}

// verify moves every verifying goal on: facts first, then a verifier session, supervised until it records a verdict.
func (c *Core) verify(b Board, o Org) error {
	var errs []error
	for _, m := range o.Ordered() {
		g, ok := o.Goals[m.GoalID]
		if !ok || g.State != GoalVerifying {
			continue
		}
		v, err := c.Store.OpenVerification(g.ID)
		if errors.Is(err, ErrNotFound) {
			v, err = c.Store.AddVerification(Verification{Goal: g.ID, Summary: g.Summary, CreatedAt: c.now()})
		}
		if err == nil {
			err = c.verifyOne(b, o, m, g, v)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("verify %s: %w", g.ID, err))
		}
	}
	return errors.Join(append(errs, c.endVerifiers(b))...)
}

func (c *Core) verifyOne(b Board, o Org, m Manager, g Goal, v Verification) error {
	now := c.now()
	if v.LaunchedAt.IsZero() {
		if !v.FactsAt.IsZero() && now.Sub(v.FactsAt) < factsEvery {
			return nil
		}
		v.Evidence = c.withAgentPRs(b, o, m, v.Evidence)
		v.Facts, v.FactsAt = c.checkFacts(prsOf(v), now), now
		var failed, pending []string
		for _, f := range v.Facts {
			switch {
			case f.Pending:
				pending = append(pending, f.String())
			case !f.OK:
				failed = append(failed, f.String())
			}
		}
		switch {
		case len(failed) > 0:
			return c.decide(b, o, g, v, VerdictFail, failed)
		case len(pending) > 0 && now.Sub(v.CreatedAt) >= FactsTimeout:
			return c.decide(b, o, g, v, VerdictError, append([]string{fmt.Sprintf("facts still pending after %s", FactsTimeout)}, pending...))
		case len(pending) > 0:
			return c.Store.SaveVerification(v)
		}
		return c.launchVerifier(o, g, v)
	}
	s, alive := b.Session(v.Session)
	starting := now.Sub(v.LaunchedAt) < StartGrace && v.LastError == ""
	switch {
	case alive && now.Sub(v.LaunchedAt) >= VerifyTimeout:
		if c.Launches != nil {
			if _, err := c.Launches.End(v.Session, ""); err != nil {
				return err
			}
		}
		v.LastError = fmt.Sprintf("verifier %s gave no verdict in %s", Short(v.Session), VerifyTimeout)
	case alive:
		if s.Status != Idle || s.Prompt != nil || !v.NudgedAt.IsZero() || starting {
			return nil
		}
		text := fmt.Sprintf("ym: you have not recorded a verdict on %s. Finish checking, then run ym goal verdict %s --pass or --fail with a --finding for each concrete gap.", g.ID, g.ID)
		if err := c.tell(b, v.Session, text, false); err != nil {
			return err
		}
		v.NudgedAt = now
		return c.Store.SaveVerification(v)
	case starting || now.Before(v.NextAttempt):
		return nil
	case v.LastError == "":
		v.LastError = fmt.Sprintf("verifier %s ended without a verdict", Short(v.Session))
	}
	v.Failures++
	if v.Failures >= VerifierLaunches {
		return c.decide(b, o, g, v, VerdictError, []string{fmt.Sprintf("%d verifier sessions in a row gave no verdict; last: %s", v.Failures, v.LastError)})
	}
	v.NextAttempt = now.Add(backoff(v.Failures))
	return c.launchVerifier(o, g, v)
}

// launchVerifier starts a fresh verifier session on v, in a new worktree of the goal's repository.
func (c *Core) launchVerifier(o Org, g Goal, v Verification) error {
	v.LaunchedAt, v.NudgedAt = c.now(), timeZero
	if c.Launches == nil {
		v.Session, v.LastError = "", "ym's launcher is unavailable"
		return c.Store.SaveVerification(v)
	}
	st, err := c.Launches.Start(NewLaunch{Prompt: c.verifierPrompt(o, g, v), Harness: o.Managers[g.ManagerID].Harness, Profile: o.Managers[g.ManagerID].Profile, ConfigRoot: o.Managers[g.ManagerID].ConfigRoot, Repo: g.Repo,
		Slug: fmt.Sprintf("verify-%s-%d", g.ID, v.Attempt), Session: uuid.NewString(), Await: true})
	if err != nil {
		v.Session, v.LastError = "", err.Error()
		return c.Store.SaveVerification(v)
	}
	v.Session, v.LastError = st.Session, ""
	return c.Store.SaveVerification(v)
}

// RecordVerdict takes the verdict of goal's verifier, which only the verifier session itself may give.
func (c *Core) RecordVerdict(session, goal, verdict string, findings []string) (Verification, error) {
	g, err := c.Store.Goal(goal)
	if err != nil {
		return Verification{}, err
	}
	v, err := c.Store.OpenVerification(g.ID)
	if err != nil {
		return v, err
	}
	if session == "" || (session != v.Session && c.Resolve(session) != v.Session) {
		return v, fmt.Errorf("only %s's verifier session %s may record its verdict", g.ID, Short(v.Session))
	}
	var lines []string
	for _, f := range findings {
		if f = strings.TrimSpace(f); f != "" {
			lines = append(lines, f)
		}
	}
	switch verdict {
	case VerdictPass:
	case VerdictFail:
		if len(lines) == 0 {
			return v, errors.New("a failing verdict needs at least one --finding naming what is missing or wrong")
		}
	default:
		return v, fmt.Errorf("a verdict is %s or %s, not %q", VerdictPass, VerdictFail, verdict)
	}
	b, err := c.Board()
	if err != nil {
		return v, err
	}
	o, err := c.LoadOrg()
	if err != nil {
		return v, err
	}
	v.LastError = ""
	return v, c.decide(b, o, g, v, verdict, lines)
}

// decide records v's verdict and moves the goal: a pass makes it done for the owner, a fail hands the findings back to the manager,
// an error shows it stuck.
func (c *Core) decide(b Board, o Org, g Goal, v Verification, verdict string, findings []string) error {
	now := c.now()
	v.Verdict, v.Findings, v.DecidedAt = verdict, findings, now
	if err := c.Store.SaveVerification(v); err != nil {
		return err
	}
	list := "\n  - " + strings.Join(findings, "\n  - ")
	switch verdict {
	case VerdictPass:
		g.State, g.DoneAt, g.Evidence, g.AckedAt = GoalDone, now, "", timeZero
	case VerdictFail:
		g.State, g.Evidence, g.LastMovedAt = GoalActive, "", now
	default:
		g.State, g.Evidence, g.AckedAt = GoalStuck, verifyEvidence+strings.Join(findings, "; "), timeZero
	}
	if err := c.Store.SaveGoal(g); err != nil {
		return err
	}
	switch verdict {
	case VerdictPass:
		if err := c.goalEvent(g, EventVerified, fmt.Sprintf("attempt %d passed:%s", v.Attempt, list), true); err != nil {
			return err
		}
		if err := c.goalEvent(g, EventDone, g.Summary, true); err != nil {
			return err
		}
		if m, ok := o.Managers[g.ManagerID]; ok && m.Session != "" {
			return c.tell(b, m.Session, fmt.Sprintf("ym: %s passed verification (attempt %d). It is done and the owner sees it.", g.ID, v.Attempt), false)
		}
		return nil
	case VerdictFail:
		return c.goalEvent(g, EventFailedVerify, fmt.Sprintf("attempt %d failed, so the goal is active again. Fix each finding, then run ym goal done again:%s", v.Attempt, list), false)
	}
	return c.goalEvent(g, EventStuck, fmt.Sprintf("verification attempt %d could not finish, so the goal shows stuck to the owner. Once the cause is fixed, run ym goal done again:%s", v.Attempt, list), false)
}

// endVerifiers ends the session of every decided verification whose verifier still runs.
func (c *Core) endVerifiers(b Board) error {
	all, err := c.Store.Verifications("")
	if err != nil {
		return err
	}
	var errs []error
	for _, v := range all {
		if v.Verdict == "" || v.Ended {
			continue
		}
		if _, alive := b.Session(v.Session); alive && c.Launches != nil {
			if _, err := c.Launches.End(v.Session, ""); err != nil {
				errs = append(errs, fmt.Errorf("end verifier %s: %w", Short(v.Session), err))
				continue
			}
		}
		v.Ended = true
		errs = append(errs, c.Store.SaveVerification(v))
	}
	return errors.Join(errs...)
}

// withAgentPRs adds to evidence the pull requests of m's own session and its agents not already in it.
func (c *Core) withAgentPRs(b Board, o Org, m Manager, evidence []string) []string {
	have := map[string]bool{}
	for _, pr := range PRsIn(strings.Join(evidence, "\n")) {
		have[pr.URL] = true
	}
	if tracked, err := c.Store.GoalPRs(); err == nil {
		for _, url := range tracked[m.GoalID] {
			if !have[url] {
				have[url] = true
				evidence = append(evidence, "opened by the goal's sessions: "+url)
			}
		}
	}
	for _, s := range b.Sessions {
		if owner, ok := o.Managed(s.Key(), b.parents); !ok || owner.ID != m.ID {
			continue
		}
		for _, pr := range s.AllPRs() {
			if !have[pr.URL] {
				have[pr.URL] = true
				evidence = append(evidence, "opened by the goal's sessions: "+pr.URL)
			}
		}
	}
	return evidence
}

// prsOf lists the pull requests v's summary and evidence name, once each.
func prsOf(v Verification) []string {
	var out []string
	for _, pr := range PRsIn(v.Summary + "\n" + strings.Join(v.Evidence, "\n")) {
		if !slices.Contains(out, pr.URL) {
			out = append(out, pr.URL)
		}
	}
	return out
}

// checkFacts checks each pull request is merged, green on its merge commit, and has no unresolved review threads.
func (c *Core) checkFacts(prs []string, now time.Time) []Fact {
	var out []Fact
	for _, url := range prs {
		if c.Landings == nil {
			out = append(out, Fact{Check: "merged", Subject: url, Pending: true, Detail: "ym cannot reach GitHub from here"})
			continue
		}
		l, err := c.Landings.Landing(url)
		if err != nil {
			out = append(out, Fact{Check: "merged", Subject: url, Pending: true, Detail: err.Error()})
			continue
		}
		switch l.State {
		case "MERGED":
			out = append(out, Fact{Check: "merged", Subject: url, OK: true, Detail: "merged as " + shortSHA(l.MergeCommit)})
			ci := Fact{Check: "ci on merge commit", Subject: url, OK: true}
			switch {
			case l.Failed > 0:
				ci.OK, ci.Detail = false, fmt.Sprintf("%d of %d checks failed on %s", l.Failed, l.Passed+l.Failed+l.Pending, shortSHA(l.MergeCommit))
			case l.Pending > 0:
				ci.Pending, ci.Detail = true, fmt.Sprintf("%d of %d checks still running on %s", l.Pending, l.Passed+l.Failed+l.Pending, shortSHA(l.MergeCommit))
			case l.Passed == 0:
				ci.Detail = "no checks ran on " + shortSHA(l.MergeCommit)
			default:
				ci.Detail = fmt.Sprintf("%d checks passed on %s", l.Passed, shortSHA(l.MergeCommit))
			}
			out = append(out, ci)
		case "OPEN":
			out = append(out, Fact{Check: "merged", Subject: url, Detail: "still open"})
		case "CLOSED":
			out = append(out, Fact{Check: "merged", Subject: url, Detail: "closed without merging"})
		default:
			out = append(out, Fact{Check: "merged", Subject: url, Detail: "GitHub has no such pull request"})
		}
		threads := Fact{Check: "review threads", Subject: url, OK: l.Unresolved == 0, Detail: "none unresolved"}
		if l.Unresolved > 0 {
			threads.Detail = fmt.Sprintf("%d unresolved", l.Unresolved)
		}
		out = append(out, threads)
	}
	return out
}

func shortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	if sha == "" {
		return "an unknown commit"
	}
	return sha
}

func (c *Core) verifierPrompt(o Org, g Goal, v Verification) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are the verifier of goal %s in yamnaya (ym), attempt %d. You did not do this work and you are not its manager. Decide independently whether the goal is really done as the owner asked. Do not trust the claims below: check them against the repository, GitHub and the code's actual behaviour.\n\n", g.ID, v.Attempt)
	fmt.Fprintf(&sb, "The owner's ask, in their own words:\n%s\n\n", g.Ask)
	if steers, err := c.Store.Steers(g.ManagerID); err == nil && len(steers) > 0 {
		st := steers[len(steers)-1]
		fmt.Fprintf(&sb, "The plan was steered since: revision %d by %s replaces the ask where they differ. Judge the work against it:\n%s\n\n", st.Revision, st.By(), st.Text)
	}
	fmt.Fprintf(&sb, "The manager's summary:\n%s\n\n", v.Summary)
	sb.WriteString("Evidence the manager claims:\n")
	if len(v.Evidence) == 0 {
		sb.WriteString("- none\n")
	}
	for _, e := range v.Evidence {
		sb.WriteString("- " + e + "\n")
	}
	sb.WriteString("\nFacts ym checked on GitHub:\n")
	if len(v.Facts) == 0 {
		sb.WriteString("- none: the goal names no pull requests\n")
	}
	for _, f := range v.Facts {
		sb.WriteString("- " + f.String() + "\n")
	}
	brief := g.BriefPath
	if m, ok := o.Managers[g.ManagerID]; ok && m.BriefPath != "" {
		brief = m.BriefPath
	}
	fmt.Fprintf(&sb, "\nRepository: %s (%s). You run in a fresh worktree of it; git fetch and read origin's default branch, which is where finished work lands. The manager's brief is at %s.\n\n", filepath.Base(g.Repo), g.Repo, brief)
	sb.WriteString("Rules:\n")
	sb.WriteString("- Read only: do not edit, commit, push, merge, comment, or message anyone. Running tests, builds and the program to check behaviour is fine.\n")
	sb.WriteString("- Check every part of the ask, not only what the summary mentions. A part not done, done differently than asked, or not shown to work is a failure.\n")
	fmt.Fprintf(&sb, "- When you have decided, run exactly one of:\n  ym goal verdict %s --pass --finding \"<what you checked>\"\n  ym goal verdict %s --fail --finding \"<concrete gap>\" --finding \"<another>\"\n", g.ID, g.ID)
	sb.WriteString("  Each finding is one concrete, checkable line: what is missing or wrong, and where.\n")
	sb.WriteString("- Then stop. ym ends this session.")
	return sb.String()
}
