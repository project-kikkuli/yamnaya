package core

import (
	"os/exec"
	"regexp"
	"strings"
)

var (
	stillWaiting   = regexp.MustCompile(`(?i)\b(still|waiting|wait for|running|in progress|once (it|the)|when it|until|will (start|arrive|push|report|send|verify))\b`)
	saysNothingNew = regexp.MustCompile(`(?i)\b(nothing (new|has changed|changed|to act on|for you to do)|no change|repeats (its|the|what))\b`)
	words          = regexp.MustCompile(`[a-z0-9]{3,}`)
)

const (
	// shortReply is the longest reply that can be only a still-waiting note.
	shortReply = 300
	// sameReply is how alike two replies' words must be for the second to repeat the first.
	sameReply = 0.6
)

// news reports whether an idle agent's reply tells its manager anything: its first reply, a commit since its last one, an ask it
// filed since, or words that neither repeat the last reply handed on, nor only say it is still waiting or that nothing is new.
func (c *Core) news(s Live) (bool, error) {
	key := s.Key()
	head := worktreeHead(s)
	last, seen, err := c.Store.lastReport(key)
	if err != nil {
		return true, err
	}
	said := strings.TrimSpace(s.Result)
	fresh := !seen || head != last.Head || saysNew(said, last.Said)
	if !fresh {
		items, err := c.Store.Items(ItemFilter{Sessions: []string{key}})
		if err != nil {
			return true, err
		}
		for _, it := range items {
			fresh = fresh || it.CreatedAt.After(last.At)
		}
	}
	r := lastReport{Head: head, Said: last.Said, At: s.ResultAt}
	if fresh {
		r.Said = said
	}
	return fresh, c.Store.noteReport(key, r)
}

// saysNew reports whether said adds to prev, the last reply handed on: not mostly the same words, not opening with nothing new,
// and not a short still-waiting note.
func saysNew(said, prev string) bool {
	if similarity(said, prev) >= sameReply {
		return false
	}
	if first := firstSentence(said); saysNothingNew.MatchString(first) && !strings.Contains(first, " but ") {
		return false
	}
	return len([]rune(said)) > shortReply || !stillWaiting.MatchString(said)
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
