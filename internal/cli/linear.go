package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/intake"
	"github.com/spf13/cobra"
)

type linearPoll struct {
	dir     string
	every   time.Duration
	checked time.Time
	client  *intake.Linear
	api     string
	now     func() time.Time
	token   func(string, linearConfig) (string, error)
}

func (p *linearPoll) poll(ctx context.Context, st *core.Store, force bool) error {
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	if !force && !p.checked.IsZero() && now.Sub(p.checked) < p.every {
		return nil
	}
	cfg, err := linearConfigAt(p.dir)
	if err != nil {
		return err
	}
	read := p.token
	if read == nil {
		read = loadLinearToken
	}
	token, err := read(p.dir, cfg)
	if err != nil {
		return errors.Join(err, st.LinearFailure(err))
	}
	if token == "" {
		return nil
	}
	if p.client != nil && now.Before(p.client.Next) {
		return nil
	}
	p.checked = now
	if p.client == nil || p.client.Token != token {
		p.client = intake.NewLinear(token)
		if p.api != "" {
			p.client.URL = p.api
		}
		if p.now != nil {
			p.client.Now = p.now
		}
	}
	before, err := st.LinearSnapshot()
	if err != nil {
		return err
	}
	snapshot, err := p.client.Snapshot(ctx, before)
	if err != nil {
		return errors.Join(err, st.LinearFailure(err))
	}
	return st.SyncLinear(snapshot)
}

