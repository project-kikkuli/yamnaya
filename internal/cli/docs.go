package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/intake"
	"github.com/spf13/cobra"
)

// notionConfig is how ym reads Notion: an internal integration's token and the owner's Notion user.
type notionConfig struct {
	Token     string `json:"token"`
	Owner     string `json:"owner"`
	OwnerName string `json:"owner_name"`
}

const notionFile = "notion.json"

// loadNotion reads the saved connection; NOTION_TOKEN and NOTION_OWNER_ID override it.
func loadNotion(dir string) notionConfig {
	var cfg notionConfig
	if raw, err := os.ReadFile(filepath.Join(dir, notionFile)); err == nil {
		_ = json.Unmarshal(raw, &cfg)
	}
	if t := os.Getenv("NOTION_TOKEN"); t != "" {
		cfg.Token = t
	}
	for _, k := range []string{"NOTION_REVIEWER_ID", "NOTION_OWNER_ID"} {
		if id := os.Getenv(k); id != "" {
			cfg.Owner = id
		}
	}
	return cfg
}

func (c notionConfig) ready() bool { return c.Token != "" && c.Owner != "" }

// connectNotion finds the owner among the people token can see by email and saves the connection in dir.
func connectNotion(ctx context.Context, n *intake.Notion, email, dir string) (notionConfig, error) {
	people, err := n.People(ctx)
	if err != nil {
		return notionConfig{}, err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	cfg := notionConfig{Token: n.Token}
	for id, who := range people {
		mail, name, _ := strings.Cut(who, "\t")
		if mail == email {
			cfg.Owner, cfg.OwnerName = id, name
		}
	}
	if cfg.Owner == "" {
		return cfg, fmt.Errorf("no Notion person with email %s is visible to this integration; give it the capability to read user information including email addresses", email)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	if err = os.WriteFile(filepath.Join(dir, notionFile+".new"), raw, 0o600); err != nil {
		return cfg, err
	}
	return cfg, os.Rename(filepath.Join(dir, notionFile+".new"), filepath.Join(dir, notionFile))
}

// docSweeper sweeps Notion for asks waiting on the owner, one full sweep at a time, starting a sweep at most once per every.
type docSweeper struct {
	dir    string
	every  time.Duration
	pace   time.Duration
	notion *intake.Notion
	api    string
}

func (d *docSweeper) sweep(ctx context.Context, st *core.Store) error {
	cfg := loadNotion(d.dir)
	if !cfg.ready() {
		return nil
	}
	if d.notion == nil || d.notion.Token != cfg.Token || d.notion.Owner != cfg.Owner {
		d.notion = intake.NewNotion(cfg.Token, cfg.Owner)
		d.notion.Pace = d.pace
		if d.api != "" {
			d.notion.URL = d.api
		}
	}
	keep, err := st.OpenDocPages("notion")
	if err != nil {
		return err
	}
	found, pages, err := d.notion.Asks(ctx, keep)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return st.FailDocSweep("notion", err)
	}
	asks := make([]core.DocAsk, len(found))
	for i, a := range found {
		asks[i] = core.DocAsk{Key: a.Key, Kind: a.Kind, URL: a.URL, Page: a.Page, Title: a.Title, Asker: a.Asker, Text: a.Text,
			Evidence: a.Evidence, Version: a.Version, Refs: a.Refs}
	}
	return st.SyncDocAsks("notion", asks, pages)
}

func (d *docSweeper) run(ctx context.Context, st *core.Store, log io.Writer) {
	for {
		started := time.Now()
		if err := d.sweep(ctx, st); err != nil {
			fmt.Fprintln(log, "doc sweep:", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(max(0, d.every-time.Since(started))):
		}
	}
}

// docSweepErrors are the failures of each source's last sweep.
func docSweepErrors(st *core.Store) error {
	sweeps, err := st.DocSweeps()
	if err != nil {
		return err
	}
	var out []error
	for _, s := range sweeps {
		if s.Error != "" {
			out = append(out, fmt.Errorf("%s sweep: %s", s.Source, s.Error))
		}
	}
	return errors.Join(out...)
}

func docsCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var asJSON bool
	root := &cobra.Command{Use: "docs", Short: "List what people ask of you in Notion, and where each ask went", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		dir, err := stateDir()
		if err != nil {
			return err
		}
		asks, err := e.core.Store.OpenDocAsks()
		if err != nil {
			return err
		}
		if asJSON {
			if asks == nil {
				asks = []core.DocAsk{}
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(asks)
		}
		out := cmd.OutOrStdout()
		cfg := loadNotion(dir)
		sweeps, err := e.core.Store.DocSweeps()
		if err != nil {
			return err
		}
		switch {
		case !cfg.ready():
			fmt.Fprintln(out, "Notion: not connected. Run: ym docs connect <your Notion email>")
		case len(sweeps) == 0:
			fmt.Fprintf(out, "Notion: connected as %s; the first sweep has not finished\n", cfg.OwnerName)
		default:
			for _, s := range sweeps {
				fmt.Fprintf(out, "%s: last sweep %s, %d pages", s.Source, s.At.Local().Format("Jan 2 15:04"), s.Pages)
				if s.Error != "" {
					fmt.Fprintf(out, ", failed: %s", s.Error)
				}
				fmt.Fprintln(out)
			}
		}
		for _, a := range asks {
			to := "you"
			if a.Recipient != core.Owner {
				to = a.Recipient
			}
			fmt.Fprintf(out, "\n%-8s %-7s %s on %q\n  %s: %s\n  %s\n", to, a.Kind, a.Source, a.Title, a.Asker, a.Text, a.URL)
		}
		return nil
	})}
	root.Flags().BoolVar(&asJSON, "json", false, "print the open asks as JSON")
	root.AddCommand(&cobra.Command{Use: "connect <your Notion email>", Short: "Connect a Notion internal integration token; reads the token from stdin",
		Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := stateDir()
			if err != nil {
				return err
			}
			var token string
			if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(f.Fd()) {
				fmt.Fprint(cmd.ErrOrStderr(), "Notion integration token: ")
				raw, err := term.ReadPassword(f.Fd())
				fmt.Fprintln(cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				token = string(raw)
			} else {
				line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if err != nil && line == "" {
					return fmt.Errorf("read the token from stdin: %w", err)
				}
				token = line
			}
			token = strings.TrimSpace(token)
			if token == "" {
				return fmt.Errorf("no token given")
			}
			n := intake.NewNotion(token, "")
			if api := os.Getenv("YAMNAYA_NOTION_API"); api != "" {
				n.URL = api
			}
			cfg, err := connectNotion(cmd.Context(), n, args[0], dir)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Notion connected as %s. ym serve sweeps the pages shared with the integration within a few minutes.\n", cfg.OwnerName)
			return nil
		}})
	return root
}
