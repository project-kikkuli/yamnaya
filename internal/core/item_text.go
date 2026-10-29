package core

import (
	"fmt"
	"strings"
	"time"
)

// ItemText is it with its history as plain text, as ym item show prints it.
func (c *Core) ItemText(it Item) string {
	var w strings.Builder
	id := it.Qualified()
	if it.Alias != "" && it.Alias != it.ID {
		id += " (was " + it.Alias + ")"
	}
	fmt.Fprintf(&w, "%s  %s  %s\n%s\n", id, it.Kind, it.State, it.Question)
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&w, "%s: %s\n", k, v)
		}
	}
	line("action", it.Action)
	line("rec", it.Rec)
	for _, o := range it.Options {
		line("option", o.Label+" = "+o.Effect)
	}
	line("assumption", it.Assumption)
	if m := Unprepared(it); len(m) > 0 {
		line("unprepared", "missing "+strings.Join(m, " and ")+c.preparedBy(it))
	}
	line("tried", it.Tried)
	for _, ev := range it.Evidence {
		line("evidence", ev)
	}
	for _, l := range DossierLines(it.Dossier) {
		line(l[0], l[1])
	}
	line("blocks", fmt.Sprint(it.Blocks))
	line("source", it.Harness+" "+it.Session)
	line("answer", it.Answer)
	line("dismissed", it.Reason)
	for _, ev := range it.History[min(1, len(it.History)):] {
		h := fmt.Sprintf("%s %s -> %s", ev.At.Local().Format(time.DateTime), ev.From, ev.To)
		if ev.Text != "" {
			h += ": " + ev.Text
		}
		line("history", h)
	}
	return w.String()
}

func (c *Core) preparedBy(it Item) string {
	if it.Preparer == "" {
		return ""
	}
	if o, err := c.LoadOrg(); err == nil {
		if m, ok := o.Managers[it.Preparer]; ok {
			return "; " + m.Label() + " was told to prepare it"
		}
	}
	return "; " + it.Preparer + " was told to prepare it"
}
