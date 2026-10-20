package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/project-kikkuli/yamnaya/internal/intake"
	"github.com/project-kikkuli/yamnaya/internal/work"
	"github.com/spf13/cobra"
)

func workCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	root := &cobra.Command{Use: "work", Short: "Continuing work, context, briefs, source reviews, and decisions"}
	var repo, purpose string
	create := &cobra.Command{Use: "create <name>", Short: "Create continuing work before any session starts", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		w, err := e.core.Store.Work.Ensure("manual/"+uuid.NewString(), repo, args[0])
		if err != nil {
			return err
		}
		if purpose != "" {
			w, err = e.core.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "purpose", Actor: "owner", Text: purpose})
			if err != nil {
				return err
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), w.ID)
		return nil
	})}
	create.Flags().StringVar(&repo, "repo", "", "repository identity or git common directory")
	create.Flags().StringVar(&purpose, "purpose", "", "outcome or ongoing responsibility")
	root.AddCommand(create)
	root.AddCommand(&cobra.Command{Use: "list", Short: "List continuing work", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		b, err := e.core.Board()
		if err != nil {
			return err
		}
		if err = e.core.ObserveWork(b); err != nil {
			return err
		}
		b, err = e.core.Board()
		if err != nil {
			return err
		}
		var all []work.Scope
		seen := map[string]bool{}
		for _, p := range b.Projects {
			if p.Work != nil && !seen[p.Work.ID] {
				all = append(all, *p.Work)
				seen[p.Work.ID] = true
			}
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(all)
	})})
	resolve := func(e *env, id string) (work.Scope, error) {
		b, err := e.core.Board()
		if err != nil {
			return work.Scope{}, err
		}
		return e.core.WorkFor(b, id)
	}
	for _, kind := range []string{"relate", "unrelate"} {
		kind := kind
		relation := &cobra.Command{Use: kind + " <work> <other-work> [relationship]", Short: "Connect work across repositories", Args: cobra.MinimumNArgs(2), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			w, err := resolve(e, args[0])
			if err != nil {
				return err
			}
			other, err := resolve(e, args[1])
			if err != nil {
				return err
			}
			_, err = e.core.ChangeWork(w, work.Event{Type: kind, Actor: "owner", Target: other.ID, Text: strings.Join(args[2:], " ")})
			return err
		})}
		root.AddCommand(relation)
	}
	var showJSON bool
	show := &cobra.Command{Use: "show <work>", Short: "Read current context, assessment, obligations, and direction", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		w, err := resolve(e, args[0])
		if err != nil {
			return err
		}
		if showJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(w)
		}
		work.Render(cmd.OutOrStdout(), w)
		return nil
	})}
	show.Flags().BoolVar(&showJSON, "json", false, "print the versioned work record as JSON")
	root.AddCommand(show)
	root.AddCommand(&cobra.Command{Use: "history <work>", Short: "Read consequential changes and decision rationale", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		w, err := resolve(e, args[0])
		if err != nil {
			return err
		}
		if w.Host != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), "Captured remote history (latest 20 events); full history is available on the origin host.")
			return json.NewEncoder(cmd.OutOrStdout()).Encode(w.Activity)
		}
		events, err := e.core.Store.Work.History(w.ID, 0)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(events)
	})})
	for _, kind := range []string{"context", "purpose", "authority", "manager", "bind", "message", "close", "resume"} {
		var file string
		var revision int64 = -1
		kind := kind
		cmd := &cobra.Command{Use: kind + " <work> [text]", Short: "Set " + kind + " for continuing work", Args: cobra.MinimumNArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			w, err := resolve(e, args[0])
			if err != nil {
				return err
			}
			text := strings.Join(args[1:], " ")
			if file != "" {
				var b []byte
				if file == "-" {
					b, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), 8<<20))
				} else {
					b, err = os.ReadFile(file)
				}
				if err != nil {
					return err
				}
				text = string(b)
			}
			if strings.TrimSpace(text) == "" {
				return fmt.Errorf("%s needs text or --file", kind)
			}
			if revision >= 0 && revision != w.Revision {
				return work.ErrConflict
			}
			event := work.Event{ID: uuid.NewString(), Type: kind, Actor: "owner", Text: text}
			if kind == "manager" || kind == "bind" {
				event.Target = text
			}
			if kind == "message" {
				event.Target = w.Manager
				if event.Target == "" {
					return fmt.Errorf("assign a manager before messaging this work")
				}
			}
			w, err = e.core.ChangeWork(w, event)
			if err == nil {
				if w.Host != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s %s queued for %s; awaiting application\n", w.ID, kind, w.Host)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "%s %s recorded; revision %d\n", w.ID, kind, w.Revision)
				}
			}
			return err
		})}
		cmd.Flags().StringVar(&file, "file", "", "read text from a file; - reads stdin")
		cmd.Flags().Int64Var(&revision, "revision", -1, "expected work revision; reject concurrent changes")
		root.AddCommand(cmd)
	}
	root.AddCommand(&cobra.Command{Use: "brief <work>", Short: "Publish a manager assessment as JSON on stdin", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		w, err := resolve(e, args[0])
		if err != nil {
			return err
		}
		var brief work.Brief
		if err = json.NewDecoder(io.LimitReader(cmd.InOrStdin(), 8<<20)).Decode(&brief); err != nil {
			return err
		}
		_, err = e.core.ChangeWork(w, work.Event{ID: uuid.NewString(), Type: "brief", Actor: w.Manager, Brief: brief})
		return err
	})})
	var revision int64
	adopt := &cobra.Command{Use: "adopt <work>", Short: "Record that this session incorporated a context revision", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		w, err := resolve(e, args[0])
		if err != nil {
			return err
		}
		caller, err := e.caller()
		if err != nil {
			return err
		}
		_, err = e.core.ChangeWork(w, work.Event{ID: uuid.NewString(), Type: "adopt", Actor: caller.Session, Context: revision})
		return err
	})}
	adopt.Flags().Int64Var(&revision, "revision", 0, "context revision actually incorporated")
	adopt.MarkFlagRequired("revision")
	root.AddCommand(adopt)
	root.AddCommand(&cobra.Command{Use: "inbox", Short: "Receive pending work updates for the calling session", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		caller, err := e.caller()
		if err != nil {
			return err
		}
		messages, err := e.core.Store.Work.Inbox(caller.Session)
		if err != nil {
			return err
		}
		for _, text := range messages {
			fmt.Fprintln(cmd.OutOrStdout(), text)
		}
		return nil
	})})
	var requester, question, kind string
	watch := &cobra.Command{Use: "watch <work> <url>", Short: "Watch a PR, shared Notion page, or local artifact for revisions", Args: cobra.ExactArgs(2), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		w, err := resolve(e, args[0])
		if err != nil {
			return err
		}
		coverage := "explicitly registered source"
		address, err := intake.Identity(kind, args[1])
		if err != nil {
			return err
		}
		if kind == "notion" {
			coverage = "registered pages shared with the integration; Notion has no workspace-wide review-request inbox API"
		}
		_, err = e.core.ChangeWork(w, work.Event{ID: uuid.NewString(), Type: "watch", Actor: "owner", Source: work.Source{Kind: kind, URL: address, Requester: requester, Question: question, Coverage: coverage}})
		return err
	})}
	watch.Flags().StringVar(&kind, "kind", "github", "github, notion, or file")
	watch.Flags().StringVar(&requester, "requester", "", "person explicitly requesting review; absent means provisional")
	watch.Flags().StringVar(&question, "question", "", "judgment requested")
	root.AddCommand(watch)
	root.AddCommand(&cobra.Command{Use: "prepare <work> <review>", Short: "Prepare a source-version-bound judgment from JSON on stdin", Args: cobra.ExactArgs(2), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		w, err := resolve(e, args[0])
		if err != nil {
			return err
		}
		var r work.Review
		if err = json.NewDecoder(io.LimitReader(cmd.InOrStdin(), 8<<20)).Decode(&r); err != nil {
			return err
		}
		actor := "owner"
		if caller, callerErr := e.caller(); callerErr == nil {
			actor = caller.Session
		} else if e.session != "" {
			return callerErr
		}
		_, err = e.core.ChangeWork(w, work.Event{ID: uuid.NewString(), Type: "prepare", Actor: actor, Target: args[1], Review: r, Context: r.PreparedContext})
		return err
	})})
	for _, kind := range []string{"feedback", "decide"} {
		kind := kind
		var expected int64 = -1
		cmd := &cobra.Command{Use: kind + " <work> <review> <text>", Short: "Record the owner's " + kind + " and preserve follow-through", Args: cobra.MinimumNArgs(3), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			w, err := resolve(e, args[0])
			if err != nil {
				return err
			}
			if expected >= 0 && expected != w.Revision {
				return work.ErrConflict
			}
			_, err = e.core.ChangeWork(w, work.Event{ID: uuid.NewString(), Type: kind, Actor: "owner", Target: args[1], Text: strings.Join(args[2:], " ")})
			return err
		})}
		cmd.Flags().Int64Var(&expected, "revision", -1, "work revision you reviewed")
		root.AddCommand(cmd)
	}
	root.AddCommand(&cobra.Command{Use: "trigger <work> <RFC3339> <reason>", Short: "Persist the manager's next wake trigger", Args: cobra.MinimumNArgs(3), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		w, err := resolve(e, args[0])
		if err != nil {
			return err
		}
		at, err := time.Parse(time.RFC3339, args[1])
		if err != nil {
			return err
		}
		_, err = e.core.ChangeWork(w, work.Event{ID: uuid.NewString(), Type: "trigger", Actor: w.Manager, WakeAt: at, Text: strings.Join(args[2:], " ")})
		return err
	})})
	root.AddCommand(&cobra.Command{Use: "export", Short: "Export host-scoped continuing work for a peer", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		b, err := e.core.Board()
		if err != nil {
			return err
		}
		if err = e.core.ObserveWork(b); err != nil {
			return err
		}
		all, err := e.core.Store.Work.List()
		if err != nil {
			return err
		}
		for i := range all {
			all[i].Activity, err = e.core.Store.Work.Recent(all[i].ID)
			if err != nil {
				return err
			}
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(all)
	})}, &cobra.Command{Use: "apply", Short: "Apply a versioned, idempotent work event from stdin", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		var event work.Event
		if err := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), 8<<20)).Decode(&event); err != nil {
			return err
		}
		if event.ID == "" {
			return fmt.Errorf("remote event needs its idempotency identity")
		}
		w, err := e.core.ApplyWork(event)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(w)
	})}, &cobra.Command{Use: "host <ssh-host>", Short: "Observe continuing work on an independent SSH host", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		if strings.HasPrefix(args[0], "-") || strings.ContainsAny(args[0], " \t\r\n") {
			return fmt.Errorf("use an SSH host alias")
		}
		return e.core.Store.Work.AddPeer(args[0])
	})})
	return root
}
