package core

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestChiefInboxFollowsItemsAcrossHosts(t *testing.T) {
	for _, hosts := range []struct{ chief, filer string }{{"a", "b"}, {"b", "a"}, {"b", "c"}} {
		for _, route := range []string{"chief", "owner", "escalated"} {
			t.Run(hosts.chief+"/"+hosts.filer+"/"+route, func(t *testing.T) {
				f := newFleet(t)
				f.a.core.Remotes = &fakeRemotes{boards: []RemoteBoard{{Host: "b"}, {Host: "c"}}}
				on := map[string]*orgWorld{"a": f.a, "b": f.b, "c": f.c}
				cw, fw := on[hosts.chief], on[hosts.filer]
				chief := cw.chief(t, "platform")
				_, mgr := fw.goal(t, "ship", "/repo/app")
				if err := fw.core.Attach(mgr.ID, RemoteKey(hosts.chief, chief.ID)); err != nil {
					t.Fatal(err)
				}
				it := fw.ask(t, mgr.Session, "ship the API?")
				if route == "owner" {
					if err := fw.core.Store.SetRecipient(it.Seq, Owner); err != nil {
						t.Fatal(err)
					}
				} else if route == "escalated" {
					if _, err := fw.core.Escalate(it.Key()); err != nil {
						t.Fatal(err)
					}
				}
				for range 3 {
					f.sync(t)
				}
				key := RemoteKey(hosts.filer, it.Key())
				mine, err := cw.core.ToMe(cw.board(t), chief)
				if err != nil || len(mine) != 1 || mine[0].Key() != key {
					t.Fatalf("chief inbox = %+v, %v; want %s", mine, err, key)
				}
				asks, err := cw.core.NewAsks(chief.Session)
				if err != nil || len(asks) != 1 || asks[0].Key() != key {
					t.Fatalf("new asks = %+v, %v", asks, err)
				}
				if asks, err := cw.core.NewAsks(chief.Session); err != nil || len(asks) != 0 {
					t.Fatalf("repeated inbox = %+v, %v", asks, err)
				}
				for _, ref := range []string{key, RemoteKey(hosts.filer, it.Qualified()), it.Qualified()} {
					found, err := cw.core.FindItem("", ref)
					if err != nil || found.Key() != key || found.Rec != it.Rec || len(found.History) == 0 {
						t.Fatalf("show %s = %+v, %v", ref, found, err)
					}
				}
				if _, err := fw.core.Store.Transition(it.Key(), ItemAnswered, "ship"); err != nil {
					t.Fatal(err)
				}
				for range 3 {
					f.sync(t)
				}
				found, err := cw.core.FindItem("", key)
				wantHistory := 2
				if route == "escalated" {
					wantHistory++
				}
				if err != nil || found.State != ItemAnswered || found.Answer != "ship" || len(found.History) != wantHistory {
					t.Fatalf("answered item = %+v, %v", found, err)
				}
				if mine, err := cw.core.ToMe(cw.board(t), chief); err != nil || len(mine) != 0 {
					t.Fatalf("answered item still in inbox: %+v, %v", mine, err)
				}
			})
		}
	}
}

