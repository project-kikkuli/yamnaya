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

// A host reachable by relay courier is routable; a usage report never makes it so, and the board's hosts line never reports it.
func TestAUsageReportAloneAddsNoHostToRouteToOrReport(t *testing.T) {
	for _, tc := range []struct {
		name   string
		as     string
		usage  bool
		routed bool
	}{
		{"a report with no courier", "", true, false},
		{"a courier that reports", "rajatsmachine", true, true},
		{"the same courier reporting nothing", "rajatsmachine", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := newOrgWorld(t)
			rm, lp := usageWorld(called.clock)
			called.core.Meter, called.core.ShowRemotes = fixedMeter{rm}, true
			req := RelaySync{Entries: []Relay{}, Ack: []int64{}, From: "laptop", As: tc.as}
			rows := 3
			if tc.usage {
				req.Usage = &RelayUsage{Ready: true, At: called.clock, Limits: lp.Limits, Today: lp.Today, Week: lp.Week}
				rows = 6
			}
			if _, err := called.core.ServeSync(req); err != nil {
				t.Fatal(err)
			}
			b, err := called.core.Board()
			if err != nil {
				t.Fatal(err)
			}
			if len(b.Fleet.PerHost) != rows {
				t.Fatalf("%d rows, want %d: %+v", len(b.Fleet.PerHost), rows, b.Fleet.PerHost)
			}
			if len(b.Remotes) != 0 {
				t.Errorf("joined the boards the hosts line reports: %+v", b.Remotes)
			}
			if _, err := called.core.Store.ResolveHost("laptop"); !errors.Is(err, ErrNotFound) {
				t.Errorf("made a host ym resolves for commands: %v", err)
			}
			if _, _, ok := called.core.RemoteRef("laptop/goal1"); ok != tc.routed {
				t.Errorf("routes references to laptop = %v, want %v", ok, tc.routed)
			}
		})
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

func topline(t *testing.T, w *orgWorld) (rows map[string]int, today, week float64) {
	t.Helper()
	b, err := w.core.Board()
	if err != nil {
		t.Fatal(err)
	}
	rows = map[string]int{}
	for _, l := range b.Fleet.PerHost {
		rows[l.Host]++
	}
	return rows, b.Fleet.Today.USD, b.Fleet.Week.USD
}

// The real rename: the laptop relays under its OS name until rajatsmachine calls it as laptop, and its next report goes by that name.
func TestARenamedMachineKeepsOneReport(t *testing.T) {
	sync := func(t *testing.T, caller *orgWorld, net Courier) {
		t.Helper()
		if err := caller.core.SyncRelays([]string{"rajatsmachine"}, net); err != nil {
			t.Fatal(err)
		}
	}
	call := func(t *testing.T, caller *orgWorld, as string) {
		t.Helper()
		if _, err := caller.core.ServeSync(RelaySync{From: "rajatsmachine", As: as, Entries: []Relay{}, Ack: []int64{}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		from string
		// stray is a row an older build left under the OS name.
		stray bool
		as    []string
	}{
		{"named once by a host calling it", osHostName(), false, []string{"laptop"}},
		{"named again and again", osHostName(), false, []string{"laptop", "mac", "laptop"}},
		{"a stray from before reports named a machine clears itself", osHostName(), true, []string{"laptop"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called, caller, net := relayedWorld(t)
			if err := caller.core.Store.setRelaySelf(RelaySelf{}); err != nil {
				t.Fatal(err)
			}
			if tc.stray {
				_, lp := usageWorld(called.clock)
				old := RelayUsage{Ready: true, At: called.clock, Limits: lp.Limits, Today: lp.Today, Week: lp.Week}
				if _, err := called.core.ServeSync(RelaySync{From: tc.from, As: "rajatsmachine", Entries: []Relay{}, Ack: []int64{}, Usage: &old}); err != nil {
					t.Fatal(err)
				}
			} else {
				sync(t, caller, net)
			}
			for _, as := range tc.as {
				call(t, caller, as)
				sync(t, caller, net)
			}
			last := tc.as[len(tc.as)-1]
			rows, today, week := topline(t, called)
			if len(rows) != 2 || rows[""] != 3 || rows[last] != 3 {
				t.Errorf("rows by host %v, want three local and three under %s", rows, last)
			}
			if today != 189 || week != 725 {
				t.Errorf("spend today %v, 7d %v; want each host once", today, week)
			}
			if hosts, _ := called.core.Store.relayedUsageHosts(); !slices.Equal(hosts, []string{last}) {
				t.Errorf("stored reports %v, want one under %s", hosts, last)
			}
		})
	}
}

func TestAnOSHostnameChangeReplacesTheReportLikeAnyRename(t *testing.T) {
	called, _, _ := relayedWorld(t)
	_, lp := usageWorld(called.clock)
	for _, from := range []string{"old-hostname", "new-hostname"} {
		u := RelayUsage{Machine: "m1", Names: []string{from}, Ready: true, At: called.clock, Limits: lp.Limits, Today: lp.Today, Week: lp.Week}
		if _, err := called.core.ServeSync(RelaySync{From: from, As: "rajatsmachine", Entries: []Relay{}, Ack: []int64{}, Usage: &u}); err != nil {
			t.Fatal(err)
		}
	}
	if rows, today, _ := topline(t, called); rows["old-hostname"] != 0 || rows["new-hostname"] != 3 || today != 189 {
		t.Errorf("rows %v, spend %v", rows, today)
	}
}

func TestReportsKeepToTheirOwnMachine(t *testing.T) {
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		// First the row stored, then the report that follows it.
		kept, next RelayUsage
		age        time.Duration
		keptStays  bool
	}{
		{"another machine under its own name stays", RelayUsage{Machine: "m2"}, RelayUsage{Machine: "m1"}, 0, true},
		{"another machine under this one's OS name stays", RelayUsage{Machine: "m2"}, RelayUsage{Machine: "m1", Names: []string{"old"}}, 0, true},
		{"an older peer's row under another name stays while it may still report", RelayUsage{}, RelayUsage{Machine: "m1"}, 6 * 24 * time.Hour, true},
		{"an older peer's renamed row stays too: nothing links it", RelayUsage{}, RelayUsage{}, 0, true},
		{"an older peer's row goes after a week of silence", RelayUsage{}, RelayUsage{}, 7 * 24 * time.Hour, false},
		{"a named machine's row stays through a week of silence", RelayUsage{Machine: "m2"}, RelayUsage{}, 8 * 24 * time.Hour, true},
		{"this machine's row under its OS name goes", RelayUsage{}, RelayUsage{Machine: "m1", Names: []string{"old"}}, 0, false},
		{"this machine's row under another name goes", RelayUsage{Machine: "m1"}, RelayUsage{Machine: "m1"}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			st := w.core.Store
			tc.kept.At = now.Add(-tc.age)
			if err := st.keepRelayedUsage("old", tc.kept, tc.kept.At); err != nil {
				t.Fatal(err)
			}
			if err := st.keepRelayedUsage("new", tc.next, now); err != nil {
				t.Fatal(err)
			}
			hosts, err := st.relayedUsageHosts()
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(hosts, "old") != tc.keptStays || !slices.Contains(hosts, "new") {
				t.Errorf("stored %v, want old kept = %v", hosts, tc.keptStays)
			}
		})
	}
}

func TestANicknameFollowsAMachineThroughARename(t *testing.T) {
	w := newOrgWorld(t)
	st := w.core.Store
	if err := st.keepRelayedUsage("dm6tyx3m6c", RelayUsage{Machine: "m1"}, w.clock); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RenameHost("dm6tyx3m6c", "mac"); err != nil {
		t.Fatal(err)
	}
	if err := st.keepRelayedUsage("laptop", RelayUsage{Machine: "m1"}, w.clock); err != nil {
		t.Fatal(err)
	}
	nicks, err := st.ShownNicknames()
	if err != nil {
		t.Fatal(err)
	}
	if nicks["laptop"] != "mac" || len(nicks) != 1 {
		t.Errorf("nicknames %v, want laptop to keep mac", nicks)
	}
}

// Rename seen from the reading side: rajatsmachine reads the laptop by its new name before the laptop's next report arrives.
func TestAReadOfAMachineBeatsItsReportUnderAnOldName(t *testing.T) {
	called, _, _ := relayedWorld(t)
	_, lp := usageWorld(called.clock)
	old := RelayUsage{Machine: "m1", Ready: true, At: called.clock, Limits: lp.Limits, Today: lp.Today, Week: lp.Week}
	if err := called.core.Store.keepRelayedUsage("dm6tyx3m6c", old, called.clock); err != nil {
		t.Fatal(err)
	}
	read := Fleet{Ready: true, At: called.clock, Limits: lp.Limits, Today: lp.Today, Week: lp.Week}
	called.core.Remotes = &readBoards{boards: []RemoteBoard{{Host: "laptop", Self: "laptop", Machine: "m1", At: called.clock, Usage: &read}}}
	if rows, today, _ := topline(t, called); len(rows) != 2 || rows["laptop"] != 3 || today != 189 {
		t.Errorf("rows %v, spend %v; want the laptop once", rows, today)
	}
}

func TestAReportCountsOnlyWhatStillHolds(t *testing.T) {
	made := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	tokyo := time.FixedZone("JST", 9*3600)
	for _, tc := range []struct {
		name        string
		reported    time.Time
		later       time.Duration
		rows        int
		today, week float64
		quiet       bool
	}{
		{"fresh", made, 0, 6, 189, 725, false},
		{"later the same day", made, 10 * time.Hour, 6, 189, 725, false},
		{"its day has ended", made, 16 * time.Hour, 6, 169, 725, false},
		{"its windows have reset", made, 31 * time.Hour, 3, 169, 725, true},
		{"six days on", made, 6 * 24 * time.Hour, 3, 169, 725, true},
		{"ten days old", made, 10 * 24 * time.Hour, 3, 169, 675, true},
		{"the reporting host's day ended though this host's has not", made.Add(5*time.Hour + 30*time.Minute).In(tokyo), time.Hour, 6, 169, 725, false},
		{"the reporting host's day goes on though this host's ended", made.Add(14*time.Hour + 30*time.Minute).In(tokyo), time.Hour, 6, 189, 725, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called, caller, net := relayedWorld(t)
			caller.clock = tc.reported
			if err := caller.core.SyncRelays([]string{"rajatsmachine"}, net); err != nil {
				t.Fatal(err)
			}
			called.clock = tc.reported.UTC().Add(tc.later)
			b, err := called.core.Board()
			if err != nil {
				t.Fatal(err)
			}
			if len(b.Fleet.PerHost) != tc.rows {
				t.Errorf("%d rows, want %d: %+v", len(b.Fleet.PerHost), tc.rows, b.Fleet.PerHost)
			}
			if b.Fleet.Today.USD != tc.today || b.Fleet.Week.USD != tc.week {
				t.Errorf("spend today %v, 7d %v; want %v, %v", b.Fleet.Today.USD, b.Fleet.Week.USD, tc.today, tc.week)
			}
			if got := len(b.Fleet.Quiet) == 1 && b.Fleet.Quiet[0].Host == "laptop" && b.Fleet.Quiet[0].At.Equal(tc.reported); got != tc.quiet {
				t.Errorf("quiet %+v, want laptop = %v", b.Fleet.Quiet, tc.quiet)
			}
		})
	}
}

// A board kept from an earlier read is the same report by another route.
func TestAStaleReadCountsOnlyWhatStillHolds(t *testing.T) {
	called, _, _ := relayedWorld(t)
	_, lp := usageWorld(called.clock)
	old := called.clock
	read := Fleet{Ready: true, At: old, Limits: lp.Limits, Today: lp.Today, Week: lp.Week}
	called.core.Remotes = &readBoards{boards: []RemoteBoard{{Host: "laptop", Self: "laptop", At: old, Stale: true, Usage: &read}}}
	called.clock = old.Add(10 * 24 * time.Hour)
	b, err := called.core.Board()
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Fleet.PerHost) != 3 || b.Fleet.Today.USD != 169 || b.Fleet.Week.USD != 675 {
		t.Errorf("rows %+v, spend %v, %v", b.Fleet.PerHost, b.Fleet.Today.USD, b.Fleet.Week.USD)
	}
	if len(b.Fleet.Quiet) != 0 {
		t.Errorf("a host this board reads has its own state line, not a quiet one: %+v", b.Fleet.Quiet)
	}
}
