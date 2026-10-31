package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func itemCmd(with, withHosts func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	cmd := &cobra.Command{Use: "item", Short: "File and manage open items"}
	cmd.AddCommand(itemAddCmd(with), itemReviewCmd(with), itemEscalateCmd(withHosts), itemConsolidateCmd(withHosts), itemListCmd(withHosts), itemShowCmd(withHosts), itemInboxCmd(with), itemPrepareCmd(withHosts))
	with = withHosts
	cmd.AddCommand(&cobra.Command{
		Use:   "answer <id> <text>",
		Short: "Answer an item; an idle agent gets it typed in, a busy one reads it from ym item inbox",
		Args:  cobra.MinimumNArgs(2),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			key, err := e.itemKey(b, args[0])
			if err != nil {
				return err
			}
			it, d, err := e.core.Answer(b, key, strings.Join(args[1:], " "))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s answered, %s\n", it.ID, d)
			return nil
		}),
	}, &cobra.Command{
		Use:   "go <id>",
		Short: "Answer a go item with " + core.GoAhead,
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			key, err := e.itemKey(b, args[0])
			if err != nil {
				return err
			}
			it, d, err := e.core.Go(b, key)
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
			key, err := e.itemKey(b, args[0])
			if err != nil {
				return err
			}
			it, d, err := e.core.Dismiss(b, key, strings.Join(args[1:], " "))
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
			key, err := e.itemKey(b, args[0])
			if err != nil {
				return err
			}
			it, err := e.core.Snooze(key)
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
			key, err := e.itemKey(b, args[0])
			if err != nil {
				return err
			}
			it, err := e.core.Reopen(key)
			if err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", it.ID, it.State)
			}
			return err
		}),
	})
	return cmd
}

func itemAddCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return itemCreateCmd(with, false)
}

func itemReviewCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return itemCreateCmd(with, true)
}

func itemCreateCmd(with func(runFunc) func(*cobra.Command, []string) error, review bool) *cobra.Command {
	var it core.Item
	var options []string
	var to string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "File an item against the calling session and print its id; ym attaches the goal, its pull requests and recent activity, and refuses a decision or default bound for the owner that lacks --rec (and a decision without two --option)",
		Args:  cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if to != "" && to != "owner" {
				return fmt.Errorf("--to must be owner; omit it for normal manager routing")
			}
			c, err := e.caller()
			if err != nil {
				return err
			}
			it.Session, it.Harness = c.Session, c.Harness
			if review {
				it.Kind = core.KindReview
				it.Evidence = append([]string{args[0]}, it.Evidence...)
			}
			for _, o := range options {
				label, effect, _ := strings.Cut(o, "=")
				it.Options = append(it.Options, core.Option{Label: strings.TrimSpace(label), Effect: strings.TrimSpace(effect)})
			}
			var added core.Item
			if to == "owner" {
				added, err = e.core.AddOwnerItem(it)
			} else {
				added, err = e.core.AddItem(it)
			}
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), added.ID)
			if added.Recipient != core.Owner {
				if host, id, remote := core.SplitRemote(added.Recipient); remote {
					fmt.Fprintf(e.nicked(cmd.ErrOrStderr()), "sent to %s on %s, who owns the answer; it comes back through ym item inbox\n", id, host)
				} else if o, err := e.core.LoadOrg(); err == nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "sent to %s, who owns the answer; it comes back through ym item inbox\n", o.Managers[added.Recipient].Label())
				}
			}
			return nil
		}),
	}
	cmd.Flags().StringVar(&it.Action, "action", "", "chat when the owner must act in the originating conversation")
	cmd.Flags().StringVar(&to, "to", "", "owner for an explicit owner request; omitted follows manager routing")
	if review {
		cmd.Use, cmd.Short, cmd.Args = "review <document>", "Request the owner's review of a document link or path; keeps one durable item until answered or dismissed", cobra.ExactArgs(1)
	} else {
		cmd.Flags().StringVar(&it.Kind, "kind", "", "decision, default, chore, go, or review")
		cmd.MarkFlagRequired("kind")
	}
	cmd.Flags().StringVar(&it.Question, "question", "", "one-line question")
	cmd.Flags().StringVar(&it.Rec, "rec", "", "what you recommend")
	cmd.Flags().StringArrayVar(&options, "option", nil, "option as label=effect (repeatable)")
	cmd.Flags().StringVar(&it.Assumption, "assumption", "", "what you are assuming meanwhile")
	cmd.Flags().StringArrayVar(&it.Evidence, "evidence", nil, "a PR, CI run, commit, file or other link the call rests on (repeatable)")
	cmd.Flags().StringVar(&it.Tried, "tried", "", "what you tried before asking")
	cmd.Flags().IntVar(&it.Blocks, "blocks", 0, "how many pieces of work wait on this")
	cmd.MarkFlagRequired("question")
	if review {
		cmd.MarkFlagRequired("rec")
	}
	return cmd
}

func itemEscalateCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{Use: "escalate <id>", Short: "Send an existing prepared open item to the owner, keeping its id, source and history", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		b, err := e.core.Board()
		if err != nil {
			return err
		}
		key, err := e.itemKey(b, args[0])
		if err != nil {
			return err
		}
		it, err := e.core.Escalate(key)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s owner escalation requested\n", it.ID)
		return nil
	})}
}

func itemPrepareCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var p core.Prepared
	var options []string
	cmd := &cobra.Command{
		Use:   "prepare <id>",
		Short: "Add a recommendation, options, evidence or what was tried to an open ask, so the owner can decide it; an ask bound for the owner must end up with --rec and, for a decision, two --option",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			key, err := e.itemKey(b, args[0])
			if err != nil {
				return err
			}
			for _, o := range options {
				label, effect, _ := strings.Cut(o, "=")
				p.Options = append(p.Options, core.Option{Label: strings.TrimSpace(label), Effect: strings.TrimSpace(effect)})
			}
			it, err := e.core.Prepare(b, key, p)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s prepared\n", it.ID)
			return nil
		}),
	}
	cmd.Flags().StringVar(&p.Rec, "rec", "", "what you recommend and why")
	cmd.Flags().StringArrayVar(&options, "option", nil, "option as label=effect (repeatable; replaces the ask's options)")
	cmd.Flags().StringArrayVar(&p.Evidence, "evidence", nil, "a PR, CI run, commit, file or other link the call rests on (repeatable)")
	cmd.Flags().StringVar(&p.Tried, "tried", "", "what was tried before asking")
	cmd.Flags().StringVar(&p.Assumption, "assumption", "", "what is assumed meanwhile")
	return cmd
}

func itemListCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var all, asJSON, everywhere, toMe bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List open items of the calling session's project (--all for every state, --everywhere for every project, --to-me for the asks routed to the calling manager)",
		Args:  cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			if toMe {
				c, err := e.caller()
				if err != nil {
					return err
				}
				m, ok := b.Org.ManagerOf(e.core.Resolve(c.Session))
				if !ok {
					return fmt.Errorf("session %s is not a manager or chief", core.Short(c.Session))
				}
				items, err := e.core.Store.Items(core.ItemFilter{States: []string{core.ItemOpen}})
				if err != nil {
					return err
				}
				mine := []core.Item{}
				for _, it := range items {
					if it.Recipient == m.ID {
						mine = append(mine, it)
					}
				}
				if e.core.Remotes != nil {
					me := core.RemoteKey(e.core.SelfName(), m.ID)
					for _, r := range e.core.Remotes.Boards() {
						for _, it := range r.Items {
							if it.State == core.ItemOpen && it.Recipient == me {
								mine = append(mine, it)
							}
						}
					}
				}
				return printItems(e.textOut(cmd.OutOrStdout(), asJSON), mine, asJSON)
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
			return printItems(e.textOut(cmd.OutOrStdout(), asJSON), items, asJSON)
		}),
	}
	cmd.Flags().BoolVar(&all, "all", false, "include answered, dismissed and snoozed items")
	cmd.Flags().BoolVar(&everywhere, "everywhere", false, "items of every project")
	cmd.Flags().BoolVar(&toMe, "to-me", false, "open items routed to the calling manager or chief")
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
			out := e.nicked(cmd.OutOrStdout())
			for _, it := range items {
				if it.State == core.ItemAnswered {
					fmt.Fprintf(out, "re %s: %s\n", it.Spoken(), it.Answer)
				} else {
					fmt.Fprintf(out, "re %s: dismissed: %s\n", it.Spoken(), it.Reason)
				}
			}
			msgs, err := e.core.TakeMessages(c.Session)
			for _, m := range msgs {
				fmt.Fprintf(out, "msg: %s\n", m.Text)
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
			if host, rest, ok, err := e.remoteItemRef(args[0]); err != nil {
				return err
			} else if ok {
				if !e.reaches(host) {
					return notRemote(args[0], host)
				}
				remote := []string{"item", "show", rest}
				if asJSON {
					remote = append(remote, "--json")
				}
				out, err := e.core.RunRemote(host, remote...)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), out)
				return nil
			}
			it, err := e.item(b, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), toJSON(it))
			}
			fmt.Fprint(e.nicked(cmd.OutOrStdout()), e.core.ItemText(it))
			return nil
		}),
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

