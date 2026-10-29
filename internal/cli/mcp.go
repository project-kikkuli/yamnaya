package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/project-kikkuli/yamnaya/internal/mcp"
	"github.com/project-kikkuli/yamnaya/internal/profile"
	"github.com/spf13/cobra"
)

var mcpTerminal = func() bool { return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd()) }

func mcpCmd() *cobra.Command {
	var harness, id, cwd, root, scope, source, revision string
	var asJSON bool
	cmd := &cobra.Command{Use: "mcp", Short: "Discover and connect native MCP servers for an exact login profile", Long: "Manage Claude and Codex MCP servers without copying credentials. Inventory reads files only. Checks may start the selected server. OAuth requires your terminal. On another host: ym remote <host> mcp ...; roots and cwd belong to that host."}
	cmd.PersistentFlags().StringVar(&harness, "harness", "", "claude or codex (required for changes and checks)")
	cmd.PersistentFlags().StringVar(&id, "profile", "default", "exact profile ID on this host; never auto-selected by quota")
	cmd.PersistentFlags().StringVar(&cwd, "cwd", "", "absolute working directory on this host (default: current directory)")
	cmd.PersistentFlags().StringVar(&root, "root", "", "refuse if the selected profile's resolved native root differs")
	cmd.PersistentFlags().StringVar(&scope, "scope", "", "declared server scope: user, project, local (add defaults to user)")
	cmd.PersistentFlags().StringVar(&source, "source", "", "exact source path from list; refuses a changed or ambiguous source")
	cmd.PersistentFlags().StringVar(&revision, "revision", "", "refuse if the selected definition changed since inventory")
	cmd.PersistentFlags().BoolVar(&asJSON, "json", false, "print safe inventory and status as JSON")
	load := func() (string, []profile.Profile, error) {
		state, err := stateDir()
		if err != nil {
			return "", nil, err
		}
		ps, err := profile.Load(state)
		return state, ps, err
	}
	target := func(ps []profile.Profile) (mcp.Target, error) {
		if harness == "" {
			return mcp.Target{}, fmt.Errorf("choose --harness claude or codex and --profile from ym profiles")
		}
		p, err := profile.Find(ps, harness, id)
		if err != nil {
			return mcp.Target{}, err
		}
		if root != "" && filepath.Clean(root) != p.Root {
			return mcp.Target{}, fmt.Errorf("profile root changed; reopen MCPs for the current native root")
		}
		work := cwd
		if work == "" {
			work, err = os.Getwd()
			if err != nil {
				return mcp.Target{}, err
			}
		}
		return mcp.NewTarget(p, work)
	}
	list := &cobra.Command{Use: "list", Short: "Read configured names and scopes without starting any MCP server", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if harness == "" && (cmd.PersistentFlags().Changed("profile") || root != "") {
			return fmt.Errorf("--profile and --root require --harness; inventory will not broaden an explicit target")
		}
		state, ps, err := load()
		if err != nil {
			return err
		}
		var inventories []mcp.Inventory
		for _, p := range ps {
			if harness != "" && (p.Harness != harness || p.ID != profile.ID(id)) {
				continue
			}
			work := cwd
			if work == "" {
				work, err = os.Getwd()
				if err != nil {
					return err
				}
			}
			if root != "" && p.Root != filepath.Clean(root) {
				return fmt.Errorf("profile root changed; reopen MCPs")
			}
			t, err := mcp.NewTarget(p, work)
			if err != nil {
				return err
			}
			i, err := mcp.Read(t)
			if err != nil {
				return err
			}
			i.Observations(state, time.Now())
			inventories = append(inventories, i)
		}
		if len(inventories) == 0 {
			return fmt.Errorf("no matching configured profile on this host")
		}
		if asJSON {
			return writeJSON(c.OutOrStdout(), inventories)
		}
		for _, i := range inventories {
			printMCP(c, i)
		}
		return nil
	}}
	var transport, url, bearer string
	var notion bool
	add := &cobra.Command{Use: "add <name> [-- <executable> <args>...]", Short: "Configure Notion or a custom HTTP/SSE/stdio server in native storage", Args: cobra.MinimumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		_, ps, err := load()
		if err != nil {
			return err
		}
		t, err := target(ps)
		if err != nil {
			return err
		}
		a := mcp.Addition{Name: args[0], Scope: scope, Transport: transport, URL: url, Command: args[1:], BearerEnv: bearer}
		if a.Scope == "" {
			a.Scope = "user"
		}
		if notion {
			if url != "" || len(args) > 1 || transport != "http" || bearer != "" {
				return fmt.Errorf("--notion supplies its own HTTP endpoint; authenticate with login")
			}
			a.URL = "https://mcp.notion.com/mcp"
		}
		binary, err := bin(t.Harness)
		if err != nil {
			return err
		}
		if err = (mcp.Native{Binary: binary}).Add(c.Context(), t, a); err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), "configured; run ym mcp login, then ym mcp check for this profile and scope; existing sessions may need native reconnect or resume")
		return nil
	}}
	add.Flags().StringVar(&transport, "transport", "http", "native transport: http, sse (Claude only), stdio")
	add.Flags().StringVar(&url, "url", "", "HTTP endpoint without credentials or query parameters")
	add.Flags().StringVar(&bearer, "bearer-token-env-var", "", "Codex HTTP bearer variable name, never the token value")
	add.Flags().BoolVar(&notion, "notion", false, "use the Notion HTTP MCP preset; OAuth login is a separate terminal action")
	cmd.AddCommand(list, add)
	cmd.AddCommand(&cobra.Command{Use: "manage", Short: "Open the native terminal for /mcp, project approval and plugin/cloud sources", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		_, ps, err := load()
		if err != nil {
			return err
		}
		t, err := target(ps)
		if err != nil {
			return err
		}
		if !mcpTerminal() {
			return fmt.Errorf("native MCP management requires the owner's interactive terminal")
		}
		binary, err := bin(t.Harness)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), "Use /mcp in the native harness for approvals, plugin/cloud connections and advanced native configuration. Exit to return to ym.")
		return (mcp.Native{Binary: binary}).Manage(c.Context(), t, c.InOrStdin(), c.OutOrStdout(), c.ErrOrStderr())
	}})
	for _, operation := range []string{"login", "logout", "check", "remove"} {
		op := operation
		var noBrowser bool
		child := &cobra.Command{Use: op + " <name>", Short: map[string]string{"login": "Authenticate in your terminal, without saving OAuth output in ym", "logout": "Clear native MCP authentication in your terminal", "check": "Explicitly check the effective server using the native harness", "remove": "Remove exactly one declared native server scope"}[op], Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
			state, ps, err := load()
			if err != nil {
				return err
			}
			t, err := target(ps)
			if err != nil {
				return err
			}
			if scope == "" {
				return fmt.Errorf("select an explicit --scope from ym mcp list; same names may exist in several scopes")
			}
			i, err := mcp.Read(t)
			if err != nil {
				return err
			}
			s, err := i.Select(args[0], scope, source)
			if err != nil {
				return err
			}
			if revision != "" && revision != s.Fingerprint {
				return fmt.Errorf("MCP definition changed; refresh inventory before acting")
			}
			binary, err := bin(t.Harness)
			if err != nil {
				return err
			}
			native := mcp.Native{Binary: binary}
			switch op {
			case "login", "logout":
				if !mcpTerminal() {
					return fmt.Errorf("MCP authentication requires the owner's interactive terminal; open MCPs and choose login (remote hosts use --no-browser)")
				}
				if err = mcp.ForgetObservation(state, t, s); err != nil {
					return err
				}
				return native.Auth(c.Context(), t, s, op == "logout", noBrowser, c.InOrStdin(), c.OutOrStdout(), c.ErrOrStderr())
			case "remove":
				if err = native.Remove(c.Context(), t, s); err != nil {
					return err
				}
				return mcp.ForgetObservation(state, t, s)
			case "check":
				o, checkErr := native.Check(c.Context(), t, s)
				if checkErr != nil {
					o.Health = "unsupported-or-unavailable"
				}
				if err = mcp.SaveObservation(state, t, s, o); err != nil {
					return err
				}
				i.Observations(state, time.Now())
				if asJSON {
					if err = writeJSON(c.OutOrStdout(), i); err != nil {
						return err
					}
				} else {
					printMCP(c, i)
				}
				return checkErr
			}
			return nil
		}}
		if op == "login" {
			child.Flags().BoolVar(&noBrowser, "no-browser", false, "native headless OAuth; display the link and paste callback only in this terminal")
		}
		cmd.AddCommand(child)
	}
	return cmd
}

func printMCP(c *cobra.Command, i mcp.Inventory) {
	w := c.OutOrStdout()
	fmt.Fprintf(w, "%s %s/%s  root=%s  cwd=%s\n", i.Target.Host, i.Target.Harness, i.Target.Profile, i.Target.Root, i.Target.Cwd)
	if len(i.Servers) == 0 {
		fmt.Fprintln(w, "  no file-configured MCP servers; add notion --notion or add a custom server")
	}
	for _, s := range i.Servers {
		effective := "effective file definition"
		if !s.Effective {
			effective = "shadowed or inactive"
		}
		fmt.Fprintf(w, "  %s  %s  scope=%s  %s  config=%s approval=%s auth=%s health=%s\n    source=%s context=%s\n", s.Name, s.Transport, s.Scope, effective, s.Configuration, s.Approval, s.Auth, s.Health, s.Source, s.Context)
		if !s.CheckedAt.IsZero() {
			fmt.Fprintf(w, "    checked=%s\n", s.CheckedAt.Format(time.RFC3339))
		}
	}
	for _, note := range i.Notes {
		fmt.Fprintln(w, "  "+note)
	}
}
