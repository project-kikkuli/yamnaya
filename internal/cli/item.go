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
			it, d, err := e.core.Answer(b, args[0], strings.Join(args[1:], " "))
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
			it, d, err := e.core.Dismiss(b, args[0], strings.Join(args[1:], " "))
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
			it, err := e.core.Snooze(args[0])
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
			it, err := e.core.Reopen(args[0])
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
			var f core.ItemFilter
			if !all {
				f.States = []string{core.ItemOpen}
			}
			items, err := e.core.Store.Items(f)
			if err != nil {
				return err
			}
			if !everywhere {
				key, err := e.projectKey(b)
				if err != nil {
					return err
				}
				var kept []core.Item
				for _, it := range items {
					if b.ProjectOf(it.Session) == key {
						kept = append(kept, it)
					}
				}
				items = kept
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
		Short: "Print answers and dismissals for the calling session that it has not received yet, and mark them received",
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
			if asJSON {
				return printItems(cmd.OutOrStdout(), items, true)
			}
			for _, it := range items {
				if it.State == core.ItemAnswered {
					fmt.Fprintf(cmd.OutOrStdout(), "re %s: %s\n", it.ID, it.Answer)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "re %s: dismissed: %s\n", it.ID, it.Reason)
				}
			}
			return nil
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
			it, err := e.core.Store.Item(args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), toJSON(it))
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%s  %s  %s\n%s\n", it.ID, it.Kind, it.State, it.Question)
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
	return itemJSON{ID: it.ID, Kind: it.Kind, State: it.State, Question: it.Question, Rec: it.Rec, Options: it.Options,
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
	for _, it := range items {
		line := fmt.Sprintf("%-5s %-8s %-9s %s", it.ID, it.Kind, it.State, it.Question)
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
