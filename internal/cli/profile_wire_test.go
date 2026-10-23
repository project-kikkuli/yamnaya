package cli

import (
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

type profileRemote struct{ calls []string }

func (r *profileRemote) Hosts() []string            { return []string{"user@owner-host"} }
func (r *profileRemote) Boards() []core.RemoteBoard { return nil }
func (r *profileRemote) Run(host string, args ...string) (string, error) {
	r.calls = append([]string{host}, args...)
	return "switched on owner host", nil
}
func (r *profileRemote) Attach(string, ...string) *exec.Cmd { return nil }

func TestRemoteSwitchResolvesProfileOnOwningHost(t *testing.T) {
	for _, h := range []string{"claude", "codex"} {
		r := &profileRemote{}
		l := launcher{core: &core.Core{Remotes: r}}
		ref := h + ":work@" + profileConversation
		if _, err := l.SwitchProfile("user@owner-host/"+ref, "personal"); err != nil {
			t.Fatal(err)
		}
		want := []string{"user@owner-host", "switch", ref, "--profile", "personal"}
		if !reflect.DeepEqual(r.calls, want) {
			t.Fatalf("remote control lost scope or forwarded a root: %q", r.calls)
		}
	}
}

func TestRemoteProfileIdentityAndSharedSubscriptionSnapshots(t *testing.T) {
	now := time.Now()
	account := "claude/principal/workspace"
	local := core.Fleet{Ready: true, At: now, Today: core.Spend{USD: 1}, Week: core.Spend{USD: 2}, Sessions: map[string]core.SessionSpend{}, Limits: []core.Limit{{Harness: "claude", Profile: "local-id", ProfileLabel: "same label", Account: account, Name: "5h", Used: 70, Seen: now}}}
	remote := core.Fleet{Ready: true, At: now, Today: core.Spend{USD: 3}, Week: core.Spend{USD: 4}, Sessions: map[string]core.SessionSpend{"session": {Harness: "claude", Profile: "remote-id", ID: "session", Account: account}}, Limits: []core.Limit{{Harness: "claude", Profile: "remote-id", ProfileLabel: "same label", Account: account, Name: "5h", Used: 90, Seen: now.Add(time.Second)}, {Harness: "claude", Profile: "other-id", ProfileLabel: "same label", Account: "claude/other/workspace", Name: "5h", Used: 5, Seen: now}}}
	wire := boardJSON{Self: "physical-host", Usage: usageWire(remote), Profiles: []core.LoginProfile{{Harness: "claude", ID: "remote-id", Label: "same label", Account: account}}, Sessions: []sessionJSON{{Harness: "claude", ID: "session", Profile: "remote-id", Account: account}}}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"config_root", "secure_storage", "id_token", "auth.json"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("remote allowlist leaked %s", secret)
		}
	}
	var decoded boardJSON
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	r := remoteBoard("alice@host-a", decoded)
	alias := remoteBoard("host-a.example.test", decoded)
	merged := core.MergeFleet(local, []core.RemoteBoard{r, alias})
	if merged.Today.USD != 4 || merged.Week.USD != 6 {
		t.Fatalf("host alias duplicated spend: %#v", merged)
	}
	if len(merged.Limits) != 2 {
		t.Fatalf("subscription aliases confused with labels: %#v", merged.Limits)
	}
	for _, limit := range merged.Limits {
		if limit.Account == account && (limit.Used != 90 || limit.Host != "alice@host-a") {
			t.Fatalf("account-global percentage was summed or stale: %#v", limit)
		}
	}
	if r.Profiles[0].Host != "alice@host-a" || r.Sessions[0].Profile != "remote-id" || r.Sessions[0].ConfigRoot != "" {
		t.Fatalf("remote identity lost host scope: %#v", r)
	}
	if remoteBoard("old", boardJSON{}).Usage != nil {
		t.Fatal("old snapshot fabricated zero available usage")
	}
}
