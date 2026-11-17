package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/spf13/cobra"
)

func TestInboxPreservesMessageSourcesInTextAndJSON(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[asJSON], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "ym.db")
			st, err := core.OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if _, err = st.AddMessage("reader", "inspect the failure", core.MsgPending); err != nil {
				t.Fatal(err)
			}
			if _, err = st.AddSystemMessage("reader", "remote command completed"); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`INSERT INTO ym_messages(session,text,state,created_at,source) VALUES('reader','old unattributed content','pending',?,'legacy')`, time.Now().UTC().Format(time.RFC3339Nano))
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			k := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
			with := func(run runFunc) func(*cobra.Command, []string) error {
				return func(cmd *cobra.Command, args []string) error { return run(&env{core: k, session: "reader"}, cmd, args) }
			}
			read := func() string {
				t.Helper()
				cmd := itemInboxCmd(with)
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&out)
				if asJSON {
					cmd.SetArgs([]string{"--json"})
				} else {
					cmd.SetArgs(nil)
				}
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
				return out.String()
			}
			output := read()
			if asJSON {
				var result struct {
					Messages []inboxMessageJSON `json:"messages"`
				}
				if err = json.Unmarshal([]byte(output), &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Messages) != 3 {
					t.Fatalf("JSON omitted message results: %s", output)
				}
				for n, source := range []string{"owner", "system", "legacy"} {
					if result.Messages[n].Source != source || result.Messages[n].State != core.MsgInbox {
						t.Fatalf("JSON changed source/delivery: %+v", result.Messages)
					}
				}
			} else {
				for _, want := range []string{"msg: [owner] inspect the failure", "msg: [system tool result] remote command completed", "msg: [legacy, source unknown] old unattributed content"} {
					if !strings.Contains(output, want) {
						t.Fatalf("inbox omitted source %q: %s", want, output)
					}
				}
			}
			second := read()
			for _, body := range []string{"inspect the failure", "remote command completed", "old unattributed content"} {
				if strings.Contains(second, body) {
					t.Fatalf("received result repeated: %s", second)
				}
			}
		})
	}
}
