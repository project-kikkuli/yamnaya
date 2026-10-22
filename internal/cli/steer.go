package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// author is who steers from this shell: the manager or chief the calling session runs as, else the owner.
func (e *env) author() (string, error) {
	c, err := e.core.Calling(os.Getpid(), os.Getenv)
	if errors.Is(err, core.ErrNotFound) {
		return core.Owner, nil
	}
	if err != nil {
		return "", err
	}
	o, err := e.core.LoadOrg()
	if err != nil {
		return "", err
	}
	if m, ok := o.ManagerOf(e.core.Resolve(c.Session)); ok {
		return m.ID, nil
	}
	return core.Owner, nil
}

func goalSteerCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var file, inline, by string
	cmd := &cobra.Command{
		Use:   "steer [--file PATH|-] <goal|manager|chief>",
		Short: "Change what a goal or chief is about as its next numbered revision; its manager must adopt it",
		Long: "Opens $EDITOR on the current plan, or reads the new plan from --file or from piped stdin. The new revision goes to the\n" +
			"manager, which records taking it on with ym goal adopt; until then the board shows it unadopted and ym serve nudges it.\n" +
			"A chief may steer the goals and chiefs below it.",
		Args: cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			m, was, rev, err := e.core.Context(args[0])
			if err != nil {
				return err
			}
			var text string
			switch {
			case inline != "":
				text = inline
			case file == "-" || (file == "" && !term.IsTerminal(os.Stdin.Fd())):
				raw, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				text = string(raw)
			case file != "":
				raw, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				text = string(raw)
			default:
				path := filepath.Join(os.TempDir(), fmt.Sprintf("ym-steer-%s-%d.md", m.ID, time.Now().UnixNano()))
				defer os.Remove(path)
				if err := core.EditPlan(path, m, was, rev); err != nil {
					return err
				}
				if err := core.EditorCommand(path).Run(); err != nil {
					return fmt.Errorf("editor: %w", err)
				}
				if text, err = core.ReadPlan(path, was); err != nil {
					return err
				}
				if text == "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s unchanged at revision %d\n", m.ID, rev)
					return nil
				}
			}
			author, err := e.author()
			if err != nil {
				return err
			}
			if _, _, remote := core.SplitRemote(by); remote && author == core.Owner {
				author = by
			}
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			st, err := e.core.SteerContext(b, m.ID, text, author)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s steered to revision %d; it shows unadopted until its manager runs ym goal adopt %s --revision %d\n", m.ID, st.Revision, m.ID, st.Revision)
			return nil
		}),
	}
	cmd.Flags().StringVar(&file, "file", "", "read the new plan from this file, or - for stdin")
	cmd.Flags().StringVar(&inline, "text", "", "the new plan itself")
	cmd.Flags().StringVar(&by, "by", "", "the chief on another host that steers, as host/id")
	cmd.Flags().MarkHidden("text")
	cmd.Flags().MarkHidden("by")
	return cmd
}

func goalAdoptCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var revision int
	cmd := &cobra.Command{
		Use:   "adopt [<goal|manager|chief>] --revision N",
		Short: "(manager or chief) Record that you took on revision N of your own plan, after reworking your course to fit it",
		Args:  cobra.MaximumNArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			c, err := e.caller()
			if err != nil {
				return err
			}
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			st, err := e.core.Adopt(c.Session, ref, revision)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s adopted revision %d\n", st.Manager, st.Revision)
			return nil
		}),
	}
	cmd.Flags().IntVar(&revision, "revision", 0, "the revision you took on")
	cmd.MarkFlagRequired("revision")
	return cmd
}

func goalContextCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:   "context [--all] <goal|manager|chief>",
		Short: "Print what a goal or chief is about now, its revision and whether its manager adopted it; --all prints every revision",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			m, text, rev, err := e.core.Context(args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), core.PlanContext{Manager: m, Text: text, Revision: rev})
			}
			steers, err := e.core.Store.Steers(m.ID)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if len(steers) == 0 {
				fmt.Fprintf(w, "%s revision 0, the ask it started with:\n\n%s\n", m.ID, text)
				return nil
			}
			if !all {
				steers = steers[len(steers)-1:]
			}
			for i, st := range steers {
				if i > 0 {
					fmt.Fprintln(w)
				}
				fmt.Fprintf(w, "%s revision %d by %s at %s, %s:\n\n%s\n", m.ID, st.Revision, st.By(), st.CreatedAt.Local().Format(time.DateTime), adoption(st), st.Text)
			}
			return nil
		}),
	}
	cmd.Flags().BoolVar(&all, "all", false, "print every revision, oldest first")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the current plan as JSON")
	return cmd
}

func adoption(st core.Steer) string {
	if st.Adopted() {
		return "adopted " + st.AdoptedAt.Local().Format(time.DateTime)
	}
	return "not adopted"
}

func goalBriefCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var b core.Brief
	cmd := &cobra.Command{
		Use: "brief --assessment TEXT --changed TEXT --uncertainty TEXT --next TEXT --waiting-on TEXT",
		Short: "(manager or chief) Record your next brief version: where things stand, what changed, what you are unsure of, what is next, " +
			"and what you wait on",
		Args: cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			c, err := e.caller()
			if err != nil {
				return err
			}
			b, err := e.core.WriteBrief(c.Session, b)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s brief v%d recorded; %s\n", b.Manager, b.Version, core.BriefURL(b.Manager))
			return nil
		}),
	}
	for _, f := range []struct {
		p    *string
		name string
		help string
	}{
		{&b.Assessment, "assessment", "where the goal stands, in a line or two"},
		{&b.Changed, "changed", "what changed since your last brief"},
		{&b.Uncertainty, "uncertainty", "what you are unsure of, or none"},
		{&b.Next, "next", "what happens next"},
		{&b.WaitingOn, "waiting-on", "what you wait on (agents, CI, reviews, a person), or none"},
	} {
		cmd.Flags().StringVar(f.p, f.name, "", f.help)
		cmd.MarkFlagRequired(f.name)
	}
	return cmd
}

func briefLines(w io.Writer, f core.Freshness) {
	if f.Brief == nil {
		fmt.Fprintln(w, "\nno brief recorded yet")
		return
	}
	state := "fresh"
	if f.Stale() {
		state = "stale: predates " + f.BehindWhat + " at " + f.Behind.Local().Format(time.DateTime)
	}
	fmt.Fprintf(w, "\nbrief v%d, %s, %s\n", f.Brief.Version, f.Brief.CreatedAt.Local().Format(time.DateTime), state)
	for _, s := range f.Brief.Sections() {
		fmt.Fprintf(w, "\n%s\n%s\n", strings.ToUpper(s[0]), s[1])
	}
}
