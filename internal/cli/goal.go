package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func goalCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "goal [--repo NAME|PATH] <ask>",
		Short: "Hand a goal to a new manager that owns it until it is done; its agents report to it, not to you",
		Args:  goalArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			dir, err := e.repoArg(repo)
			if err != nil {
				return err
			}
			g, m, err := e.core.NewGoal(args[0], dir, "")
			if err != nil {
				return err
			}
			if m.LastError != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s recorded, but its manager %s did not start: %s; ym serve retries with backoff\n", g.ID, m.ID, m.LastError)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: manager %s runs it as %s\nbrief: %s\nopen: o on the board, or ym open %s\n", g.ID, m.ID, core.Short(m.Session), g.BriefPath, m.Session)
			return nil
		}),
	}
	cmd.Flags().StringVar(&repo, "repo", "", "repository the goal is in, by path or by any part of its name (default: the current directory's)")
	cmd.AddCommand(goalListCmd(with), goalShowCmd(with), goalDoneCmd(with), goalVerdictCmd(with), goalArtifactCmd(with), goalSendCmd(with),
		goalRelaunchCmd(with), goalDropCmd(with), goalAuthorityCmd(with), goalUpdateCmd(with), goalWakeCmd(with), goalFeedCmd(with))
	return cmd
}

// goalArgs takes the ask as one quoted argument, so a subcommand this ym does not have is refused rather than started as a goal.
func goalArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 1 {
		return nil
	}
	if len(args) == 0 {
		return errors.New(`a goal needs the ask in your words: ym goal "<ask>"`)
	}
	var subs []string
	for _, c := range cmd.Commands() {
		subs = append(subs, c.Name())
	}
	return fmt.Errorf(`ym goal has no subcommand %q (it has %s); to start a goal, quote the whole ask: ym goal "<ask>"`, args[0], strings.Join(subs, ", "))
}

func (e *env) repoArg(repo string) (string, error) {
	if repo == "" {
		return os.Getwd()
	}
	return e.core.ResolveRepo(repo)
}

type goalRow struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	Name     string `json:"name"`
	Parent   string `json:"parent,omitempty"`
	Goal     string `json:"goal,omitempty"`
	Ask      string `json:"ask,omitempty"`
	State    string `json:"state"`
	Session  string `json:"session"`
	Brief    string `json:"brief"`
	Artifact string `json:"artifact,omitempty"`
	Agents   int    `json:"agents"`
	Depth    int    `json:"-"`
}

func treeRows(b core.Board, under string) []goalRow {
	var out []goalRow
	var walk func(core.Node, int)
	walk = func(n core.Node, depth int) {
		m := n.Manager
		r := goalRow{ID: m.ID, Role: m.Role, Name: m.Name, Parent: m.ParentID, State: n.Standing, Session: m.Session, Brief: m.BriefPath, Artifact: m.ArtifactURL, Agents: len(n.Agents), Depth: depth}
		if n.Goal != nil {
			r.Goal, r.Ask, r.Brief, r.Artifact = n.Goal.ID, n.Goal.Ask, n.Goal.BriefPath, n.Goal.ArtifactURL
		}
		out = append(out, r)
		for _, c := range n.Children {
			walk(c, depth+1)
		}
	}
	if under != "" {
		if n, ok := b.Node(strings.ToLower(under)); ok {
			for _, c := range n.Children {
				walk(c, 0)
			}
		}
		return out
	}
	for _, n := range b.Tree {
		walk(n, 0)
	}
	return out
}

func goalListCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var under string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list [--under CHIEF] [--json]",
		Short: "List goals, managers and chiefs as a tree, with their state and brief",
		Args:  cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			rows := treeRows(b, under)
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no goals; start one with ym goal \"<ask>\"")
			}
			for _, r := range rows {
				printGoalRow(cmd.OutOrStdout(), r)
			}
			return nil
		}),
	}
	cmd.Flags().StringVar(&under, "under", "", "only the reports below this chief")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func printGoalRow(w io.Writer, r goalRow) {
	what := r.Ask
	if r.Role == core.RoleChief {
		what = "chief " + r.Name
	}
	id := r.ID
	if r.Goal != "" {
		id = r.Goal + "/" + r.ID
	}
	fmt.Fprintf(w, "%s%-14s %-7s %d agents  %s\n%s  brief %s\n", strings.Repeat("  ", r.Depth), id, r.State, r.Agents, oneLine(what), strings.Repeat("  ", r.Depth), r.Brief)
}

func goalShowCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "show <goal|manager|chief>",
		Short: "Show a goal or chief and print its brief",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			for _, r := range treeRows(b, "") {
				if strings.EqualFold(r.ID, args[0]) || strings.EqualFold(r.Goal, args[0]) {
					r.Depth = 0
					printGoalRow(cmd.OutOrStdout(), r)
					if r.Artifact != "" {
						fmt.Fprintln(cmd.OutOrStdout(), "  artifact "+r.Artifact)
					}
					if r.Goal != "" {
						if err := printVerifications(cmd.OutOrStdout(), e.core, r.Goal); err != nil {
							return err
						}
					}
					if brief, err := os.ReadFile(r.Brief); err == nil {
						fmt.Fprintf(cmd.OutOrStdout(), "\n%s", brief)
					}
					return nil
				}
			}
			return fmt.Errorf("no goal, manager or chief %q on the board: %w", args[0], core.ErrNotFound)
		}),
	}
}

func printVerifications(w io.Writer, c *core.Core, goal string) error {
	all, err := c.Store.Verifications(goal)
	if err != nil {
		return err
	}
	for _, v := range all {
		verdict := v.Verdict
		if verdict == "" {
			verdict = "under way"
			if v.LaunchedAt.IsZero() {
				verdict = "checking facts"
			} else if v.Session != "" {
				verdict = "verifier " + core.Short(v.Session) + " checking"
			}
		}
		fmt.Fprintf(w, "  verification %d: %s\n", v.Attempt, verdict)
		for _, f := range v.Facts {
			fmt.Fprintln(w, "    fact "+f.String())
		}
		for _, f := range v.Findings {
			fmt.Fprintln(w, "    finding "+f)
		}
		if v.Verdict == "" && v.LastError != "" {
			fmt.Fprintln(w, "    last error "+v.LastError)
		}
	}
	return nil
}

func goalDoneCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var summary string
	var evidence []string
	cmd := &cobra.Command{
		Use:   "done --summary <line> [--evidence <line>]...",
		Short: "Hand the calling manager's goal to verification; the owner sees it done once a fresh verifier passes it",
		Args:  cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			c, err := e.caller()
			if err != nil {
				return err
			}
			g, v, err := e.core.FinishGoal(c.Session, summary, evidence)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s verifying (attempt %d): ym checks its pull requests, then a fresh verifier session checks the work; its findings come back here\n", g.ID, v.Attempt)
			return nil
		}),
	}
	cmd.Flags().StringVar(&summary, "summary", "", "one line on what was done")
	cmd.Flags().StringArrayVar(&evidence, "evidence", nil, "a line of proof the verifier should check, such as a pull request URL or a commit on main; repeatable")
	cmd.MarkFlagRequired("summary")
	return cmd
}

func goalVerdictCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var pass, fail bool
	var findings []string
	cmd := &cobra.Command{
		Use:   "verdict <goal> --pass|--fail [--finding <line>]...",
		Short: "Record a verifier session's verdict on the goal it was launched to check",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if pass == fail {
				return errors.New("pass exactly one of --pass or --fail")
			}
			if e.session != "" {
				return errors.New("a verdict comes only from the verifier session itself; run ym goal verdict inside it, without --session")
			}
			c, err := e.core.Calling(os.Getpid(), os.Getenv)
			if err != nil {
				return err
			}
			verdict := core.VerdictPass
			if fail {
				verdict = core.VerdictFail
			}
			v, err := e.core.RecordVerdict(c.Session, args[0], verdict, findings)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s attempt %d: %s recorded; ym ends this session now\n", v.Goal, v.Attempt, verdict)
			return nil
		}),
	}
	cmd.Flags().BoolVar(&pass, "pass", false, "the goal is done as the owner asked")
	cmd.Flags().BoolVar(&fail, "fail", false, "the goal is not done; name each gap with --finding")
	cmd.Flags().StringArrayVar(&findings, "finding", nil, "one concrete, checkable line; repeatable")
	return cmd
}

func goalArtifactCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "artifact <url>",
		Short: "Register the richer brief the calling manager or chief publishes; V on the board opens it",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			c, err := e.caller()
			if err != nil {
				return err
			}
			return e.core.SetArtifact(c.Session, args[0])
		}),
	}
}

func goalSendCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var subtree bool
	cmd := &cobra.Command{
		Use:   "send [--subtree] <id>[,<id>...] <message>",
		Short: "Send managers or chiefs a message; --subtree also reaches everyone below a chief",
		Args:  cobra.MinimumNArgs(2),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			_, callErr := e.core.Calling(os.Getpid(), os.Getenv)
			owner := errors.Is(callErr, core.ErrNotFound)
			n, err := e.core.Steer(b, strings.Split(args[0], ","), subtree, owner, strings.Join(args[1:], " "))
			fmt.Fprintf(cmd.OutOrStdout(), "sent to %d\n", n)
			return err
		}),
	}
	cmd.Flags().BoolVar(&subtree, "subtree", false, "also send to every manager and chief below each chief named")
	return cmd
}

func goalRelaunchCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "relaunch <goal|manager|chief>",
		Short: "End a manager's or chief's session and start a fresh one from its prompt and brief",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			m, err := e.core.ReplaceManager(b, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s relaunched as %s\n", m.ID, core.Short(m.Session))
			return nil
		}),
	}
}

func goalDropCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "drop <goal|manager|chief>",
		Short: "Drop a goal you no longer want, or retire a chief; ym stops supervising it",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			g, err := e.core.DropGoal(args[0])
			if err != nil {
				return err
			}
			if g.ID == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "retired")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), g.ID+" dropped")
			}
			return nil
		}),
	}
}

func goalAuthorityCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var merge, noMerge bool
	cmd := &cobra.Command{
		Use:   "authority [<repo> --merge|--no-merge]",
		Short: "Show, or set, the repositories whose managers may merge their own pull requests",
		Args:  cobra.MaximumNArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if merge == noMerge {
					return errors.New("pass --merge or --no-merge")
				}
				if err := e.core.Store.SetAuthority(args[0], merge); err != nil {
					return err
				}
			}
			all, err := e.core.Store.Authority()
			if err != nil {
				return err
			}
			repos := make([]string, 0, len(all))
			for r := range all {
				repos = append(repos, r)
			}
			sort.Strings(repos)
			for _, r := range repos {
				can := "may not merge"
				if all[r] {
					can = "may merge"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-20s managers %s\n", r, can)
			}
			return nil
		}),
	}
	cmd.Flags().BoolVar(&merge, "merge", false, "let managers merge their own pull requests in the repository")
	cmd.Flags().BoolVar(&noMerge, "no-merge", false, "send the repository's merge-ready pull requests to the owner")
	return cmd
}

func chiefCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var repo, remit, over string
	cmd := &cobra.Command{
		Use:   "chief [--repo R] [--remit TEXT] [--over ID,ID] <name>",
		Short: "Start a chief that oversees managers and other chiefs, reads their briefs and steers them",
		Args:  cobra.MinimumNArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			dir, err := e.repoArg(repo)
			if err != nil {
				return err
			}
			var children []string
			if over != "" {
				children = strings.Split(over, ",")
			}
			m, err := e.core.NewChief(strings.Join(args, " "), remit, dir, children)
			if err != nil {
				return err
			}
			if m.LastError != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "chief %s recorded, but it did not start: %s; ym serve retries with backoff\n", m.ID, m.LastError)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "chief %s runs as %s\nbrief: %s\n", m.ID, core.Short(m.Session), m.BriefPath)
			return nil
		}),
	}
	cmd.Flags().StringVar(&repo, "repo", "", "repository the chief's session runs in (default: the current directory's)")
	cmd.Flags().StringVar(&remit, "remit", "", "what the chief oversees, in your words")
	cmd.Flags().StringVar(&over, "over", "", "managers, goals or chiefs it starts over, comma separated")
	cmd.AddCommand(&cobra.Command{
		Use:   "attach <child> <chief>",
		Short: "Put a manager or chief under a chief",
		Args:  cobra.ExactArgs(2),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			return e.core.Attach(args[0], args[1])
		}),
	}, &cobra.Command{
		Use:   "detach <child>",
		Short: "Make a manager or chief report to you again",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			return e.core.Attach(args[0], "")
		}),
	})
	return cmd
}

func goalUpdateCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "update <line>",
		Short: "Tell the owner and your chiefs, in one line, where the calling manager's goal stands",
		Args:  cobra.MinimumNArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			c, err := e.caller()
			if err != nil {
				return err
			}
			ev, err := e.core.UpdateGoal(c.Session, strings.Join(args, " "))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s updated\n", ev.Goal)
			return nil
		}),
	}
}

func goalWakeCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "wake <when> <why>",
		Short: "Have ym wake the calling manager or chief at a time: 30m, 2h, 15:04 or RFC 3339",
		Args:  cobra.MinimumNArgs(2),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			c, err := e.caller()
			if err != nil {
				return err
			}
			due, err := core.ParseWhen(args[0], time.Now())
			if err != nil {
				return err
			}
			t, err := e.core.SetWake(c.Session, due, strings.Join(args[1:], " "))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ym wakes %s at %s\n", t.Manager, t.Due.Local().Format(time.DateTime))
			return nil
		}),
	}
}

func goalFeedCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var keep bool
	cmd := &cobra.Command{
		Use:   "feed <goal|manager>",
		Short: "Print everything that happened on a goal, oldest first, and mark it read",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			g, err := e.core.GoalByRef(args[0])
			if err != nil {
				return err
			}
			events, err := e.core.Store.GoalEvents(g.ID)
			if err != nil {
				return err
			}
			if len(events) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), g.ID+": nothing yet")
			}
			for _, ev := range events {
				mark := " "
				if ev.OwnerNews() && ev.Seen.IsZero() {
					mark = "*"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s  %-14s %s\n", mark, ev.Created.Local().Format(time.DateTime), ev.Kind, ev.Payload)
			}
			if keep {
				return nil
			}
			return e.core.Store.MarkSeen(g.ID, time.Now())
		}),
	}
	cmd.Flags().BoolVar(&keep, "keep-unread", false, "leave the news unread")
	return cmd
}
