package tui

import (
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

var longID = regexp.MustCompile(`\b[0-9a-fA-F][0-9a-fA-F-]{11,}`)

// doingCell is the lead's last action cut to w cells, its age always kept.
func doingCell(now time.Time, s core.Live, w int) string {
	if s.Doing.Tool == "" {
		return ""
	}
	what, when := shortAction(s.Doing, s.Worktree, s.Cwd), age(now, s.Doing.At)
	room := w - ansi.StringWidth(when) - 1
	if room < 4 {
		return pad(what, w)
	}
	if ansi.StringWidth(what) > room {
		what = ansi.Truncate(what, room, "…")
	}
	return what + " " + pal.dim.Render(when)
}

// idleCell is an idle session's last line, behind a dot when it said it after the owner last opened the session.
func idleCell(s core.Live, w int) string {
	dot := "  "
	if s.Unread {
		dot = pal.yellow.Render("●") + " "
	}
	return dot + ansi.Truncate(pal.dim.Render("idle · ")+s.LastLine(), max(4, w-2), "…")
}

// shortAction is an action cut to what tells it apart: a command by its name and words without its id flags, paths relative to dirs, long ids elided.
func shortAction(a core.Action, dirs ...string) string {
	if a.Detail == "" {
		return a.Tool
	}
	home, _ := os.UserHomeDir()
	words := strings.Fields(a.Detail)
	if a.Tool == "Bash" {
		words = dropFlags(words)
	}
	for i, w := range words {
		if strings.Contains(w, "/") && !strings.Contains(w, "://") {
			w = shortPath(w, home, dirs)
			if i == 0 && a.Tool == "Bash" {
				w = path.Base(w)
			}
		}
		words[i] = longID.ReplaceAllStringFunc(w, func(id string) string {
			if !strings.ContainsAny(id, "0123456789") {
				return id
			}
			return id[:6] + "…"
		})
	}
	return a.Tool + ": " + strings.Join(words, " ")
}

// shortPath makes p relative to the first dir holding it, or to home, then keeps only its first and last parts.
func shortPath(p, home string, dirs []string) string {
	for _, d := range dirs {
		if d != "" && strings.HasPrefix(p, d+"/") {
			p = p[len(d)+1:]
			break
		}
	}
	if home != "" && strings.HasPrefix(p, home+"/") {
		p = "~" + p[len(home):]
	}
	parts := strings.Split(p, "/")
	if n := len(parts); n > 3 {
		return parts[0] + "/…/" + parts[n-1]
	}
	return p
}

// dropFlags drops flags set to a variable, and long flags set to a quoted string or an id, with their values.
func dropFlags(words []string) []string {
	var out []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		if !strings.HasPrefix(w, "-") || w == "-" || w == "--" {
			out = append(out, w)
			continue
		}
		long := strings.HasPrefix(w, "--")
		if _, v, ok := strings.Cut(w, "="); ok && long {
			if noise(v, long) {
				i = closeQuote(words, i, strings.TrimPrefix(v, "$"))
				continue
			}
		} else if !ok && i+1 < len(words) && noise(words[i+1], long) {
			i = closeQuote(words, i+1, words[i+1])
			continue
		}
		out = append(out, w)
	}
	return out
}

func noise(v string, long bool) bool {
	if strings.HasPrefix(strings.TrimLeft(v, `"'`), "$") {
		return true
	}
	return long && (strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "'") || longID.FindString(v) == v)
}

// closeQuote is the index of the word that ends the value starting at words[i].
func closeQuote(words []string, i int, v string) int {
	if v == "" || (v[0] != '"' && v[0] != '\'') {
		return i
	}
	q := v[:1]
	if len(v) > 1 && strings.HasSuffix(v, q) {
		return i
	}
	for j := i + 1; j < len(words); j++ {
		if strings.HasSuffix(words[j], q) {
			return j
		}
	}
	return len(words) - 1
}
