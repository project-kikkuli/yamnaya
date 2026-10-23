package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func TestSendQueuesOnlyForTheSelectedFullThread(t *testing.T) {
	ids := []string{"01a16a70-a4c4-7670-a0a2-e092c1f5e145", "01a16a70-a4c4-7670-a0a2-e0a21f96395f"}
	for _, c := range []struct{ ref, want string }{
		{"01a16a70", ""}, {core.Short(ids[0]), ids[0]}, {core.Short(ids[1]), ids[1]}, {ids[0], ids[0]}, {ids[1], ids[1]},
	} {
		t.Run(c.ref, func(t *testing.T) {
			base := t.TempDir()
			st, err := core.OpenStore(filepath.Join(base, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			k := &core.Core{Store: st, Procs: func() ([]proc.Proc, error) { return nil, nil }}
			l := testLauncher(t, base)
			l.core = k
			for i, id := range ids {
				rec := launch.Record{Key: []string{"first", "second"}[i], Harness: "codex", Session: id}
				if err := l.records.Save(rec); err != nil {
					t.Fatal(err)
				}
			}
			k.Launches = l
			with := func(run runFunc) func(*cobra.Command, []string) error {
				return func(cmd *cobra.Command, args []string) error { return run(&env{core: k}, cmd, args) }
			}
			cmd := sendCmd(with)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{c.ref, "Codex: keep this thread separate"})
			err = cmd.Execute()
			if c.want == "" && !errors.Is(err, core.ErrAmbiguous) || c.want != "" && err != nil {
				t.Fatalf("send: %v", err)
			}
			for _, id := range ids {
				messages, err := st.Messages(id)
				want := 0
				if id == c.want {
					want = 1
				}
				if err != nil || len(messages) != want || want == 1 && messages[0].Session != id {
					t.Fatalf("%s messages = %+v, %v, want %d", id, messages, err, want)
				}
			}
			if _, err := l.find("01a16a70"); !errors.Is(err, core.ErrAmbiguous) {
				t.Fatalf("revive lookup selected ambiguous launch: %v", err)
			}
		})
	}
}

func TestOpeningASharedPrefixRefusesToPickTheFirstSession(t *testing.T) {
	ids := []string{"01a16a70-a4c4-7670-a0a2-e092c1f5e145", "01a16a70-a4c4-7670-a0a2-e0a21f96395f"}
	b := core.Board{}
	for _, id := range ids {
		b.Sessions = append(b.Sessions, core.Live{Session: core.Session{ID: id, Harness: "codex"}})
	}
	if _, err := sessionByPrefix(b, "01a16a70"); !errors.Is(err, core.ErrAmbiguous) {
		t.Fatalf("open selected ambiguous session: %v", err)
	}
	for _, id := range ids {
		if s, err := sessionByPrefix(b, core.Short(id)); err != nil || s.ID != id {
			t.Fatalf("open selected %+v, %v, want %s", s, err, id)
		}
	}
}

type namedSessions struct {
	agent
	ids []string
}

func (a namedSessions) SessionID(p proc.Proc) string { return a.ids[p.PID-10] }
func (a namedSessions) Discover(procs []proc.Proc) []core.Session {
	var out []core.Session
	for _, p := range procs {
		out = append(out, core.Session{Harness: a.Name(), ID: a.SessionID(p), PID: p.PID, TTY: p.TTY})
	}
	return out
}

func TestSpawnStoresFullIDsAndRejectsAmbiguousChildren(t *testing.T) {
	ids := []string{"01a16a70-a4c4-7670-a0a2-e092c1f5e145", "01a16a70-a4c4-7670-a0a2-e0a21f96395f"}
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	k := &core.Core{Store: st, Harnesses: []core.Harness{namedSessions{ids: ids}}, Procs: func() ([]proc.Proc, error) {
		return []proc.Proc{{PID: 10, TTY: "/dev/t1", Args: []string{"claude"}}, {PID: 11, TTY: "/dev/t2", Args: []string{"claude"}}}, nil
	}}
	for _, ref := range []string{"01a16a70", core.Short(ids[0])} {
		with := func(run runFunc) func(*cobra.Command, []string) error {
			return func(cmd *cobra.Command, args []string) error { return run(&env{core: k}, cmd, args) }
		}
		cmd := linkCmd(with)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{ref, "--parent", core.Short(ids[1])})
		err := cmd.Execute()
		if ref == "01a16a70" && !errors.Is(err, core.ErrAmbiguous) || ref != "01a16a70" && err != nil {
			t.Fatalf("spawn %s: %v", ref, err)
		}
		lin, err := st.Lineage()
		if err != nil {
			t.Fatal(err)
		}
		if ref == "01a16a70" && len(lin.Parent) != 0 || ref != "01a16a70" && lin.Parent[ids[0]] != ids[1] {
			t.Fatalf("spawn lineage = %+v", lin.Parent)
		}
	}
}

func TestCodexToolShellAddsAndReadsItemsWithoutSessionOverride(t *testing.T) {
	const id = "01a16a70-a4c4-7670-a0a2-e092c1f5e145"
	t.Setenv("CODEX_THREAD_ID", id)
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	k := &core.Core{Store: st, Procs: func() ([]proc.Proc, error) {
		return []proc.Proc{{PID: os.Getpid(), PPID: 1, Args: []string{"sh"}}}, nil
	}}
	with := func(run runFunc) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error { return run(&env{core: k}, cmd, args) }
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := itemCmd(with, with)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("item %v: %v", args, err)
		}
		return out.String()
	}
	run("add", "--kind", "go", "--question", "Codex: build the importer?", "--rec", "build it")
	items, err := st.Items(core.ItemFilter{})
	if err != nil || len(items) != 1 || items[0].Session != id || items[0].Harness != "codex" {
		t.Fatalf("filed items = %+v, %v", items, err)
	}
	if _, err := st.Transition(items[0].Key(), core.ItemAnswered, "Codex: proceed"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMessage("01a16a70-a4c4-7670-a0a2-e0a21f96395f", "Codex: other thread", core.MsgPending); err != nil {
		t.Fatal(err)
	}
	if got := run("inbox"); !strings.Contains(got, "Codex: proceed") || strings.Contains(got, "other thread") {
		t.Fatalf("inbox = %q", got)
	}
	if got := run("inbox"); strings.Contains(got, "Codex: proceed") {
		t.Fatalf("answer delivered twice: %q", got)
	}
}
