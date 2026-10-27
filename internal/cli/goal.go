package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/codex"
)

func goalCmd(with, hosts func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var repo, selected string
	var useCodex bool
	cmd := &cobra.Command{
		Use:   "goal [--codex] [--repo NAME|PATH] <ask>",
		Short: "Hand a goal to a new manager that owns it until it is done; its agents report to it, not to you",
		Args:  goalArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			dir, err := e.repoArg(repo)
			if err != nil {
				return err
			}
			g, m, err := e.core.NewGoal(args[0], dir, harnessFlag(useCodex), selected)
			if err != nil {
				return err
			}
			if err := ensureWorkService(); err != nil {
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
	cmd.Flags().StringVar(&selected, "profile", "", "login profile for the manager and verifiers")
	cmd.Flags().BoolVar(&useCodex, "codex", false, "run the manager, and the goal's verifiers, on codex instead of claude")
	cmd.AddCommand(goalListCmd(hosts), goalShowCmd(hosts), goalDoneCmd(with), goalVerdictCmd(with), goalArtifactCmd(with), goalSendCmd(hosts),
		goalRelaunchCmd(hosts), goalAckCmd(hosts), goalAuthorityCmd(with), goalContextCapCmd(with), goalUpdateCmd(with), goalWakeCmd(with), goalFeedCmd(hosts), goalEventCmd(hosts),
		goalSteerCmd(hosts), goalAdoptCmd(with), goalContextCmd(hosts), goalBriefCmd(with))
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

// harnessFlag is the harness --codex picks, empty for the default.
func harnessFlag(useCodex bool) string {
	if useCodex {
		return codex.Name
	}
	return ""
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
	// Finished counts the subagents that ended.
	Finished int         `json:"finished"`
	Workers  []workerRow `json:"workers,omitempty"`
	BriefURL string      `json:"brief_url"`
	// BriefVersion is 0 when no brief was recorded.
	BriefVersion int    `json:"brief_version"`
	BriefStale   bool   `json:"brief_stale"`
	Revision     int    `json:"revision"`
	Adopted      bool   `json:"adopted"`
	Host         string `json:"host,omitempty"`
	// HostState says why another host's row may be out of date: it is unreachable, or not read yet.
	HostState string `json:"host_state,omitempty"`
	Depth     int    `json:"-"`
	fresh     core.Freshness
}

type workerRow struct {
	Session  string    `json:"session"`
	Subagent string    `json:"subagent,omitempty"`
	Name     string    `json:"name"`
	State    string    `json:"state"`
	Activity string    `json:"activity,omitempty"`
	At       time.Time `json:"at"`
	Branch   string    `json:"branch,omitempty"`
	Ahead    int       `json:"ahead,omitempty"`
	PRs      []string  `json:"prs,omitempty"`
	// Slot is the shared slot the worker holds or waits for and the command it runs there.
	Slot string `json:"slot,omitempty"`
}

func treeRows(b core.Board, under string) []goalRow {
	away := map[string]string{}
	for _, r := range b.Remotes {
		if r.Err != nil || r.Stale {
			away[r.Host] = r.State()
		}
	}
	var out []goalRow
	var walk func(core.Node, int)
	walk = func(n core.Node, depth int) {
		m := n.Manager
		r := goalRow{ID: m.ID, Role: m.Role, Name: m.Name, Parent: m.ParentID, State: n.Standing, Session: m.Session, Brief: m.BriefPath, Artifact: m.ArtifactURL, Depth: depth,
			BriefURL: core.BriefURL(m.ID), BriefStale: n.Fresh.Stale(), Adopted: true, fresh: n.Fresh, Host: m.Host, HostState: away[m.Host]}
		if m.Host != "" {
			r.BriefURL = ""
		}
		for _, w := range n.Workers {
			if !w.Active() {
				r.Finished++
				continue
			}
			r.Agents++
			wr := workerRow{Session: w.Session, Name: w.Name, State: w.State, Activity: w.Activity, At: w.At, Branch: w.Branch, Ahead: w.Ahead}
			if w.Member != nil {
				wr.Subagent = w.Member.ID
			} else if u, waiting, ok := b.Machine.Use(w.Session); ok {
				wr.Slot = fmt.Sprintf("slot %d: %s, %s", u.Slot, clip(u.Claim.Line(), 60), core.Span(b.At.Sub(u.Since)))
				if waiting {
					wr.Slot = fmt.Sprintf("waits for a slot: %s, %s", clip(u.Claim.Line(), 60), core.Span(b.At.Sub(u.Since)))
				}
			}
			for _, pr := range w.PRs {
				wr.PRs = append(wr.PRs, prLabel(pr))
			}
			r.Workers = append(r.Workers, wr)
		}
		if n.Fresh.Brief != nil {
			r.BriefVersion = n.Fresh.Brief.Version
		}
		if n.Steer != nil {
			r.Revision, r.Adopted = n.Steer.Revision, n.Steer.Adopted()
		}
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
			printMachine(cmd.OutOrStdout(), b.Machine, b.At)
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
		id = r.Goal + " " + r.ID
	}
	indent := strings.Repeat("  ", r.Depth)
	status := []string{r.fresh.String()}
	if r.Host != "" {
		status = []string{"on " + r.Host, r.fresh.String()}
	}
	if r.HostState != "" {
		status = []string{"on " + r.Host + " (" + r.HostState + ")", r.fresh.String()}
	}
	if r.Revision > 0 && !r.Adopted {
		status = append(status, fmt.Sprintf("steer r%d not adopted", r.Revision))
	}
	count := fmt.Sprintf("%d agents", r.Agents)
	if r.Agents == 1 {
		count = "1 agent"
	}
	if r.Finished > 0 {
		count += fmt.Sprintf(", %d done", r.Finished)
	}
	fmt.Fprintf(w, "%s%-14s %-7s %s  %s\n%s  %s  %s\n", indent, id, r.State, count, oneLine(what), indent, strings.Join(status, ", "), r.BriefURL)
	for _, a := range r.Workers {
		name := a.Name
		if a.Subagent != "" {
			name = "sub " + name
		}
		line := fmt.Sprintf("%s    %-7s %s  %s", indent, a.State, clip(oneLine(name), 40), clip(a.Activity, 90))
		if !a.At.IsZero() {
			line += " (" + ago(a.At) + ")"
		}
		var marks []string
		if a.Ahead > 0 {
			marks = append(marks, fmt.Sprintf("%s +%d", a.Branch, a.Ahead))
		}
		marks = append(marks, a.PRs...)
		if a.Slot != "" {
			marks = append(marks, "["+a.Slot+"]")
		}
		if len(marks) > 0 {
			line += "  " + strings.Join(marks, " ")
		}
		fmt.Fprintln(w, line)
	}
}

// printMachine prints the slot summary, the slots held and waited for, and any overload, above the goal tree.
func printMachine(w io.Writer, m core.Machine, now time.Time) {
	if m.Slots.Capacity == 0 {
		return
	}
	fmt.Fprintln(w, m.Summary())
	for _, u := range m.Holders {
		fmt.Fprintf(w, "  slot %d  %s\n", u.Slot, u.Line(now))
	}
	for _, u := range m.Waiters {
		fmt.Fprintf(w, "  waiting  %s\n", u.Line(now))
	}
	if o := m.Overload(); o != "" {
		fmt.Fprintln(w, o)
	}
	fmt.Fprintln(w)
}

func prLabel(pr core.PR) string {
	label := fmt.Sprintf("#%d", pr.Number)
	if pr.Number == 0 {
		label = pr.URL
	}
	if pr.Status != nil && pr.Status.State != "" {
		label += " " + strings.ToLower(pr.Status.State)
	}
	return label
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func ago(t time.Time) string {
	d := time.Since(t).Round(time.Minute)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
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
			if host, rest, ok := core.SplitRemote(args[0]); ok && e.core.Remotes != nil {
				out, err := e.core.RunRemote(host, "goal", "show", rest)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), out)
				return nil
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
					if r.Revision > 0 {
						fmt.Fprintf(cmd.OutOrStdout(), "  plan revision %d, %s; ym goal context %s prints it\n", r.Revision, map[bool]string{true: "adopted", false: "not adopted"}[r.Adopted], r.ID)
					}
					if r.fresh.Brief == nil {
						if brief, err := os.ReadFile(r.Brief); err == nil {
							fmt.Fprintf(cmd.OutOrStdout(), "\n%s", brief)
						}
					}
					briefLines(cmd.OutOrStdout(), r.fresh)
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
	var subtree, relayed bool
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
			owner := errors.Is(callErr, core.ErrNotFound) && !relayed
			n, err := e.core.Steer(b, strings.Split(args[0], ","), subtree, owner, strings.Join(args[1:], " "))
			fmt.Fprintf(cmd.OutOrStdout(), "sent to %d\n", n)
			return err
		}),
	}
	cmd.Flags().BoolVar(&subtree, "subtree", false, "also send to every manager and chief below each chief named")
	cmd.Flags().BoolVar(&relayed, "relayed", false, "the message comes from a chief on another host, not the owner")
	cmd.Flags().MarkHidden("relayed")
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
			if m.Host != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s relaunched on %s\n", m.ID, m.Host)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s relaunched as %s\n", m.ID, core.Short(m.Session))
			return nil
		}),
	}
}

func goalAckCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "ack <goal>",
		Short: "Clear a stuck goal from your queue, or a done goal from the goal tree, as x does on the board",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if err := e.core.AckGoal(args[0]); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), args[0]+" cleared")
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

func goalContextCapCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "context-cap [<tokens>|off]",
		Short: "Show, or set, the context size past which ym restarts an idle manager or chief fresh from its brief",
		Args:  cobra.MaximumNArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				n, err := readTokens(args[0])
				if err != nil {
					return err
				}
				if err := e.core.Store.SetContextCap(n); err != nil {
					return err
				}
			}
			n, err := e.core.Store.ContextCap()
			if err != nil {
				return err
			}
			if n == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "off: ym never restarts managers and chiefs for their context")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s tokens: ym restarts an idle manager or chief past it fresh from its brief when a wake is due\n", core.Kilo(n))
			return nil
		}),
	}
}

// readTokens reads a token count such as 100000, 100k or 1.5M; off is zero.
func readTokens(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "off" || s == "0" {
		return 0, nil
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "k"):
		mult, s = 1e3, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult, s = 1e6, strings.TrimSuffix(s, "m")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("cannot read %q as a token count: use 100000, 100k, 1.5M or off", s)
	}
	return int64(f * mult), nil
}

func chiefCmd(with, hosts func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var repo, remit, over, selected string
	var useCodex bool
	cmd := &cobra.Command{
		Use:   "chief [--codex] [--repo R] [--remit TEXT] [--over ID,ID] <name>",
		Short: "Start a chief that oversees managers and other chiefs, reads their briefs and steers them",
		Args:  cobra.MinimumNArgs(1),
		RunE: hosts(func(e *env, cmd *cobra.Command, args []string) error {
			dir, err := e.repoArg(repo)
			if err != nil {
				return err
			}
			var children []string
			if over != "" {
				children = strings.Split(over, ",")
			}
			m, err := e.core.NewChief(strings.Join(args, " "), remit, dir, harnessFlag(useCodex), children, selected)
			if err != nil {
				return err
			}
			if err := ensureWorkService(); err != nil {
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
	cmd.Flags().StringVar(&selected, "profile", "", "login profile for the chief")
	cmd.Flags().BoolVar(&useCodex, "codex", false, "run the chief on codex instead of claude")
	cmd.AddCommand(&cobra.Command{
		Use:   "attach <child> <chief>",
		Short: "Put a manager or chief under a chief; either may run on another host, named host/id",
		Args:  cobra.ExactArgs(2),
		RunE: hosts(func(e *env, cmd *cobra.Command, args []string) error {
			return e.core.Attach(args[0], args[1])
		}),
	}, &cobra.Command{
		Use:   "detach <child>",
		Short: "Make a manager or chief report to you again",
		Args:  cobra.ExactArgs(1),
		RunE: hosts(func(e *env, cmd *cobra.Command, args []string) error {
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
			if host, rest, ok := core.SplitRemote(args[0]); ok && e.core.Remotes != nil {
				remote := []string{"goal", "feed", rest}
				if keep {
					remote = append(remote, "--keep-unread")
				}
				out, err := e.core.RunRemote(host, remote...)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), out)
				return nil
			}
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
			return e.core.Looked(g.ID)
		}),
	}
	cmd.Flags().BoolVar(&keep, "keep-unread", false, "leave the news unread")
	return cmd
}

func goalEventCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "event <n>...",
		Short: "Print goal events whole, by the numbers a wake shows (#<n>, or #<host>/<n> for another host's)",
		Args:  cobra.MinimumNArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			for _, arg := range args {
				arg = strings.TrimPrefix(arg, "#")
				if host, rest, ok := core.SplitRemote(arg); ok && e.core.Remotes != nil {
					out, err := e.core.RunRemote(host, "goal", "event", rest)
					if err != nil {
						return err
					}
					fmt.Fprintln(cmd.OutOrStdout(), out)
					continue
				}
				seq, err := strconv.ParseInt(arg, 10, 64)
				if err != nil {
					return fmt.Errorf("%q is not an event number", arg)
				}
				ev, err := e.core.Store.GoalEvent(seq)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "#%d %s %s %s\n%s\n", ev.Seq, ev.Goal, ev.Created.Local().Format(time.DateTime), ev.Kind, ev.Payload)
			}
			return nil
		}),
	}
}