func (p *linearPoll) run(ctx context.Context, st *core.Store, w io.Writer) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if err := p.poll(ctx, st, false); err != nil && ctx.Err() == nil {
			fmt.Fprintln(w, "Linear:", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func linearCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	root := &cobra.Command{Use: "linear", Short: "Read Linear issues, link them to goals, and surface explicit actions"}
	root.AddCommand(&cobra.Command{Use: "connect", Short: "Connect a Read API key through hidden input", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		if _, err := e.caller(); err == nil {
			return errors.New("the Linear credential setup requires the owner in a terminal")
		}
		dir := e.core.StateDir
		fmt.Fprint(cmd.ErrOrStderr(), "Linear Read API key: ")
		var raw []byte
		var err error
		if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(f.Fd()) {
			raw, err = term.ReadPassword(f.Fd())
			fmt.Fprintln(cmd.ErrOrStderr())
		} else {
			var line string
			line, err = bufio.NewReader(io.LimitReader(cmd.InOrStdin(), 8193)).ReadString('\n')
			if errors.Is(err, io.EOF) {
				err = nil
			}
			raw = []byte(line)
		}
		if err != nil {
			return errors.New("could not read Linear API key")
		}
		token := strings.TrimSpace(string(raw))
		if len(token) > 8192 {
			return errors.New("the Linear API key is too long")
		}
		client := intake.NewLinear(token)
		who, err := client.Identity(cmd.Context())
		if err != nil {
			return err
		}
		old, err := e.core.Store.LinearSnapshot()
		if err != nil {
			return err
		}
		if old.OwnerID != "" && (old.OwnerID != who.ID || old.WorkspaceID != who.Organization.ID) {
			return errors.New("cached Linear account differs; disconnect --clear before replacing it")
		}
		cfg := linearConfig{OwnerID: who.ID, OwnerName: who.Name, WorkspaceID: who.Organization.ID, Workspace: who.Organization.Name}
		if err = saveLinearConfig(dir, cfg, token); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Linear connected as %s in %s; run ym linear sync\n", who.Name, who.Organization.Name)
		return nil
	})})
	var clear bool
	disconnect := &cobra.Command{Use: "disconnect", Short: "Remove credentials; retain cached issues unless --clear is explicit", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		if err := disconnectLinear(e.core.StateDir); err != nil {
			return err
		}
		if clear {
			if err := e.core.Store.ClearLinear(); err != nil {
				return err
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Linear disconnected")
		return nil
	})}
	disconnect.Flags().BoolVar(&clear, "clear", false, "Explicitly clear cached issues and links")
	root.AddCommand(disconnect)
	root.AddCommand(&cobra.Command{Use: "status", Short: "Show credential configuration and last complete sync", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		cfg, err := linearConfigAt(e.core.StateDir)
		if err != nil {
			return err
		}
		snap, err := e.core.Store.LinearSnapshot()
		if err != nil {
			return err
		}
		connected := cfg.OwnerID != "" || os.Getenv("LINEAR_API_KEY") != ""
		fmt.Fprintf(cmd.OutOrStdout(), "Linear configured: %t; workspace: %s; last complete sync: %s; cached issues: %d\n", connected, cfg.Workspace, snap.At.Format(time.RFC3339), len(snap.Issues))
		if snap.Error != "" {
			fmt.Fprintln(cmd.OutOrStdout(), "Last error:", snap.Error)
		}
		return nil
	})})
	root.AddCommand(&cobra.Command{Use: "sync", Short: "Read current Linear state without launching agents", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		p := linearPoll{dir: e.core.StateDir, every: 5 * time.Minute}
		if err := p.poll(cmd.Context(), e.core.Store, true); err != nil {
			return err
		}
		s, err := e.core.Store.LinearSnapshot()
		if err != nil {
			return err
		}
		if s.OwnerID == "" {
			return errors.New("connection to Linear is not configured; run ym linear connect")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Linear: %d issues cached\n", len(s.Issues))
		return nil
	})})
	var all, asJSON bool
	list := &cobra.Command{Use: "list", Short: "List cached assigned, linked and attention issues", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		s, err := e.core.Store.LinearSnapshot()
		if err != nil {
			return err
		}
		issues := []core.LinearIssue{}
		for _, i := range s.Issues {
			if all || i.Active() && (i.Assigned || i.Watched || i.Goal != "" || i.Attention != "") {
				issues = append(issues, i)
			}
		}
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(issues)
		}
		for _, i := range issues {
			fmt.Fprintf(cmd.OutOrStdout(), "%s  [%s] P%d  %s", i.Identifier, i.State, i.Priority, i.Title)
			if i.Goal != "" {
				fmt.Fprint(cmd.OutOrStdout(), " → ", i.Goal)
			}
			if i.Attention != "" {
				fmt.Fprint(cmd.OutOrStdout(), " · needs you: ", i.Attention)
			}
			fmt.Fprintln(cmd.OutOrStdout())
		}
		return nil
	})}
	list.Flags().BoolVar(&all, "all", false, "Include completed, reassigned and unavailable cached issues")
	list.Flags().BoolVar(&asJSON, "json", false, "Print structured issue state")
	root.AddCommand(list)
	root.AddCommand(&cobra.Command{Use: "view <issue>", Short: "Read cached issue, comments and dependencies", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		i, err := e.core.Store.LinearIssue(args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(i)
	})})
	root.AddCommand(&cobra.Command{Use: "watch <issue>", Short: "Read an issue explicitly even if it is assigned elsewhere", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		return watchLinear(cmd.Context(), e.core, args[0])
	})})
	root.AddCommand(&cobra.Command{Use: "link <issue> <goal>", Short: "Link a cached issue to a stream", Args: cobra.ExactArgs(2), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		o, err := e.core.LoadOrg()
		if err != nil {
			return err
		}
		m, err := o.Lookup(args[1])
		if err != nil {
			return err
		}
		if m.GoalID == "" {
			return errors.New("link a Linear issue to a goal, not a chief")
		}
		return e.core.Store.LinkLinear(args[0], m.GoalID)
	})})
	root.AddCommand(&cobra.Command{Use: "unlink <issue>", Short: "Remove a stream link without deleting cached evidence", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error { return e.core.Store.LinkLinear(args[0], "") })})
	var reason string
	attention := &cobra.Command{Use: "attention <issue>", Short: "Place an explicit issue action in Needs You; empty --reason clears it", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("reason") {
			return errors.New("attention requires --reason naming the action")
		}
		return e.core.Store.LinearAttention(args[0], strings.TrimSpace(reason))
	})}
	attention.Flags().StringVar(&reason, "reason", "", "Specific action the owner must take")
	root.AddCommand(attention)
	return root
}

func watchLinear(ctx context.Context, c *core.Core, ref string) error {
	cfg, err := linearConfigAt(c.StateDir)
	if err != nil {
		return err
	}
	token, err := loadLinearToken(c.StateDir, cfg)
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("connection to Linear is not configured")
	}
	api := intake.NewLinear(token)
	before, err := c.Store.LinearSnapshot()
	if err != nil {
		return err
	}
	who, err := api.Identity(ctx)
	if err != nil {
		return err
	}
	issue, err := api.Issue(ctx, ref)
	if err != nil {
		return err
	}
	if err = api.Hydrate(ctx, &issue); err != nil {
		return err
	}
	issue.Assigned = issue.AssigneeID == who.ID && issue.Active()
	issue.Watched = true
	issue.ReadAt = time.Now()
	before.Issues = []core.LinearIssue{issue}
	before.OwnerID, before.OwnerName, before.WorkspaceID, before.Workspace = who.ID, who.Name, who.Organization.ID, who.Organization.Name
	return c.Store.SyncLinear(before)
}
