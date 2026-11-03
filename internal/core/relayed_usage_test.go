package core

import (
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"testing"
	"time"
)

type readBoards struct{ boards []RemoteBoard }

func (r *readBoards) Boards() []RemoteBoard { return r.boards }
func (r *readBoards) Hosts() []string {
	var out []string
	for _, b := range r.boards {
		out = append(out, b.Host)
	}
	return out
}
func (r *readBoards) Run(string, ...string) (string, error) {
	return "", errors.New("not reachable in this test")
}
func (r *readBoards) Attach(string, ...string) *exec.Cmd { return nil }

// Today's case: rajatsmachine's default is exhausted on a 14h old report, claude-work is in use with the laptop on the same account.
func usageWorld(now time.Time) (rajatsmachine, laptop Fleet) {
	week := 7 * 24 * time.Hour
	limit := func(harness, id, account, name string, used float64, seen time.Time) Limit {
		return Limit{Harness: harness, Profile: id, Account: account, Name: name, Used: used, Window: week, ResetsAt: now.Add(30 * time.Hour), Seen: seen}
	}
	rajatsmachine = Fleet{Ready: true, At: now, Today: Spend{USD: 169}, Week: Spend{USD: 675}, Limits: []Limit{
		limit("claude", "default", "claude/personal", "7d", 100, now.Add(-14*time.Hour)),
		limit("claude", "claude-work", "claude/work", "7d", 10, now),
		limit("codex", "default", "codex/one", "codex 7d", 24, now),
	}}
	laptop = Fleet{Ready: true, At: now, Today: Spend{USD: 20}, Week: Spend{USD: 50}, Limits: []Limit{
		limit("claude", "default", "claude/laptop", "7d", 31, now),
		limit("claude", "claude-work", "claude/work", "7d", 7, now.Add(-time.Hour)),
		limit("codex", "default", "codex/two", "codex 7d", 12, now),
	}}
	return rajatsmachine, laptop
}

func rowOf(rows []Limit, host, harness, id string) (Limit, bool) {
	i := slices.IndexFunc(rows, func(l Limit) bool { return l.Host == host && l.Harness == harness && l.Profile == id })
	if i < 0 {
		return Limit{}, false
	}
	return rows[i], true
}

// wireNet carries each round trip as ym relay sync does, through JSON.
type wireNet struct{ *hostNet }

func (n wireNet) Sync(host string, req RelaySync) (RelayReply, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return RelayReply{}, err
	}
	var read RelaySync
	if err := json.Unmarshal(raw, &read); err != nil {
		return RelayReply{}, err
	}
	return n.hostNet.Sync(host, read)
}

func relayedWorld(t *testing.T) (*orgWorld, *orgWorld, Courier) {
	t.Helper()
	called, caller := newOrgWorld(t), newOrgWorld(t)
	rm, lp := usageWorld(called.clock)
	called.core.Meter, caller.core.Meter = fixedMeter{rm}, fixedMeter{lp}
	called.core.ShowRemotes = true
	if err := caller.core.Store.setRelaySelf(RelaySelf{Name: "laptop"}); err != nil {
		t.Fatal(err)
	}
	return called, caller, wireNet{&hostNet{hosts: map[string]*orgWorld{"rajatsmachine": called}, down: map[string]bool{}}}
}

func TestARelayedHostsUsageShowsOnTheCalledHostsBoardAndNowhereElse(t *testing.T) {
	called, caller, net := relayedWorld(t)
	if err := caller.core.SyncRelays([]string{"rajatsmachine"}, net); err != nil {
		t.Fatal(err)
	}
	b, err := called.core.Board()
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Fleet.PerHost) != 6 {
		t.Fatalf("called host shows %d rows, want all six: %+v", len(b.Fleet.PerHost), b.Fleet.PerHost)
	}
	for _, want := range []struct {
		host, harness, id string
		used              float64
	}{{"", "claude", "default", 100}, {"", "claude", "claude-work", 10}, {"", "codex", "default", 24},
		{"laptop", "claude", "default", 31}, {"laptop", "claude", "claude-work", 7}, {"laptop", "codex", "default", 12}} {
		if l, ok := rowOf(b.Fleet.PerHost, want.host, want.harness, want.id); !ok || l.Used != want.used {
			t.Errorf("row %+v = %+v, %v", want, l, ok)
		}
	}
	if b.Fleet.Today.USD != 189 || b.Fleet.Week.USD != 725 {
		t.Errorf("spend today %v, 7d %v; want the two hosts' totals, each once", b.Fleet.Today.USD, b.Fleet.Week.USD)
	}
	if err := caller.core.SyncRelays([]string{"rajatsmachine"}, net); err != nil {
		t.Fatal(err)
	}
	again, err := called.core.Board()
	if err != nil {
		t.Fatal(err)
	}
	if again.Fleet.Today.USD != 189 {
		t.Errorf("a second round trip made spend %v", again.Fleet.Today.USD)
	}
	for _, r := range b.Remotes {
		if r.Usage != nil || r.Err != nil || r.UsageOnly {
			t.Errorf("a relayed report changed how the board reads its host: %+v", r)
		}
	}
	if known, _ := called.core.Store.KnownHosts(); len(known) != 0 {
		t.Errorf("a usage-only host became a host ym ssh's to: %+v", known)
	}
	for _, l := range b.Fleet.Limits {
		if l.Host == "laptop" {
			t.Errorf("a relayed row reached failover's limits: %+v", l)
		}
	}
	if len(b.Fleet.Limits) != 3 {
		t.Errorf("Fleet.Limits = %+v, want this host's three", b.Fleet.Limits)
	}
}