func TestRemoteItemActionsChangeTheFilingHostAndReachTheFiler(t *testing.T) {
	for _, action := range []string{"answer", "prepare", "escalate"} {
		t.Run(action, func(t *testing.T) {
			f := newFleet(t)
			chief := f.b.chief(t, "platform")
			_, mgr := f.c.goal(t, "ship", "/repo/app")
			if err := f.c.core.Attach(mgr.ID, RemoteKey("b", chief.ID)); err != nil {
				t.Fatal(err)
			}
			it := f.c.ask(t, mgr.Session, "ship the API?")
			for range 3 {
				f.sync(t)
			}
			f.c.core.RunSelf = func(args ...string) (string, error) {
				if len(args) < 3 || !reflect.DeepEqual(args[:3], []string{"item", action, it.Key()}) {
					return "", fmt.Errorf("wrong item command: %q", args)
				}
				switch action {
				case "answer":
					_, _, err := f.c.core.Answer(f.c.board(t), args[2], args[3])
					return "answered", err
				case "prepare":
					_, err := f.c.core.Prepare(f.c.board(t), args[2], Prepared{Rec: args[4]})
					return "prepared", err
				default:
					_, err := f.c.core.Escalate(args[2])
					return "escalated", err
				}
			}
			key := RemoteKey("c", it.Key())
			var err error
			switch action {
			case "answer":
				_, _, err = f.b.core.Answer(f.b.board(t), key, "ship it")
			case "prepare":
				_, err = f.b.core.Prepare(f.b.board(t), key, Prepared{Rec: "ship with evidence"})
			default:
				_, err = f.b.core.Escalate(key)
			}
			if err != nil {
				t.Fatal(err)
			}
			for range 3 {
				f.sync(t)
			}
			changed, err := f.c.core.Store.Item(it.Key())
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "answer":
				if changed.Answer != "ship it" || !strings.Contains(lastHanded(f.c, mgr.Session), "ship it") {
					t.Fatalf("answer did not reach filer: %+v, handed %q", changed, lastHanded(f.c, mgr.Session))
				}
			case "prepare":
				if changed.Rec != "ship with evidence" {
					t.Fatalf("preparation = %+v", changed)
				}
			case "escalate":
				if changed.Recipient != Owner {
					t.Fatalf("escalation = %+v", changed)
				}
			}
			if own, err := f.b.core.Store.Items(ItemFilter{}); err != nil || len(own) != 0 {
				t.Fatalf("chief stored a second authoritative item: %+v, %v", own, err)
			}
		})
	}
}

func TestItemRefsKeepHostLocalSequencesDistinct(t *testing.T) {
	f := newFleet(t)
	localChief := f.b.chief(t, "local")
	remoteChief := f.c.chief(t, "remote")
	local := f.b.ask(t, localChief.Session, "local question?")
	remote := f.c.ask(t, remoteChief.Session, "remote question?")
	for range 3 {
		f.sync(t)
	}
	for _, tc := range []struct {
		ref, key  string
		ambiguous bool
	}{
		{local.Key(), local.Key(), false},
		{RemoteKey("b", local.Qualified()), local.Key(), false},
		{RemoteKey("c", remote.Qualified()), RemoteKey("c", remote.Key()), false},
		{local.Qualified(), "", true},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			it, err := f.b.core.FindItem("", tc.ref)
			if tc.ambiguous {
				if err == nil || !strings.Contains(err.Error(), "ambiguous") {
					t.Fatalf("expected host ambiguity, got %+v, %v", it, err)
				}
			} else if err != nil || it.Key() != tc.key {
				t.Fatalf("got %+v, %v; want %s", it, err, tc.key)
			}
		})
	}
	for _, missing := range []string{"#99999", "#0", "#-1", "#bad", "c/#0", "mgr1:", "c/mgr1:"} {
		if it, err := f.b.core.FindItem("", missing); err == nil {
			t.Fatalf("invalid or missing sequence %s resolved as %+v", missing, it)
		}
	}
}