type itemJSON struct {
	Seq        int64         `json:"seq"`
	Action     string        `json:"action,omitempty"`
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
	Evidence   []string      `json:"evidence"`
	Tried      string        `json:"tried,omitempty"`
	Dossier    *core.Dossier `json:"dossier,omitempty"`
	Missing    []string      `json:"missing,omitempty"`
	Preparer   string        `json:"preparer,omitempty"`
	Blocks     int           `json:"blocks"`
	Session    string        `json:"session"`
	Harness    string        `json:"harness"`
	Answer     string        `json:"answer,omitempty"`
	Reason     string        `json:"reason,omitempty"`
	CreatedAt  string        `json:"created_at"`
	AnsweredAt string        `json:"answered_at,omitempty"`
	Received   bool          `json:"received"`
	Recipient  string        `json:"recipient"`
	Host       string        `json:"host,omitempty"`
	History    []core.Event  `json:"history,omitempty"`
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func toJSON(it core.Item) itemJSON {
	var d *core.Dossier
	if !it.Dossier.Empty() {
		d = &it.Dossier
	}
	ev := it.Evidence
	if ev == nil {
		ev = []string{}
	}
	return itemJSON{Missing: core.Unprepared(it), Preparer: it.Preparer, Evidence: ev, Tried: it.Tried, Dossier: d, Seq: it.Seq, Recipient: it.Recipient, Host: it.Host, Action: it.Action, ID: it.ID, Project: core.ScopeName(it.Scope), Scope: it.Scope, Alias: it.Alias, Kind: it.Kind, State: it.State, Question: it.Question, Rec: it.Rec, Options: it.Options,
		Assumption: it.Assumption, Blocks: it.Blocks, Session: it.Session, Harness: it.Harness, Answer: it.Answer,
		Reason: it.Reason, CreatedAt: ts(it.CreatedAt), AnsweredAt: ts(it.AnsweredAt), Received: !it.Delivered.IsZero(), History: it.History}
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
		if it.Host != "" {
			id = it.Key()
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
	host, rest, ok, err := e.remoteItemRef(ref)
	if err != nil {
		return core.Item{}, err
	}
	if ok {
		if !e.reaches(host) {
			return core.Item{}, notRemote(ref, host)
		}
		for _, r := range e.core.Remotes.Boards() {
			for _, it := range r.Items {
				if r.Host == host && (rest == "#"+strconv.FormatInt(it.Seq, 10) || rest == it.ID || rest == it.Qualified()) {
					return it, nil
				}
			}
		}
		return core.Item{}, fmt.Errorf("item %s: not found on %s", rest, host)
	}
	return e.core.Store.Find(e.itemScope(b), ref)
}

// itemKey is the key core acts on: another host's ref as given, which core carries to that host, else the local item's key.
func (e *env) itemKey(b core.Board, ref string) (string, error) {
	if _, _, ok, err := e.remoteItemRef(ref); ok || err != nil {
		return ref, err
	}
	it, err := e.core.Store.Find(e.itemScope(b), ref)
	return it.Key(), err
}

// remoteItemRef splits a ref naming another host's item; an @nickname no host answers to is an error.
func (e *env) remoteItemRef(ref string) (host, rest string, ok bool, err error) {
	if host, rest, ok := e.core.RemoteRef(ref); ok {
		return host, rest, true, nil
	}
	if host, _, ok := core.SplitRemote(ref); ok && strings.HasPrefix(host, "@") {
		return "", "", false, notRemote(ref, strings.TrimPrefix(host, "@"))
	}
	return "", "", false, nil
}

func (e *env) reaches(host string) bool {
	return e.core.Remotes != nil && slices.Contains(e.core.Remotes.Hosts(), host)
}

func notRemote(ref, host string) error {
	return fmt.Errorf("item %s: host %s is not a configured remote: %w", ref, host, core.ErrNotFound)
}

func (e *env) itemScope(b core.Board) string {
	if e.project != "" {
		for _, r := range b.Repos {
			if r.Key == e.project || strings.EqualFold(r.Name, e.project) {
				return r.Dir
			}
		}
		if key, err := b.ProjectKey(e.project); err == nil {
			if p, ok := b.Project(key); ok && p.RepoDir != "" {
				return p.RepoDir
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
