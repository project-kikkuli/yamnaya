package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func itemCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	cmd := &cobra.Command{Use: "item", Short: "File and manage open items"}
	cmd.AddCommand(itemAddCmd(with), itemListCmd(with), itemShowCmd(with), itemInboxCmd(with))
	cmd.AddCommand(&cobra.Command{
		Use:   "answer <id> <text>",
		Short: "Answer an item; an idle agent gets it typed in, a busy one reads it from ym item inbox",
		Args:  cobra.MinimumNArgs(2),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			found, err := e.item(b, args[0])
			if err != nil {
				return err
			}
			it, d, err := e.core.Answer(b, found.Key(), strings.Join(args[1:], " "))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s answered, %s\n", it.ID, d)
			return nil
		}),
	}, &cobra.Command{
		Use:   "dismiss <id> <reason>",
		Short: "Dismiss an item for good",
		Args:  cobra.MinimumNArgs(2),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			found, err := e.item(b, args[0])
			if err != nil {
				return err
			}
			it, d, err := e.core.Dismiss(b, found.Key(), strings.Join(args[1:], " "))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s dismissed, %s\n", it.ID, d)
			return nil
		}),
	}, &cobra.Command{
		Use:   "snooze <id>",
		Short: "Snooze an open item",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			found, err := e.item(b, args[0])
			if err != nil {
				return err
			}
			it, err := e.core.Snooze(found.Key())
			if err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", it.ID, it.State)
			}
			return err
		}),
	}, &cobra.Command{
		Use:   "reopen <id>",
		Short: "Return an item to open",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			found, err := e.item(b, args[0])
			if err != nil {
				return err
			}
			it, err := e.core.Reopen(found.Key())
			if err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", it.ID, it.State)
			}
			return err
		}),
	})
	return cmd
}

func itemAddCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var it core.Item
	var options []string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "File an item against the calling session and print its id",
		Args:  cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			c, err := e.caller()
			if err != nil {
				return err
			}
			it.Session, it.Harness = c.Session, c.Harness
			for _, o := range options {
				label, effect, _ := strings.Cut(o, "=")
				it.Options = append(it.Options, core.Option{Label: strings.TrimSpace(label), Effect: strings.TrimSpace(effect)})
			}
			added, err := e.core.AddItem(it)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), added.ID)
			return nil
		}),
	}
	cmd.Flags().StringVar(&it.Kind, "kind", "", "decision, default, or chore")
	cmd.Flags().StringVar(&it.Question, "question", "", "one-line question")
	cmd.Flags().StringVar(&it.Rec, "rec", "", "what you recommend")
	cmd.Flags().StringArrayVar(&options, "option", nil, "option as label=effect (repeatable)")
	cmd.Flags().StringVar(&it.Assumption, "assumption", "", "what you are assuming meanwhile")
	cmd.Flags().IntVar(&it.Blocks, "blocks", 0, "how many pieces of work wait on this")
	cmd.MarkFlagRequired("kind")
	cmd.MarkFlagRequired("question")
	return cmd
}

func itemListCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var all, asJSON, everywhere bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List open items of the calling session's project (--all for every state, --everywhere for every project)",
		Args:  cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			key := ""
			if !everywhere {
				if key, err = e.projectKey(b); err != nil {
					return err
				}
			}
			items, err := e.core.ItemsIn(b, key, all)
			if err != nil {
				return err
			}
			return printItems(cmd.OutOrStdout(), items, asJSON)
		}),
	}
	cmd.Flags().BoolVar(&all, "all", false, "include answered, dismissed and snoozed items")
	cmd.Flags().BoolVar(&everywhere, "everywhere", false, "items of every project")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func itemInboxCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Print answers, dismissals and the owner's messages for the calling session that it has not received yet, and mark them received; while its mission's vibe is missing or stale, a vibe: line asks for one",
		Args:  cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			c, err := e.caller()
			if err != nil {
				return err
			}
			items, err := e.core.Inbox(c.Session)
			if err != nil {
				return err
			}
			nudge, err := e.core.VibeNudge(c)
			if err != nil {
				return err
			}
			if asJSON {
				out := struct {
					Items []itemJSON `json:"items"`
					Vibe  string     `json:"vibe,omitempty"`
				}{Items: []itemJSON{}, Vibe: nudge}
				for _, it := range items {
					out.Items = append(out.Items, toJSON(it))
				}
				return writeJSON(cmd.OutOrStdout(), out)
			}
			for _, it := range items {
				if it.State == core.ItemAnswered {
					fmt.Fprintf(cmd.OutOrStdout(), "re %s: %s\n", it.Spoken(), it.Answer)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "re %s: dismissed: %s\n", it.Spoken(), it.Reason)
				}
			}
			msgs, err := e.core.Store.TakeMessages(c.Session)
			for _, m := range msgs {
				fmt.Fprintf(cmd.OutOrStdout(), "msg: %s\n", m.Text)
			}
			if nudge != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "vibe: %s\n", nudge)
			}
			return err
		}),
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func itemShowCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one item with its history",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			it, err := e.item(b, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), toJSON(it))
			}
			w := cmd.OutOrStdout()
			id := it.Qualified()
			if it.Alias != "" && it.Alias != it.ID {
				id += " (was " + it.Alias + ")"
			}
			fmt.Fprintf(w, "%s  %s  %s\n%s\n", id, it.Kind, it.State, it.Question)
			line := func(k, v string) {
				if v != "" {
					fmt.Fprintf(w, "%s: %s\n", k, v)
				}
			}
			line("rec", it.Rec)
			for _, o := range it.Options {
				line("option", o.Label+" = "+o.Effect)
			}
			line("assumption", it.Assumption)
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
			return nil
		}),
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

type itemJSON struct {
	ID         string        `json:"id"`
	Project    string        `json:"project"`
	Scope      string        `json:"scope"`
	Alias      string        `json:"alias,omitempty"`
	Kind       string        `json:"kind"`
	State      string        `json:"state"`
	Question   string        `json:"question"`
	Rec        string        `json:"rec"`
	Options    []core.Option `json:"options"`
	Assumption string        `json:"assumption"`
	Blocks     int           `json:"blocks"`
	Session    string        `json:"session"`
	Harness    string        `json:"harness"`
	Answer     string        `json:"answer,omitempty"`
	Reason     string        `json:"reason,omitempty"`
	CreatedAt  string        `json:"created_at"`
	AnsweredAt string        `json:"answered_at,omitempty"`
	Received   bool          `json:"received"`
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func toJSON(it core.Item) itemJSON {
	return itemJSON{ID: it.ID, Project: core.ScopeName(it.Scope), Scope: it.Scope, Alias: it.Alias, Kind: it.Kind, State: it.State, Question: it.Question, Rec: it.Rec, Options: it.Options,
		Assumption: it.Assumption, Blocks: it.Blocks, Session: it.Session, Harness: it.Harness, Answer: it.Answer,
		Reason: it.Reason, CreatedAt: ts(it.CreatedAt), AnsweredAt: ts(it.AnsweredAt), Received: !it.Delivered.IsZero()}
}

func printItems(w io.Writer, items []core.Item, asJSON bool) error {
	if asJSON {
		out := make([]itemJSON, 0, len(items))
		for _, it := range items {
			out = append(out, toJSON(it))
		}
		return writeJSON(w, out)
	}
	scopes := map[string]bool{}
	for _, it := range items {
		scopes[it.Scope] = true
	}
	for _, it := range items {
		id := it.ID
		if len(scopes) > 1 {
			id = it.Qualified()
		}
		line := fmt.Sprintf("%-5s %-8s %-9s %s", id, it.Kind, it.State, it.Question)
		if it.Rec != "" {
			line += "  rec: " + it.Rec
		}
		if it.Answer != "" {
			line += "  answer: " + it.Answer
		}
		fmt.Fprintln(w, line)
	}
	return nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// item resolves an item ID within the scope of -p, else of the calling session; from outside any session, within no scope.
func (e *env) item(b core.Board, ref string) (core.Item, error) {
	return e.core.Store.Find(e.itemScope(b), ref)
}

func (e *env) itemScope(b core.Board) string {
	if e.project != "" {
		for _, r := range b.Repos {
			if r.Key == e.project || strings.EqualFold(r.Name, e.project) {
				return r.Key
			}
		}
		if key, err := b.ProjectKey(e.project); err == nil {
			if p, ok := b.Project(key); ok && p.Repo != "" {
				return p.Repo
			}
			return e.core.ScopeOf(b, key)
		}
		return e.project
	}
	if c, err := e.caller(); err == nil {
		return e.core.ScopeOf(b, c.Session)
	}
	return ""
}