func TestAUsageReportAloneAddsNoHostToRouteToOrReport(t *testing.T) {
	called, _, _ := relayedWorld(t)
	_, lp := usageWorld(called.clock)
	usage := RelayUsage{Ready: true, At: called.clock, Limits: lp.Limits, Today: lp.Today, Week: lp.Week}
	if _, err := called.core.ServeSync(RelaySync{Entries: []Relay{}, Ack: []int64{}, From: "laptop", As: "rajatsmachine", Usage: &usage}); err != nil {
		t.Fatal(err)
	}
	if err := called.core.Store.setRelaySelf(RelaySelf{Name: "rajatsmachine"}); err != nil {
		t.Fatal(err)
	}
	b, err := called.core.Board()
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Fleet.PerHost) != 6 {
		t.Fatalf("%d rows, want six: %+v", len(b.Fleet.PerHost), b.Fleet.PerHost)
	}
	if len(b.Remotes) != 0 {
		t.Errorf("a usage report joined the boards the hosts line reports: %+v", b.Remotes)
	}
	if _, err := called.core.Store.ResolveHost("laptop"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a usage report made a host ym resolves for commands: %v", err)
	}
	if _, _, ok := called.core.RemoteRef("laptop/goal1"); ok {
		t.Error("a usage report made a host ym routes references to")
	}
}

func TestASnapshotForOtherHostsCarriesNoRelayedRowsOrSpend(t *testing.T) {
	called, caller, net := relayedWorld(t)
	if err := caller.core.SyncRelays([]string{"rajatsmachine"}, net); err != nil {
		t.Fatal(err)
	}
	called.core.ShowRemotes = false
	b, err := called.core.Board()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rowOf(b.Fleet.PerHost, "laptop", "claude", "default"); ok || b.Fleet.Today.USD != 169 {
		t.Fatalf("ym serve's board would echo the laptop's usage back: %+v, spend %v", b.Fleet.PerHost, b.Fleet.Today.USD)
	}
	for _, l := range b.Fleet.Limits {
		if l.Host != "" {
			t.Fatalf("snapshot limit names another host: %+v", l)
		}
	}
}

func TestReadingAHostBeatsItsRelayedReportUntilItStopsAnswering(t *testing.T) {
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	read := func(mut func(*RemoteBoard)) RemoteBoard {
		f := Fleet{Ready: true, At: now, Today: Spend{USD: 3}, Limits: []Limit{{Harness: "claude", Profile: "default", Account: "claude/laptop", Name: "7d", Used: 55, ResetsAt: now.Add(time.Hour), Seen: now}}}
		r := RemoteBoard{Host: "lap.example", Self: "laptop", At: now.Add(-time.Minute), Usage: &f}
		mut(&r)
		return r
	}
	for _, tc := range []struct {
		name  string
		board RemoteBoard
		used  float64
		host  string
		spend float64
	}{
		{"fresh read wins though the relay arrived later", read(func(*RemoteBoard) {}), 55, "lap.example", 169 + 3},
		{"read by the host's own name wins", read(func(r *RemoteBoard) { r.Host = "laptop" }), 55, "laptop", 169 + 3},
		{"a stale read yields to a later relay", read(func(r *RemoteBoard) { r.Stale = true }), 31, "lap.example", 169 + 20},
		{"a failing read yields to a later relay", read(func(r *RemoteBoard) { r.Err = errors.New("timed out") }), 31, "lap.example", 169 + 20},
		{"a stale read newer than the relay stays", read(func(r *RemoteBoard) { r.Stale, r.At = true, now.Add(time.Hour) }), 55, "lap.example", 169 + 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called, caller, net := relayedWorld(t)
			called.clock, caller.clock = now, now
			called.core.Remotes = &readBoards{boards: []RemoteBoard{tc.board}}
			if err := caller.core.SyncRelays([]string{"rajatsmachine"}, net); err != nil {
				t.Fatal(err)
			}
			b, err := called.core.Board()
			if err != nil {
				t.Fatal(err)
			}
			l, ok := rowOf(b.Fleet.PerHost, tc.host, "claude", "default")
			if !ok || l.Used != tc.used {
				t.Fatalf("row %+v %v in %+v, want %v from %s", l, ok, b.Fleet.PerHost, tc.used, tc.host)
			}
			n := 0
			for _, r := range b.Fleet.PerHost {
				if r.Host != "" && r.Harness == "claude" && r.Profile == "default" {
					n++
				}
			}
			if n != 1 || b.Fleet.Today.USD != tc.spend {
				t.Fatalf("%d laptop default rows, spend %v, want one row and %v", n, b.Fleet.Today.USD, tc.spend)
			}
		})
	}
}

