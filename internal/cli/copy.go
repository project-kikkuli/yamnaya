package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/clipboard"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

func copyCmd(with, routed func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var stdout bool
	var revision int
	cmd := &cobra.Command{
		Use:   "copy [--stdout] brief|context|item <id>",
		Short: "Put a goal's or chief's brief, its context revision, or an item on the clipboard as clean text",
		Long: "Put a goal's or chief's brief, its context revision, or an item on the clipboard as clean text: pbcopy on this Mac, " +
			"or the OSC 52 escape to the terminal over SSH, which the owner's terminal turns into its clipboard.",
		Args:      cobra.ExactArgs(2),
		ValidArgs: []string{"brief", "context", "item"},
	}
	run := func(e *env, cmd *cobra.Command, args []string) error {
		text, what, err := copyText(e, args[0], args[1], revision)
		if err != nil {
			return err
		}
		text = clipboard.Clean(text)
		if stdout {
			_, err := fmt.Fprint(cmd.OutOrStdout(), text)
			return err
		}
		way, err := clipboard.Copy(text)
		if err != nil {
			return fmt.Errorf("copy %s through %s: %w", what, way, err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "copied %s, %d lines, through %s\n", what, strings.Count(text, "\n"), way)
		return nil
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if args[0] == "item" {
			return routed(run)(c, args)
		}
		return with(run)(c, args)
	}
	cmd.Flags().BoolVar(&stdout, "stdout", false, "print the clean text instead of copying it")
	cmd.Flags().IntVar(&revision, "revision", 0, "with context, copy this revision instead of the current one")
	return cmd
}

func copyText(e *env, kind, ref string, revision int) (text, what string, err error) {
	switch kind {
	case "brief":
		text, err = e.core.BriefPage(ref)
		return text, ref + "'s brief", err
	case "context":
		m, text, rev, err := e.core.Context(ref)
		if err != nil || revision == 0 || revision == rev {
			return text, fmt.Sprintf("%s's context revision %d", m.ID, rev), err
		}
		steers, err := e.core.Store.Steers(m.ID)
		if err != nil {
			return "", "", err
		}
		for _, st := range steers {
			if st.Revision == revision {
				return st.Text, fmt.Sprintf("%s's context revision %d", m.ID, revision), nil
			}
		}
		return "", "", fmt.Errorf("%s has no context revision %d: %w", m.ID, revision, core.ErrNotFound)
	case "item":
		b, err := e.core.Board()
		if err != nil {
			return "", "", err
		}
		it, err := e.item(b, ref)
		if err != nil {
			return "", "", err
		}
		return e.core.Store.Nicked(e.core.ItemText(it)), "item " + it.Qualified(), nil
	}
	return "", "", fmt.Errorf("copy %s: say brief, context or item", strconv.Quote(kind))
}
