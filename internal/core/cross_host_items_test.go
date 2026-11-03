package core

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
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
				if err != nil || found.State != ItemAnswered || found.Answer != "ship" || len(found.History) != 2 {
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
	for _, missing := range []string{"#99999", "#0", "#-1", "#bad", "c/#0"} {
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