func TestOwnerEscalationKeepsTheWholeChiefPathAcrossHosts(t *testing.T) {
	for _, mode := range []string{"remote chief", "remote worker", "nested remote worker", "continued remote worker"} {
		t.Run(mode, func(t *testing.T) {
			f, repo := remoteWorkFleet(t)
			chief := f.a.chief(t, "parent")
			var fw *orgWorld
			var session string
			if mode == "remote chief" {
				middle := f.c.chief(t, "middle")
				if err := f.c.core.Attach(middle.ID, RemoteKey("a", chief.ID)); err != nil {
					t.Fatal(err)
				}
				_, mgr := f.b.goal(t, "ship", "/repo/app")
				if err := f.b.core.Attach(mgr.ID, RemoteKey("c", middle.ID)); err != nil {
					t.Fatal(err)
				}
				fw, session = f.b, mgr.Session
			} else {
				_, mgr := f.a.goal(t, "remote work", "/repo/app")
				if err := f.a.core.Attach(mgr.ID, chief.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.a.core.StartNew(NewLaunch{Parent: mgr.Session, Repo: repo, Prompt: "work", Session: "worker", Source: "root"}); err != nil {
					t.Fatal(err)
				}
				syncWork(t, f)
				fw, session = f.b, "worker"
				if mode == "nested remote worker" {
					session = "nested"
					fw.spawn(session, "worker")
				}
			}
			it := fw.ask(t, session, "owner must decide?")
			if _, err := fw.core.Escalate(it.Key()); err != nil {
				t.Fatal(err)
			}
			if mode == "continued remote worker" {
				fw.spawn("worker-new", "")
				fw.claude.next = map[string]string{session: "worker-new"}
			}
			for range 3 {
				f.sync(t)
			}
			mine, err := f.a.core.ToMe(f.a.board(t), chief)
			if err != nil || len(mine) != 1 || mine[0].Key() != RemoteKey("b", it.Key()) {
				t.Fatalf("parent cannot see owner escalation: %+v, %v", mine, err)
			}
		})
	}
}

func TestAMachineItemFiledOnAHostWithARemoteChiefWaitsOnTheOwnerEverywhere(t *testing.T) {
	f := newFleet(t)
	f.a.core.Remotes = &fakeRemotes{boards: []RemoteBoard{{Host: "b"}}}
	chief := f.a.chief(t, "platform")
	_, mgr := f.b.goal(t, "ship", "/repo/app")
	if err := f.b.core.Attach(mgr.ID, RemoteKey("a", chief.ID)); err != nil {
		t.Fatal(err)
	}
	it, err := f.b.core.AddOwnerItem(Item{Kind: KindChore, DoLine: "Do it", Session: "ym", Question: "disk low", Rec: "remove something"})
	if err != nil {
		t.Fatal(err)
	}
	if it.Recipient != Owner {
		t.Fatalf("recipient = %q, want the owner", it.Recipient)
	}
	isIt := func(n Need) bool { return n.Item != nil && n.Item.Question == "disk low" }
	if b := f.b.board(t); !slices.ContainsFunc(b.Needs, isIt) {
		t.Fatalf("the filing host's board does not ask the owner: %+v", b.Needs)
	}
	for range 3 {
		f.sync(t)
	}
	if mine, err := f.a.core.ToMe(f.a.board(t), chief); err != nil || len(mine) != 0 {
		t.Fatalf("the chief got the machine's item: %+v, %v", mine, err)
	}
	f.a.core.ShowRemotes = true
	if b := f.a.board(t); !slices.ContainsFunc(b.Needs, isIt) {
		t.Fatalf("the chief's host's board does not ask the owner: %+v", b.Needs)
	}
}

func snapshotHosts(t *testing.T, w *orgWorld) []string {
	t.Helper()
	snapshots, err := w.core.Store.itemSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	return slices.Sorted(maps.Keys(snapshots))
}

// The real rename: rajatsmachine calls the laptop by a new name, and neither host keeps or passes back the laptop's old ones.
func TestARenamedMachineIsOneHostWithEachItemOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		as   []string
	}{
		{"named once", []string{"laptop"}},
		{"named again and again", []string{"laptop", "mac", "laptop", "book"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called, caller, net := relayedWorld(t)
			if err := caller.core.Store.setRelaySelf(RelaySelf{}); err != nil {
				t.Fatal(err)
			}
			_, mgr := caller.goal(t, "ship", "/repo/app")
			it := caller.ask(t, mgr.Session, "ship the API?")
			round := func() {
				t.Helper()
				for range 2 {
					if err := caller.core.SyncRelays([]string{"rajatsmachine"}, net); err != nil {
						t.Fatal(err)
					}
				}
			}
			round()
			if hosts := snapshotHosts(t, called); !slices.Equal(hosts, []string{osHostName()}) {
				t.Fatalf("first report under %v", hosts)
			}
			for _, as := range tc.as {
				if _, err := caller.core.ServeSync(RelaySync{From: "rajatsmachine", As: as, Entries: []Relay{}, Ack: []int64{}}); err != nil {
					t.Fatal(err)
				}
				caller.clock = caller.clock.Add(time.Minute)
				called.clock = caller.clock
				round()
			}
			last := tc.as[len(tc.as)-1]
			if hosts := snapshotHosts(t, called); !slices.Equal(hosts, []string{last}) {
				t.Errorf("called host keeps %v, want only %s", hosts, last)
			}
			if hosts := snapshotHosts(t, caller); !slices.Equal(hosts, []string{"rajatsmachine"}) {
				t.Errorf("renamed host keeps %v, want only the host it reaches", hosts)
			}
			b, err := called.core.Board()
			if err != nil {
				t.Fatal(err)
			}
			if len(b.Remotes) != 1 || b.Remotes[0].Host != last {
				t.Errorf("hosts line %+v, want only %s", b.Remotes, last)
			}
			n := 0
			for _, r := range b.Remotes {
				for _, got := range r.Items {
					if got.Seq == it.Seq {
						n++
					}
				}
			}
			if n != 1 {
				t.Errorf("the item is listed %d times", n)
			}
		})
	}
}

