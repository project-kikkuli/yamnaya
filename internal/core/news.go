package core

import (
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

var (
	stillWaiting   = regexp.MustCompile(`(?i)\b(still|waiting|wait for|running|in progress|once (it|the)|when it|until|will (start|arrive|push|report|send|verify))\b`)
	saysNothingNew = regexp.MustCompile(`(?i)\b(nothing (new|has changed|changed|to act on|for you to do)|no change|repeats (its|the|what))\b`)
	words          = regexp.MustCompile(`[a-z0-9]{3,}|[0-9]+`)
	sentenceEnd    = regexp.MustCompile(`[.!?]\s+|\n+`)
	statesFailure  = regexp.MustCompile(`(?i)\b(fail|failed|fails|failing|failures?|breaks?|broke|broken|crash(ed|es|ing)?|errored|errors?|refused|rejected|denied|timed out|stuck|blocked|conflicts?|conflicting|regress(ed|ion)|needs?[- ]rework|(did not|didn't|does not|doesn't) (take|work|pass|compile)|(can't|cannot|couldn't|could not|unable to) \w+|(is|are|went|turned|still) red)\b`)
	hedgedFailure  = regexp.MustCompile(`(?i)\b(no|any|first|real|code|(not|isn't|wasn't) an?)\s+(\w+\s+)?(failures?|errors?|conflicts?)\b|\b(no|nothing|none|not|hasn't|haven't|hadn't)\s+(\w+\s+){0,2}(fail|failed|failing|broke|broken|stuck|blocked)\b|\b(if|unless|whether|in case)\b[^,;]*\b(fail\w*|breaks?|crash\w*|errors?|conflicts?)\b|\b(when|once|until|should|would)\s+(\w+\s+){0,3}(fail|fails|breaks?|crash(es)?)\b|\b(failed|failing|failures?)[- ](before|paths?|tests?|cases?|modes?|handling)\b`)
	asksManager    = regexp.MustCompile(`(?i)(\byour (call|choice|decision|input|word|approval|acceptance|go-ahead)\b|\byou (decide|choose|pick)\b|\bup to you\b|\b(which|what) (do|would|should) you\b|\bdo you want\b|\bwould you (like|prefer|rather)\b|\bshould i\b|\bshall i\b|\bwant me to\b|\btell me\b|\blet me know\b|\bplease (confirm|decide|advise|choose|pick)\b|\bneeds? (you|your|a decision)\b|\b(waiting|blocked) (on|for) (you|your)\b|, confirm\b|\?[*_\x60"')]*$)`)
)

const (
	// shortReply is the longest reply that can be only a still-waiting note.
	shortReply = 300
	// sameReply is how alike two replies' words must be for the second to repeat the first.
	sameReply = 0.6
)

// news reports why an idle agent's reply tells its manager nothing, "" when it tells it something: its first reply, a commit since
// its last one, an ask it filed since, or words that raise a failure or a decision, or that neither repeat the last reply handed on,
// nor only say it is still waiting or that nothing is new. ym cannot read a remote session's head, so it may have committed.
func (c *Core) news(s Live) (string, error) {
	key := s.Key()
	head := worktreeHead(s)
	last, seen, err := c.Store.lastReport(key)
	if err != nil {
		return "", err
	}
	said := strings.TrimSpace(s.Result)
	counted := ""
	if seen && s.Remote == "" && head == last.Head {
		counted = addsNothing(said, last.Said)
	}
	if counted != "" {
		items, err := c.Store.Items(ItemFilter{Sessions: []string{key}})
		if err != nil {
			return "", err
		}
		for _, it := range items {
			if it.CreatedAt.After(last.At) {
				counted = ""
			}
		}
	}
	r := lastReport{Head: head, Said: last.Said, At: s.ResultAt}
	if counted == "" {
		r.Said = said
	}
	if counted != "" {
		if head == "" {
			counted = "no new ask, " + counted
		} else {
			counted = "no new commit or ask, " + counted
		}
	}
	return counted, c.Store.noteReport(key, r)
}

// addsNothing says why said adds nothing to prev, the last reply handed on, "" when it does add something: a failure or a request
// to decide that prev did not hold always adds; otherwise mostly the same words, opening with nothing new, or a short still-waiting
// note do not.
func addsNothing(said, prev string) string {
	if raisesNews(said, prev) {
		return ""
	}
	if same := similarity(said, prev); same >= sameReply {
		return fmt.Sprintf("%d%% the same words as its last reply", int(same*100))
	}
	if first := firstSentence(said); saysNothingNew.MatchString(first) && !strings.Contains(first, " but ") {
		return "it opens by saying nothing is new"
	}
	if len([]rune(said)) <= shortReply && stillWaiting.MatchString(said) {
		return "a short still-waiting note"
	}
	return ""
}

// raisesNews reports whether a sentence of said states a failure or asks the manager to decide, and prev has no sentence of the same words.
func raisesNews(said, prev string) bool {
	before := sentences(prev)
	for _, s := range sentences(said) {
		if !asksManager.MatchString(s) && !statesFailure.MatchString(hedgedFailure.ReplaceAllString(s, "")) {
			continue
		}
		if !slices.ContainsFunc(before, func(p string) bool { return similarity(s, p) == 1 }) {
			return true
		}
	}
	return false
}

func sentences(s string) []string {
	var out []string
	start := 0
	for _, end := range sentenceEnd.FindAllStringIndex(s, -1) {
		out = append(out, strings.TrimSpace(s[start:end[1]]))
		start = end[1]
	}
	return append(out, strings.TrimSpace(s[start:]))
}

// similarity is the share of distinct words two texts have in common.
func similarity(a, b string) float64 {
	wa, wb := wordSet(a), wordSet(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	both := 0
	for w := range wa {
		if wb[w] {
			both++
		}
	}
	return float64(both) / float64(len(wa)+len(wb)-both)
}

func wordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range words.FindAllString(strings.ToLower(s), -1) {
		out[w] = true
	}
	return out
}

func firstSentence(s string) string {
	end := len(s)
	for _, sep := range []string{". ", "\n", "! ", "? "} {
		if i := strings.Index(s, sep); i >= 0 && i < end {
			end = i
		}
	}
	return s[:end]
}

// worktreeHead is the commit the session's worktree is at, "" when ym cannot read it here.
func worktreeHead(s Live) string {
	dir := s.Worktree
	if dir == "" {
		dir = s.Cwd
	}
	if dir == "" || s.Remote != "" {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
