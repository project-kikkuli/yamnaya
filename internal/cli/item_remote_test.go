package cli

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

type itemRemote struct {
	board core.RemoteBoard
	ran   [][]string
}

func (r *itemRemote) Hosts() []string            { return []string{r.board.Host} }
func (r *itemRemote) Boards() []core.RemoteBoard { return []core.RemoteBoard{r.board} }
func (r *itemRemote) Run(host string, args ...string) (string, error) {
	r.ran = append(r.ran, append([]string{host}, args...))
	return "ran on " + host, nil
}
func (r *itemRemote) Attach(string, ...string) *exec.Cmd { return nil }

func TestItemCommandsOnAnotherHostsItemActOnTheOriginal(t *testing.T) {
	commands := []struct {
		name string
		args []string
		want []string
	}{
		{"show", []string{"show"}, []string{"item", "show", "#788"}},
		{"prepare", []string{"prepare", "--rec", "ship it", "--option", "a=adopt"}, []string{"item", "prepare", "#788", "--rec", "ship it", "--option", "a=adopt"}},
		{"escalate", []string{"escalate"}, []string{"item", "escalate", "#788"}},
		{"answer", []string{"answer", "yes"}, []string{"item", "answer", "#788", "yes"}},
	}
	setups := []struct {
		name  string
		ref   string
		wire  func(*core.Core) *itemRemote
		fail  string
		relay bool
	}{
		{name: "unknown nickname", ref: "@dm6tyx3m6c/#788", wire: func(*core.Core) *itemRemote { return nil }, fail: "host dm6tyx3m6c is not a configured remote"},
		{name: "relay peer without remotes", ref: "box/#788", wire: func(k *core.Core) *itemRemote {
			if _, err := k.ServeSync(core.RelaySync{As: "here", From: "box"}); err != nil {
				t.Fatal(err)
			}
			return nil
		}, relay: true},
		{name: "remote board", ref: "box/#788", wire: func(k *core.Core) *itemRemote {
			r := &itemRemote{board: core.RemoteBoard{Host: "box", Items: []core.Item{{Seq: 788, ID: "D2", Host: "box", Kind: core.KindDecision, State: core.ItemOpen, Question: "merge?"}}}}
			k.Remotes = r
			return r
		}},
	}
	for _, s := range setups {
		for _, c := range commands {
			t.Run(s.name+"/"+c.name, func(t *testing.T) {
				st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { st.Close() })
				k := &core.Core{Store: st, Procs: func() ([]proc.Proc, error) { return nil, nil }}
				r := s.wire(k)
				with := func(run runFunc) func(*cobra.Command, []string) error {
					return func(cmd *cobra.Command, args []string) error { return run(&env{core: k}, cmd, args) }
				}
				cmd := itemCmd(with, with)
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&out)
				cmd.SetArgs(append([]string{c.args[0], s.ref}, c.args[1:]...))
				err = cmd.Execute()
				pending, perr := st.PendingRelays()
				if perr != nil {
					t.Fatal(perr)
				}
				switch {
				case s.fail != "":
					if err == nil || !strings.Contains(err.Error(), s.fail) {
						t.Fatalf("error %v, want it to say %q", err, s.fail)
					}
					if len(pending) != 0 {
						t.Fatalf("refused ref queued relays: %+v", pending)
					}
				case s.relay && c.name == "show":
					if err == nil || !strings.Contains(err.Error(), "host box is not a configured remote") {
						t.Fatalf("show through relays only: %v", err)
					}
				case s.relay:
					if err != nil {
						t.Fatalf("%s: %v, output %s", c.name, err, out.String())
					}
					var y struct{ Args []string }
					if len(pending) != 1 || pending[0].Dest != "box" || json.Unmarshal([]byte(pending[0].Body), &y) != nil || !reflect.DeepEqual(y.Args, c.want) {
						t.Fatalf("relays %+v, want one to box carrying %q", pending, c.want)
					}
				default:
					if err != nil {
						t.Fatalf("%s: %v, output %s", c.name, err, out.String())
					}
					if want := [][]string{append([]string{"box"}, c.want...)}; !reflect.DeepEqual(r.ran, want) {
						t.Fatalf("ran %q, want %q", r.ran, want)
					}
				}
				if items, err := st.Items(core.ItemFilter{}); err != nil || len(items) != 0 {
					t.Fatalf("another host's item touched this host's store: %+v, %v", items, err)
				}
			})
		}
	}
}