func TestPeersOnAnOlderYmIgnoreRelayedUsage(t *testing.T) {
	called, caller, net := relayedWorld(t)
	if err := caller.core.SyncRelays([]string{"rajatsmachine"}, net); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(RelaySync{From: "laptop", As: "rajatsmachine", Peers: []string{}, Entries: []Relay{}, Ack: []int64{}, Usage: &RelayUsage{Ready: true}})
	if err != nil {
		t.Fatal(err)
	}
	var older struct {
		From, As string
		Entries  []Relay `json:"entries"`
	}
	if err := json.Unmarshal(raw, &older); err != nil || older.From != "laptop" {
		t.Fatalf("an older receiver could not read the request: %v, %+v", err, older)
	}
	// An older sender's request has no usage: the host still answers and what it last reported stays.
	if _, err := called.core.ServeSync(RelaySync{From: "laptop", As: "rajatsmachine", Entries: []Relay{}, Ack: []int64{}}); err != nil {
		t.Fatal(err)
	}
	b, err := called.core.Board()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rowOf(b.Fleet.PerHost, "laptop", "codex", "default"); !ok {
		t.Fatalf("a request without usage lost the host's rows: %+v", b.Fleet.PerHost)
	}
	silent, err := called.core.ServeSync(RelaySync{From: "other", As: "rajatsmachine", Entries: []Relay{}, Ack: []int64{}})
	if err != nil || silent.Entries == nil {
		t.Fatalf("older sender's round trip: %+v, %v", silent, err)
	}
	if hosts, _ := called.core.Store.relayedUsageHosts(); slices.Contains(hosts, "other") {
		t.Errorf("a host that reported nothing is listed: %v", hosts)
	}
}

func TestAUsageOnlyHostCanBeNamedAndForgotten(t *testing.T) {
	called, caller, net := relayedWorld(t)
	if err := caller.core.SyncRelays([]string{"rajatsmachine"}, net); err != nil {
		t.Fatal(err)
	}
	st := called.core.Store
	host, err := st.ResolveShownHost("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if nick, err := st.RenameHost(host, "mac"); err != nil || nick != "mac" {
		t.Fatalf("rename = %q, %v", nick, err)
	}
	if _, err := st.ResolveHost("mac"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a usage-only host resolved as a host ym routes commands to: %v", err)
	}
	if known, err := st.ForgetHost("laptop"); err != nil || !known {
		t.Fatalf("forget = %v, %v", known, err)
	}
	b, err := called.core.Board()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rowOf(b.Fleet.PerHost, "laptop", "claude", "default"); ok {
		t.Errorf("forgotten host still shows: %+v", b.Fleet.PerHost)
	}
}

func TestPerHostKeepsEveryReportInBoardOrderAndLimitsStayFolded(t *testing.T) {
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	local, lap := usageWorld(now)
	notReady := Fleet{Ready: false, At: now, Limits: []Limit{{Harness: "codex", Profile: "default", Account: "codex/three", Name: "codex 7d", Used: 2, ResetsAt: now.Add(time.Hour), Seen: now}}}
	merged := MergeFleet(local, []RemoteBoard{{Host: "zed", Usage: &notReady}, {Host: "box", Self: "box", Usage: &lap}, {Host: "alias", Self: "box", Usage: &lap}})
	var got []string
	for _, l := range merged.PerHost {
		got = append(got, l.Host+"|"+l.Harness+"|"+l.Profile)
	}
	want := []string{"|claude|default", "|claude|claude-work", "|codex|default", "box|claude|default", "box|claude|claude-work", "box|codex|default", "zed|codex|default"}
	if !slices.Equal(got, want) {
		t.Errorf("PerHost order\n got %q\nwant %q", got, want)
	}
	work := 0
	for _, l := range merged.Limits {
		if l.Account == "claude/work" {
			work++
			if l.Used != 10 || l.Host != "" {
				t.Errorf("fold kept %+v, want the newest report of the account", l)
			}
		}
		if l.Host == "zed" {
			t.Errorf("a remote still scanning joined Limits: %+v", l)
		}
	}
	if work != 1 {
		t.Errorf("Limits holds %d rows for one account", work)
	}
}