func TestItemSnapshotsKeepToTheirOwnMachine(t *testing.T) {
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	own := func(t *testing.T, w *orgWorld) string {
		t.Helper()
		id, err := w.core.Store.MachineID()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	for _, tc := range []struct {
		name string
		rows func(id string) map[string]ItemSnapshot
		want []string
	}{
		{"a machine's older name goes", func(string) map[string]ItemSnapshot {
			return map[string]ItemSnapshot{"old": {Machine: "m1", At: at}, "new": {Machine: "m1", At: at.Add(time.Minute)}}
		}, []string{"new"}},
		{"an old name a peer passes back goes again", func(string) map[string]ItemSnapshot {
			return map[string]ItemSnapshot{"new": {Machine: "m1", At: at.Add(time.Hour)}, "old": {Machine: "m1", At: at.Add(time.Minute)}}
		}, []string{"new"}},
		{"this machine's own old name goes", func(id string) map[string]ItemSnapshot {
			return map[string]ItemSnapshot{"old": {Machine: id, At: at}}
		}, nil},
		{"two machines stay two", func(string) map[string]ItemSnapshot {
			return map[string]ItemSnapshot{"a": {Machine: "m1", At: at}, "b": {Machine: "m2", At: at}}
		}, []string{"a", "b"}},
		{"a stray from before ids under the name its machine went by goes", func(string) map[string]ItemSnapshot {
			return map[string]ItemSnapshot{"dm6": {At: at}, "laptop": {Machine: "m1", HostNames: []string{"dm6"}, At: at}}
		}, []string{"laptop"}},
		{"a stray from before ids under this machine's own OS name goes", func(string) map[string]ItemSnapshot {
			return map[string]ItemSnapshot{osHostName(): {At: at}}
		}, nil},
		{"older peers under two names both stay: nothing links them", func(string) map[string]ItemSnapshot {
			return map[string]ItemSnapshot{"old": {At: at}, "new": {At: at.Add(time.Minute)}}
		}, []string{"new", "old"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			if err := w.core.Store.setRelaySelf(RelaySelf{Name: "rajatsmachine"}); err != nil {
				t.Fatal(err)
			}
			if err := w.core.keepItemSnapshots(tc.rows(own(t, w))); err != nil {
				t.Fatal(err)
			}
			if got := snapshotHosts(t, w); !slices.Equal(got, tc.want) {
				t.Errorf("kept %v, want %v", got, tc.want)
			}
		})
	}
}

func TestForgettingAHostClearsItsItemSnapshot(t *testing.T) {
	w := newOrgWorld(t)
	if err := w.core.keepItemSnapshots(map[string]ItemSnapshot{"box": {At: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)}}); err != nil {
		t.Fatal(err)
	}
	if known, err := w.core.Store.ForgetHost("box"); err != nil || !known {
		t.Fatalf("forget = %v, %v", known, err)
	}
	if got := snapshotHosts(t, w); len(got) != 0 {
		t.Errorf("snapshots %v survive forgetting", got)
	}
	if b, err := w.core.Board(); err != nil || len(b.Remotes) != 0 {
		t.Errorf("forgotten host still on the board: %+v, %v", b.Remotes, err)
	}
}

func TestANicknameFollowsAMachineKnownOnlyByItsItems(t *testing.T) {
	w := newOrgWorld(t)
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	if err := w.core.keepItemSnapshots(map[string]ItemSnapshot{"old": {Machine: "m1", At: at}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.Store.db.Exec(`INSERT INTO ym_host_nicknames(host,nick) VALUES('old','mac')`); err != nil {
		t.Fatal(err)
	}
	if err := w.core.keepItemSnapshots(map[string]ItemSnapshot{"new": {Machine: "m1", At: at.Add(time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	set, err := w.core.Store.SetNicknames()
	if err != nil || set["new"] != "mac" {
		t.Errorf("nicknames %v, %v; want new to keep mac", set, err)
	}
}
