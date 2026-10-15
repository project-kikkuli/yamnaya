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

var longID = regexp.MustCompile(`[0-9a-fA-F][0-9a-fA-F-]{11,}`)

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

// shortAction is an action cut to what tells it apart: a command by its name, paths relative to dirs, long ids elided.
func shortAction(a core.Action, dirs ...string) string {
	if a.Detail == "" {
		return a.Tool
	}
	home, _ := os.UserHomeDir()
	words := strings.Fields(a.Detail)
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
