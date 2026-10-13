package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/spf13/cobra"
)

func itemCmd(withApp runner) *cobra.Command {
	cmd := &cobra.Command{Use: "item", Short: "Manage the project's open-item tracker"}
	cmd.AddCommand(itemAddCmd(withApp), itemListCmd(withApp), itemShowCmd(withApp))
	cmd.AddCommand(itemTransitionCmd(withApp, "answer <id> <text>", "Answer an item", 2, func(s *store.Store, p, id, text string) (store.Item, error) { return s.Answer(p, id, text) }))
	cmd.AddCommand(itemTransitionCmd(withApp, "dismiss <id> <reason>", "Dismiss an item with a reason; it is never asked again", 2, func(s *store.Store, p, id, text string) (store.Item, error) { return s.Dismiss(p, id, text) }))
	cmd.AddCommand(itemTransitionCmd(withApp, "snooze <id>", "Snooze an open item", 1, func(s *store.Store, p, id, _ string) (store.Item, error) { return s.Snooze(p, id) }))
	cmd.AddCommand(itemTransitionCmd(withApp, "reopen <id>", "Return an item to open", 1, func(s *store.Store, p, id, _ string) (store.Item, error) { return s.Reopen(p, id) }))
	return cmd
}

func parseOption(s string) store.Option {
	label, effect, _ := strings.Cut(s, "=")
	return store.Option{Label: strings.TrimSpace(label), Effect: strings.TrimSpace(effect)}
}

func itemAddCmd(withApp runner) *cobra.Command {
	var it store.Item
	var options []string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "File an item and print its id",
		Args:  cobra.NoArgs,
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			project, err := a.requireProject()
			if err != nil {
				return err
			}
			it.Project = project
			it.SourceWindow = envWindow()
			for _, o := range options {
				it.Options = append(it.Options, parseOption(o))
			}
			added, err := a.store.AddItem(it)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), added.ID)
			return nil
		}),
	}
	cmd.Flags().StringVar(&it.Kind, "kind", "", "decision, default, or chore")
	cmd.Flags().StringVar(&it.Question, "question", "", "one-line question")
	cmd.Flags().StringVar(&it.Rec, "rec", "", "recommendation")
	cmd.Flags().StringArrayVar(&options, "option", nil, "option as label=effect (repeatable)")
	cmd.Flags().StringVar(&it.Assumption, "assumption", "", "what the filer assumed")
	cmd.MarkFlagRequired("kind")
	cmd.MarkFlagRequired("question")
	return cmd
}

func printItemLine(w io.Writer, it store.Item) {
	line := fmt.Sprintf("%-4s %-8s %-9s %s", it.ID, it.Kind, it.State, it.Question)
	if it.Rec != "" {
		line += "  rec: " + it.Rec
	}
	fmt.Fprintln(w, line)
}

func itemListCmd(withApp runner) *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List open items (--all for every state)",
		Args:  cobra.NoArgs,
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			project, err := a.requireProject()
			if err != nil {
				return err
			}
			if _, err := a.store.Project(project); err != nil {
				return err
			}
			items, err := a.store.Items(project, all)
			if err != nil {
				return err
			}
			if asJSON {
				out := make([]itemJSON, 0, len(items))
				for _, it := range items {
					out = append(out, toItemJSON(it))
				}
				return writeJSON(cmd.OutOrStdout(), out)
			}
			for _, it := range items {
				printItemLine(cmd.OutOrStdout(), it)
			}
			return nil
		}),
	}
	cmd.Flags().BoolVar(&all, "all", false, "include answered, dismissed, and snoozed items")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func printItem(w io.Writer, it store.Item) {
	fmt.Fprintf(w, "%s  %s  %s\n%s\n", it.ID, it.Kind, it.State, it.Question)
	if it.Rec != "" {
		fmt.Fprintf(w, "rec: %s\n", it.Rec)
	}
	for _, o := range it.Options {
		fmt.Fprintf(w, "option: %s = %s\n", o.Label, o.Effect)
	}
	if it.Assumption != "" {
		fmt.Fprintf(w, "assumption: %s\n", it.Assumption)
	}
	if it.SourceWindow != "" {
		fmt.Fprintf(w, "source: %s\n", it.SourceWindow)
	}
	fmt.Fprintf(w, "created: %s\n", ts(it.CreatedAt))
	if it.Answer != "" {
		fmt.Fprintf(w, "answer: %s\n", it.Answer)
	}
	if it.Reason != "" {
		fmt.Fprintf(w, "dismissed: %s\n", it.Reason)
	}
	for _, e := range it.History {
		if e.From == "" {
			continue
		}
		line := fmt.Sprintf("history: %s %s -> %s", ts(e.At), e.From, e.To)
		if e.Text != "" {
			line += ": " + e.Text
		}
		fmt.Fprintln(w, line)
	}
}

func itemShowCmd(withApp runner) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one item",
		Args:  cobra.ExactArgs(1),
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			project, err := a.requireProject()
			if err != nil {
				return err
			}
			it, err := a.store.Item(project, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), toItemJSON(it))
			}
			printItem(cmd.OutOrStdout(), it)
			return nil
		}),
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func itemTransitionCmd(withApp runner, use, short string, minArgs int, apply func(s *store.Store, project, id, text string) (store.Item, error)) *cobra.Command {
	args := cobra.ExactArgs(1)
	if minArgs > 1 {
		args = cobra.MinimumNArgs(minArgs)
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  args,
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			project, err := a.requireProject()
			if err != nil {
				return err
			}
			it, err := apply(a.store, project, args[0], strings.Join(args[1:], " "))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", it.ID, it.State)
			return nil
		}),
	}
}
