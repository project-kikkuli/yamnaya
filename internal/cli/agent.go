package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/work"
	"github.com/spf13/cobra"
)

func agentCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	root := &cobra.Command{Use: "agent", Short: "Persistent chiefs and managers, independent of execution sessions"}
	root.AddCommand(agentBatchCommands(with)...)
	var role, repo, remit, authority, directory, harness, session, parent string
	spawn := &cobra.Command{Use: "spawn <name>", Short: "Create a delegated agent and schedule its first management pass", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		dir, err := filepath.Abs(core.ExpandHome(directory))
		if err != nil {
			return err
		}
		if _, err = os.Stat(dir); err != nil {
			return err
		}
		if repo != "" {
			repo = agentRepo(repo)
		}
		a, err := e.core.Store.Work.CreateAgent(args[0], role, repo, remit, authority, work.Execution{Harness: harness, Directory: dir}, e.actor())
		if err != nil {
			return err
		}
		if session != "" {
			a.Session = session
			if previous, findErr := e.core.Store.Work.Find(session); findErr == nil && previous.ID != a.Workspace {
				if err = e.core.Store.Work.Attach(a.ID, previous.ID, "Continue responsibility with the linked native conversation"); err != nil {
					return err
				}
			}
			if err = e.core.Store.Work.SaveAgent(a); err != nil {
				return err
			}
		}
		if parent != "" {
			w, err := e.core.Store.Work.Find(parent)
			if err != nil {
				return err
			}
			aw, err := e.core.Store.Work.Get(a.Workspace)
			if err != nil {
				return err
			}
			if _, err = e.core.Store.Work.Commit(work.Event{Work: aw.ID, Expected: aw.Revision, Type: "parent", Target: w.ID, Actor: e.actor()}); err != nil {
				return err
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), a.ID)
		return ensureWorkService()
	})}
	spawn.Flags().StringVar(&role, "role", "chief", "chief or manager")
	spawn.Flags().StringVar(&repo, "repo", "", "repository scope; empty allows cross-project work")
	spawn.Flags().StringVar(&remit, "remit", "", "outcome or standing responsibility")
	spawn.Flags().StringVar(&authority, "authority", "", "actions delegated to this agent")
	spawn.Flags().StringVar(&directory, "directory", ".", "independent execution working directory")
	spawn.Flags().StringVar(&harness, "harness", "codex", "codex or claude")
	spawn.Flags().StringVar(&session, "session-id", "", "existing native conversation to continue")
	spawn.Flags().StringVar(&parent, "parent", "", "delegation whose owner verifies this agent result")
	spawn.MarkFlagRequired("remit")
	spawn.MarkFlagRequired("authority")
	root.AddCommand(spawn)
	root.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		all, err := e.core.Store.Work.Agents()
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(all)
	})})
	root.AddCommand(&cobra.Command{Use: "snapshot <agent>", Short: "Read the remit, working context, commitments, pending inputs, and attachments", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		x, err := e.core.Store.Work.AgentSnapshot(args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(x)
	})})
	for _, kind := range []string{"attach", "detach", "assign"} {
		kind := kind
		cmd := &cobra.Command{Use: kind + " <agent> <work-or-session> [responsibility]", Args: cobra.MinimumNArgs(2), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			a, err := e.core.Store.Work.Agent(args[0])
			if err != nil {
				return err
			}
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			if err = e.core.ObserveWork(b); err != nil {
				return err
			}
			w, err := e.core.WorkFor(b, args[1])
			if err != nil {
				return err
			}
			switch kind {
			case "assign":
				_, err = e.core.Store.Work.Assign(w.ID, a.ID, e.actor())
			case "detach":
				err = e.core.Store.Work.Detach(a.ID, w.ID)
			default:
				reason := strings.Join(args[2:], " ")
				if reason == "" {
					reason = "Carry context, investigate consequential changes, and own follow-through under the remit"
				}
				err = e.core.Store.Work.Attach(a.ID, w.ID, reason)
			}
			return err
		})}
		root.AddCommand(cmd)
	}
	root.AddCommand(&cobra.Command{Use: "kill <agent>", Short: "Stop this chief's native execution, drop its attachments and defaults, and close its workspace so it never wakes again", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		a, err := e.core.Store.Work.Agent(args[0])
		if err != nil {
			return err
		}
		b, err := e.core.Board()
		if err != nil {
			return err
		}
		msg, err := e.core.KillAgent(a, b)
		if err == nil {
			fmt.Fprintln(cmd.OutOrStdout(), msg)
		}
		return err
	})})
	var remove bool
	rule := &cobra.Command{Use: "default <agent> [repo]", Short: "Attach this chief to new sessions globally or within one repository", Args: cobra.RangeArgs(1, 2), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		repo := ""
		if len(args) == 2 {
			repo = agentRepo(args[1])
		}
		return e.core.Store.Work.SetRule(args[0], repo, remove)
	})}
	rule.Flags().BoolVar(&remove, "remove", false, "remove this new-session rule; existing attachments remain")
	root.AddCommand(rule)
	for _, kind := range []string{"pause", "resume", "replace"} {
		kind := kind
		var native, directory string
		cmd := &cobra.Command{Use: kind + " <agent>", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			a, err := e.core.Store.Work.Agent(args[0])
			if err != nil {
				return err
			}
			a.Paused = kind == "pause"
			if kind == "replace" {
				a.Session = native
				if directory != "" {
					a.Execution.Directory = directory
				}
			}
			if err = e.core.Store.Work.SaveAgent(a); err != nil {
				return err
			}
			if kind != "pause" {
				w, err := e.core.Store.Work.Get(a.Workspace)
				if err != nil {
					return err
				}
				_, err = e.core.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "message", Actor: e.actor(), Text: "Resume responsibility; investigate pending failures and continue from the current brief"})
				return err
			}
			return nil
		})}
		if kind == "replace" {
			cmd.Flags().StringVar(&native, "session-id", "", "replacement native session; empty starts a fresh execution")
			cmd.Flags().StringVar(&directory, "directory", "", "replacement execution directory")
		}
		root.AddCommand(cmd)
	}
	root.AddCommand(&cobra.Command{Use: "message <agent> <text>", Args: cobra.MinimumNArgs(2), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		a, err := e.core.Store.Work.Agent(args[0])
		if err != nil {
			return err
		}
		w, err := e.core.Store.Work.Get(a.Workspace)
		if err != nil {
			return err
		}
		_, err = e.core.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "message", Actor: e.actor(), Text: strings.Join(args[1:], " ")})
		return err
	})})
	root.AddCommand(&cobra.Command{Use: "checkpoint <agent>", Short: "Publish a substantive management checkpoint from JSON on stdin", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		var c work.Checkpoint
		if err := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), 8<<20)).Decode(&c); err != nil {
			return err
		}
		token := os.Getenv("YAMNAYA_AGENT_RUN")
		if token == "" {
			token = uuid.NewString()
		}
		return e.core.Store.Work.Checkpoint(args[0], token, c)
	})})
	root.AddCommand(&cobra.Command{Use: "capacity [concurrent-executions]", Short: "Read or set background native execution capacity; independently running sessions are unrestricted", Args: cobra.MaximumNArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			n, err := strconv.Atoi(args[0])
			if err != nil {
				return err
			}
			if err = e.core.Store.Work.SetAgentCapacity(n); err != nil {
				return err
			}
		}
		n, err := e.core.Store.Work.AgentCapacity()
		if err == nil {
			fmt.Fprintln(cmd.OutOrStdout(), n)
		}
		return err
	})})
	return root
}

func agentRepo(repo string) string {
	dir := core.ExpandHome(repo)
	if g := core.ReadGit(dir); g.Common != "" {
		return g.Common
	}
	return repo
}

func (e *env) actor() string {
	if id := os.Getenv("YAMNAYA_AGENT_ID"); id != "" {
		return id
	}
	session := e.session
	if session == "" {
		if caller, ok := core.CallerFromEnv(os.Getenv); ok {
			session = caller.Session
		}
	}
	if session == "" && e.core.Procs != nil {
		if caller, err := e.core.Calling(os.Getpid(), os.Getenv); err == nil {
			session = caller.Session
		}
	}
	if session != "" {
		if all, err := e.core.Store.Work.Agents(); err == nil {
			for _, a := range all {
				if a.Session == session {
					return a.ID
				}
			}
		}
	}

	return "owner"
}
