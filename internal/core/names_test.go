package core

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestAGoalIsNamedByItsNamerElseByItsAsksFirstWords(t *testing.T) {
	for _, tc := range []struct {
		name, ask, namer, want string
	}{
		{name: "the namer's slug", ask: "make the export endpoint stream", namer: "stream-export-endpoint\n", want: "stream-export-endpoint"},
		{name: "a namer reply that talks", ask: "make the export endpoint stream", namer: "Sure! Here is the name: fix-export-streaming", want: "make-the-export-endpoint"},
		{name: "a namer reply of several words", ask: "make the export endpoint stream", namer: "Stream-Export Endpoint", want: "make-the-export-endpoint"},
		{name: "a namer reply of one word", ask: "make the export endpoint stream", namer: "export", want: "make-the-export-endpoint"},
		{name: "a namer reply too long keeps its first words", ask: "make the export endpoint stream", namer: "board-relay-sync-drops-items-on-second-host-restart", want: "board-relay-sync-drops"},
		{name: "a namer reply too long and not a slug", ask: "make the export endpoint stream", namer: "Board Relay-Sync-Drops-Items-On-Second", want: "make-the-export-endpoint"},
		{name: "the personal-repo preamble", ask: "yamnaya is a personal repo: no PR; wt worktree, push to main.\n\nTeach the board to scroll", want: "teach-the-board"},
		{name: "an opener says nothing", ask: "we were looking into the board", want: "looking-into-the-board"},
		{name: "I'd like", ask: "I'd like yamnaya Needs You closer to a queue", want: "yamnaya-needs-you-closer"},
		{name: "an id in the ask", ask: "Owner-reported via mgr51: the relay drops goal7 rows", want: "relay-drops-rows"},
		{name: "first words stopping on a loose word", ask: "check the code and tell me", want: "check-the-code"},
		{name: "an id in the namer's reply", ask: "make the export endpoint stream", namer: "fix-mgr51-export", want: "fix-export"},
		{name: "a namer reply that is filler and an id", ask: "make the export endpoint stream", namer: "owner-reported-via-mgr51", want: "make-the-export-endpoint"},
		{name: "a namer reply opening with filler", ask: "make the export endpoint stream", namer: "i-d-like-streaming-export", want: "streaming-export"},
		{name: "no namer reply", ask: "Make the export endpoint stream instead of buffering", want: "make-the-export-endpoint"},
		{name: "the standing preamble", ask: "Fix forward. Diagnose before acting. No reverts, guards, retries, or safety steps without evidence they're needed. Never block on a wait without first checking why it's slow.\n\nTeach the board to scroll", want: "teach-the-board"},
		{name: "a you-are opening", ask: "You are the implementer of this goal.\nTeach the board to scroll", want: "teach-the-board"},
		{name: "a preamble, a hard rule and how it came", ask: "Fix forward. Diagnose before acting.\n\nHARD RULE from the owner: no agent writes on any PR not authored by them.\n\nOwner approved (item mgr10:G10, PLEASE GO IMPLEMENT THIS): readable names for goals", want: "readable-names-for-goals"},
		{name: "a design-only opening", ask: "Fix forward.\n\nDESIGN ONLY. No implementation, no commits, until the owner says go.\n\nOwner ask: closer action queue", want: "closer-action-queue"},
		{name: "an urgent opening", ask: "URGENT, owner-reported, fix first: rows went missing", want: "rows-went-missing"},
		{name: "nothing but boilerplate", ask: "Fix forward. Diagnose before acting.", want: "fix-forward-diagnose-before"},
		{name: "a namer reply with nothing usable", ask: "fix the board's flicker", namer: "¯\\_(ツ)_/¯", want: "fix-the-board-s"},
		{name: "an ask with nothing usable", ask: "¿¡…!?", want: "goal"},
		{name: "a namer reply shaped like an id", ask: "ship it now", namer: "goal7", want: "ship-it-now"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Namer = func(string) string { return tc.namer }
			g, m := w.goal(t, tc.ask, "/repo/august")
			if g.Name != tc.want || m.Name != tc.want {
				t.Fatalf("goal %q, manager %q, want both %q", g.Name, m.Name, tc.want)
			}
		})
	}
}

func TestANameAlreadyUsedHereGetsTheFirstFreeNumericSuffix(t *testing.T) {
	w := newOrgWorld(t)
	w.core.Namer = func(string) string { return "fix-export" }
	w.chief(t, "fix-export-2")
	var got []string
	for range 3 {
		g, _ := w.goal(t, "anything", "/repo/august")
		got = append(got, g.Name)
	}
	if want := "fix-export fix-export-3 fix-export-4"; strings.Join(got, " ") != want {
		t.Fatalf("names %q, want %q", got, want)
	}
	g, m, err := w.core.NewGoalNamed("fix-export-3", "again", "/repo/august", "", "")
	if err != nil || g.Name != "fix-export-3-2" || m.Name != "fix-export-3-2" {
		t.Fatalf("a --name in use named goal %q, manager %q, %v; want both fix-export-3-2", g.Name, m.Name, err)
	}
	if _, _, err := w.core.NewGoalNamed("Not A Slug", "again", "/repo/august", "", ""); err == nil {
		t.Fatal("an invalid --name was accepted")
	}
}

func TestANameTakenWhileTheNamerThinksStillGivesEachItsOwn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		other func(*Store) (string, error)
	}{
		{"another goal", func(s *Store) (string, error) {
			g, err := s.AddGoal(Goal{Name: "shared-name", Ask: "x"})
			return g.Name, err
		}},
		{"another chief", func(s *Store) (string, error) {
			m, err := s.AddManager(Manager{Role: RoleChief, Name: "shared-name"})
			return m.Name, err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Namer = func(string) string {
				if name, err := tc.other(w.core.Store); err != nil || name != "shared-name" {
					t.Fatalf("the other one got %q, %v", name, err)
				}
				return "shared-name"
			}
			g, m := w.goal(t, "anything", "/repo/august")
			if g.Name != "shared-name-2" || m.Name != "shared-name-2" {
				t.Fatalf("goal %q, manager %q, want both shared-name-2", g.Name, m.Name)
			}
		})
	}
}

func TestARenameSettlesAgainstTheNamesInUseAndAStaleSaveLeavesItAlone(t *testing.T) {
	w := newOrgWorld(t)
	w.core.Namer = func(string) string { return "before-name" }
	g, m := w.goal(t, "anything", "/repo/august")
	boss := w.chief(t, "old-chief")
	o, _ := w.core.LoadOrg()
	staleG, staleM, staleBoss := o.Goals[g.ID], o.Managers[m.ID], o.Managers[boss.ID]
	if _, err := w.core.Rename(g.ID, "after-name"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.Rename(boss.ID, "new-chief"); err != nil {
		t.Fatal(err)
	}
	if err := w.core.Store.SaveGoal(staleG); err != nil {
		t.Fatal(err)
	}
	if err := w.core.Store.SaveManager(staleM); err != nil {
		t.Fatal(err)
	}
	if err := w.core.Store.SaveManager(staleBoss); err != nil {
		t.Fatal(err)
	}
	o, _ = w.core.LoadOrg()
	if o.Goals[g.ID].Name != "after-name" || o.Managers[m.ID].Name != "after-name" || o.Managers[boss.ID].Name != "new-chief" {
		t.Fatalf("goal %q, manager %q, chief %q after stale saves", o.Goals[g.ID].Name, o.Managers[m.ID].Name, o.Managers[boss.ID].Name)
	}
}

func TestARenameRetitlesTheBriefPageAtOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(*orgWorld, *testing.T) (ref, path string)
		want string
		keep string
	}{
		{"a goal not started", func(w *orgWorld, t *testing.T) (string, string) {
			_, m := w.goal(t, "make the export stream", "/repo/august")
			return m.ID, m.BriefPath
		}, "# new-name: make the export stream", "Not started yet"},
		{"a goal with a brief", func(w *orgWorld, t *testing.T) (string, string) {
			_, m := w.goal(t, "make the export stream", "/repo/august")
			if _, err := w.core.WriteBrief(m.Session, Brief{Assessment: "halfway there", Changed: "a", Uncertainty: "b", Next: "c", WaitingOn: "d"}); err != nil {
				t.Fatal(err)
			}
			return m.ID, m.BriefPath
		}, "# new-name: make the export stream", "halfway there"},
		{"a chief", func(w *orgWorld, t *testing.T) (string, string) {
			m := w.chief(t, "old-chief")
			o, _ := w.core.LoadOrg()
			return m.ID, o.Managers[m.ID].BriefPath
		}, "# new-name", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			ref, path := tc.make(w, t)
			if _, err := w.core.Rename(ref, "new-name"); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			first, rest, _ := strings.Cut(string(raw), "\n")
			if first != tc.want || !strings.Contains(rest, tc.keep) {
				t.Fatalf("brief page opens %q, want %q, keeping %q:\n%s", first, tc.want, tc.keep, raw)
			}
		})
	}
}

func TestHaikuNameRunsFromADirectoryThatSaysNothing(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "claude")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s:%s' \"$PWD\" \"$CLAUDE_CONFIG_DIR\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Chdir(t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "/inherited")
	for _, tc := range []struct{ root, want string }{{"", "/:"}, {"/roots/work", "/:/roots/work"}} {
		if got := HaikuName("anything", tc.root); got != tc.want {
			t.Fatalf("root %q: claude ran as %q, want dir:config %q", tc.root, got, tc.want)
		}
	}
}

func TestEventsAndTheWakeHoldFindAGoalByWhatTheyAreStoredUnder(t *testing.T) {
	w := newOrgWorld(t)
	w.core.Namer = func(string) string { return "named-goal" }
	g, m := w.goal(t, "anything", "/repo/august")
	if _, err := w.core.SteerContext(w.board(t), m.ID, "new plan", Owner); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.Adopt(m.Session, "", 1); err != nil {
		t.Fatal(err)
	}
	events, err := w.core.Store.GoalEvents(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(events, func(e GoalEvent) bool { return e.Kind == EventAdopted }) {
		t.Fatalf("goal %s's feed lacks its adoption: %+v", g.ID, events)
	}
	if _, err := w.core.Store.AddMessage(m.Session, wakeText(g, nil), MsgPending); err != nil {
		t.Fatal(err)
	}
	if hold, err := w.core.holdWake(w.board(t), m, g); err != nil || !hold {
		t.Fatalf("holdWake = %v, %v with a wake of the goal pending, want it held", hold, err)
	}
}

func TestEveryRefNamesAGoalByItsNameOrEitherId(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "make the export endpoint stream", "/repo/august")
	boss := w.chief(t, "arbitrator", m.ID)
	if err := w.core.Attach(g.ID, ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ ref, want string }{
		{g.Name, m.ID}, {strings.ToUpper(g.Name), m.ID}, {g.ID, m.ID}, {m.ID, m.ID}, {"arbitrator", boss.ID}, {boss.ID, boss.ID},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			o, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			got, err := o.Lookup(tc.ref)
			if err != nil || got.ID != tc.want {
				t.Fatalf("Lookup(%q) = %s, %v; want %s", tc.ref, got.ID, err, tc.want)
			}
		})
	}
}

func TestRenameTakesAValidFreeNameAndKeepsTheOldRefsWorking(t *testing.T) {
	for _, tc := range []struct {
		name, ref, to, err string
	}{
		{name: "a goal by its name", ref: "stream-export", to: "export-streaming"},
		{name: "a goal by its id", ref: "goal1", to: "export-streaming"},
		{name: "a chief", ref: "arbitrator", to: "arbiter-chief"},
		{name: "taken by a chief", ref: "stream-export", to: "arbitrator", err: "already names"},
		{name: "taken by another goal", ref: "arbitrator", to: "other-goal", err: "already names"},
		{name: "not kebab-case", ref: "stream-export", to: "Export Streaming", err: "kebab-case"},
		{name: "too short", ref: "stream-export", to: "x", err: "2 to 40"},
		{name: "shaped like an id", ref: "stream-export", to: "mgr9", err: "reads as an id"},
		{name: "its own name again", ref: "stream-export", to: "stream-export"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Namer = func(string) string { return "stream-export" }
			g, m := w.goal(t, "make the export endpoint stream", "/repo/august")
			w.core.Namer = func(string) string { return "other-goal" }
			w.goal(t, "another", "/repo/august")
			boss := w.chief(t, "arbitrator")
			o, _ := w.core.LoadOrg()
			before, _ := o.Lookup(tc.ref)
			_, err := w.core.Rename(tc.ref, tc.to)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("Rename(%q, %q) = %v, want an error saying %q", tc.ref, tc.to, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			o, _ = w.core.LoadOrg()
			for _, ref := range []string{tc.to, before.ID} {
				if got, err := o.Lookup(ref); err != nil || got.ID != before.ID || got.Shown() != tc.to {
					t.Fatalf("Lookup(%q) = %s shown %q, %v; want %s shown %q", ref, got.ID, got.Shown(), err, before.ID, tc.to)
				}
			}
			if before.ID == m.ID {
				if got := o.Goals[g.ID].Shown(); got != tc.to {
					t.Fatalf("goal shows %q, want %q", got, tc.to)
				}
			} else if before.ID != boss.ID {
				t.Fatalf("renamed %s, want %s", before.ID, boss.ID)
			}
		})
	}
}

func TestNamingUnnamedGoalsAndChiefsLeavesNamedOnesAlone(t *testing.T) {
	w := newOrgWorld(t)
	w.core.Namer = func(string) string { return "kept-name" }
	kept, _ := w.goal(t, "already named", "/repo/august")
	old, oldM := w.goal(t, "Fix flaky CI on main", "/repo/august")
	unname(t, w, old.ID, oldM.ID, "Fix flaky CI on main")
	boss := w.chief(t, "yamnaya-manager")
	if _, err := w.core.Store.db.Exec(`UPDATE ym_managers SET name='Yamnaya Manager' WHERE 'mgr'||num=?`, boss.ID); err != nil {
		t.Fatal(err)
	}
	w.core.Namer = func(string) string { return "" }
	n, err := w.core.NameUnnamed()
	if err != nil || n != 2 {
		t.Fatalf("named %d, %v; want 2", n, err)
	}
	o, _ := w.core.LoadOrg()
	for _, tc := range []struct{ id, want string }{{kept.ManagerID, "kept-name"}, {oldM.ID, "fix-flaky-ci"}, {boss.ID, "yamnaya-manager"}} {
		if got := o.Managers[tc.id].Shown(); got != tc.want {
			t.Errorf("%s shows %q, want %q", tc.id, got, tc.want)
		}
	}
	if got := o.Goals[old.ID].Name; got != "fix-flaky-ci" {
		t.Errorf("goal %s named %q, want fix-flaky-ci", old.ID, got)
	}
}

func TestAnotherHostsGoalIsRenamedOnItsHostAndShowsByTheNameItsHostReports(t *testing.T) {
	for _, tc := range []struct {
		name, to string
		ran      []string
		err      string
	}{
		{name: "a valid name runs on its host", to: "faster-login", ran: []string{"goal", "rename", "mgr3", "faster-login"}},
		{name: "an invalid name never leaves", to: "Faster Login", err: "kebab-case"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			r := &fakeRemotes{boards: []RemoteBoard{{Host: "box",
				Managers: []Manager{{ID: "mgr3", Role: RoleManager, Name: "make-login-faster", GoalID: "goal2"}},
				Goals:    []Goal{{ID: "goal2", Name: "make-login-faster", Ask: "make login faster", ManagerID: "mgr3"}}}}}
			w.core.Remotes, w.core.ShowRemotes = r, true
			w.core.Namer = func(string) string { return "make-login-faster" }
			w.goal(t, "make login faster here too", "/repo/august")
			b := w.board(t)
			o := b.Org
			for ref, want := range map[string]string{"box/make-login-faster": "box/mgr3", "make-login-faster": "mgr1"} {
				if got, err := o.Lookup(ref); err != nil || got.ID != want {
					t.Fatalf("Lookup(%q) = %s, %v; want %s", ref, got.ID, err, want)
				}
			}
			n := node(t, b, "box/mgr3")
			if n.Goal.Ref() != "box/make-login-faster" || n.Manager.Ref() != "box/make-login-faster" {
				t.Fatalf("another host's goal shows as %q, its manager %q; want both box/make-login-faster", n.Goal.Ref(), n.Manager.Ref())
			}
			_, err := w.core.Rename("box/mgr3", tc.to)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) || len(r.ran) != 0 {
					t.Fatalf("Rename = %v, ran %q; want an error saying %q and nothing run", err, r.ran, tc.err)
				}
				return
			}
			if err != nil || len(r.ran) != 1 || strings.Join(r.ran[0], " ") != "box "+strings.Join(tc.ran, " ") {
				t.Fatalf("Rename = %v, ran %q; want %q on box", err, r.ran, tc.ran)
			}
		})
	}
}

func TestAnItemLeadsWithItsGoalsNameAndItsKeyStillResolves(t *testing.T) {
	picks := []Option{{Label: "a", Effect: "x"}, {Label: "b", Effect: "y"}}
	for _, tc := range []struct {
		name string
		file func(t *testing.T, w *orgWorld) Item
		lead string
	}{
		{"a manager's ask leads with its goal", func(t *testing.T, w *orgWorld) Item {
			_, m := w.goal(t, "rotate the keys", "/repo/august")
			it, err := w.core.AddItem(Item{Kind: KindDecision, Question: "which?", Rec: "a", Options: picks, Session: m.Session, Recipient: Owner})
			if err != nil {
				t.Fatal(err)
			}
			return it
		}, "rotate-the-keys"},
		{"an agent's ask leads with the goal it works for", func(t *testing.T, w *orgWorld) Item {
			_, m := w.goal(t, "rotate the keys", "/repo/august")
			w.spawn("agent-1", m.Session)
			it, err := w.core.AddItem(Item{Kind: KindDecision, Question: "which?", Rec: "a", Options: picks, Session: "agent-1"})
			if err != nil {
				t.Fatal(err)
			}
			return it
		}, "rotate-the-keys"},
		{"a chief's ask leads with the chief, not its workspace", func(t *testing.T, w *orgWorld) Item {
			c := w.chief(t, "arbitrator")
			it, err := w.core.AddItem(Item{Kind: KindDecision, Question: "which?", Rec: "a", Options: picks, Session: c.Session, Scope: "/state/chiefs/" + c.ID})
			if err != nil {
				t.Fatal(err)
			}
			return it
		}, "arbitrator"},
		{"an ask with no goal leads with its project", func(t *testing.T, w *orgWorld) Item {
			it, err := w.core.AddItem(Item{Kind: KindDecision, Question: "which?", Rec: "a", Options: picks, Session: "loose", Scope: "/repo/august/.git"})
			if err != nil {
				t.Fatal(err)
			}
			return it
		}, "august"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			it := tc.file(t, w)
			if got := it.Lead(); got != tc.lead {
				t.Fatalf("Lead() = %q, want %q", got, tc.lead)
			}
			if got, err := w.core.Store.Item(it.Ref()); err != nil || got.Seq != it.Seq {
				t.Fatalf("Ref %q resolves to #%d, %v; want #%d", it.Ref(), got.Seq, err, it.Seq)
			}
			if line := askText(w.board(t), w.board(t).Org, it); !strings.HasPrefix(line, "ym: "+tc.lead+" ask from ") || !strings.HasSuffix(line, "  "+it.Key()) {
				t.Fatalf("wake line %q, want it to lead with %q and end with %s", line, tc.lead, it.Key())
			}
		})
	}
}

func TestAnAsksWakeLineKeepsItsKeyHoweverLongTheQuestion(t *testing.T) {
	it := Item{Seq: 12, Scope: "/repo/august/.git", ID: "D1", Question: strings.Repeat("word ", 200)}
	it.Dossier.GoalName = "rotate-the-keys"
	line := askLine(Org{}, it, "agent abc to the owner", askEventRoom)
	if n := len([]rune(line)); n > askEventRoom || !strings.HasPrefix(line, "rotate-the-keys ask from ") || !strings.HasSuffix(line, "  #12") {
		t.Fatalf("%d runes, room %d: %q", n, askEventRoom, line)
	}
}

func TestAnAskFromAnotherHostsGoalKeepsTheNameThatHostGaveItAcrossARefresh(t *testing.T) {
	w := newOrgWorld(t)
	r := &fakeRemotes{boards: []RemoteBoard{{Host: "box",
		Managers: []Manager{{ID: "mgr3", Role: RoleManager, Name: "faster-login", GoalID: "goal2"}},
		Goals:    []Goal{{ID: "goal2", Name: "faster-login", Ask: "make login faster", ManagerID: "mgr3"}}}}}
	w.core.Remotes, w.core.ShowRemotes = r, true
	b := w.board(t)
	it := Item{Kind: KindDecision, Question: "which?", Rec: "a", Options: []Option{{Label: "a", Effect: "x"}, {Label: "b", Effect: "y"}}, Session: "agent-9", Scope: "/repo/august/.git", Recipient: Owner}
	it.Dossier.Goal = "box/goal2"
	it.Dossier.GoalName = foreignGoalName(b.Org, it.Dossier)
	added, err := w.core.Store.addItem(it)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.core.Brief(b); err != nil {
		t.Fatal(err)
	}
	got, err := w.core.Store.Item(added.Key())
	if err != nil || got.Lead() != "box/faster-login" {
		t.Fatalf("Lead() = %q, %v; want box/faster-login", got.Lead(), err)
	}
}

func TestARenameReachesEveryItemFromThatGoalOrChiefWhoeverItWaitsOn(t *testing.T) {
	picks := []Option{{Label: "a", Effect: "x"}, {Label: "b", Effect: "y"}}
	for _, tc := range []struct {
		name    string
		chief   bool
		toOwner bool
	}{
		{name: "a goal's item waiting on the owner", toOwner: true},
		{name: "a goal's item waiting on its manager"},
		{name: "a chief's item waiting on the owner", chief: true, toOwner: true},
		{name: "an item waiting on a chief", chief: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			var boss Manager
			if tc.chief {
				boss = w.chief(t, "arbitrator")
			} else {
				_, boss = w.goal(t, "rotate the keys", "/repo/august")
			}
			asker := boss.Session
			if !tc.toOwner {
				asker = "agent-1"
				w.spawn(asker, boss.Session)
			}
			it, err := w.core.AddItem(Item{Kind: KindDecision, Question: "which?", Rec: "a", Options: picks, Session: asker, Recipient: Owner})
			if err != nil {
				t.Fatal(err)
			}
			if tc.toOwner != (it.Recipient == Owner) {
				t.Fatalf("item waits on %q", it.Recipient)
			}
			if _, err := w.core.Rename(boss.ID, "new-name"); err != nil {
				t.Fatal(err)
			}
			got, err := w.core.Store.Item(it.Key())
			if err != nil {
				t.Fatal(err)
			}
			if lead := w.board(t).Org.ItemLead(got); lead != "new-name" || got.Lead() != "new-name" {
				t.Fatalf("leads with %q, saved copy %q; want new-name", lead, got.Lead())
			}
		})
	}
}

func TestARenameOnAnotherHostReachesAnItemWaitingOnAManagerHere(t *testing.T) {
	w := newOrgWorld(t)
	board := RemoteBoard{Host: "box", Managers: []Manager{{ID: "mgr3", Role: RoleManager, Name: "faster-login", GoalID: "goal2"}},
		Goals: []Goal{{ID: "goal2", Name: "faster-login", ManagerID: "mgr3"}}}
	r := &fakeRemotes{boards: []RemoteBoard{board}}
	w.core.Remotes, w.core.ShowRemotes = r, true
	_, m := w.goal(t, "local work", "/repo/august")
	it := Item{Kind: KindDecision, Question: "which?", Session: "agent-9", Scope: "/repo/august/.git", Recipient: m.ID}
	it.Dossier.Goal, it.Dossier.GoalName = "box/goal2", "box/faster-login"
	added, err := w.core.Store.addItem(it)
	if err != nil {
		t.Fatal(err)
	}
	r.boards[0].Managers[0].Name, r.boards[0].Goals[0].Name = "quicker-login", "quicker-login"
	b := w.board(t)
	if err := w.core.Brief(b); err != nil {
		t.Fatal(err)
	}
	got, err := w.core.Store.Item(added.Key())
	if err != nil {
		t.Fatal(err)
	}
	if lead := b.Org.ItemLead(got); lead != "box/quicker-login" || got.Lead() != "box/quicker-login" {
		t.Fatalf("leads with %q, saved copy %q; want box/quicker-login", lead, got.Lead())
	}
}

// unname puts a goal and its manager back as they were before goals had names.
func unname(t *testing.T, w *orgWorld, goal, manager, old string) {
	t.Helper()
	if _, err := w.core.Store.db.Exec(`UPDATE ym_goals SET name='' WHERE 'goal'||num=?`, goal); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.Store.db.Exec(`UPDATE ym_managers SET name=? WHERE 'mgr'||num=?`, old, manager); err != nil {
		t.Fatal(err)
	}
	w.core.Store.invalidate()
}

func TestTheBackfillKeepsGoingAndKeepsWhatChangesWhileItRuns(t *testing.T) {
	for _, tc := range []struct {
		name  string
		act   func(*testing.T, *orgWorld, Goal, Manager)
		check func(*testing.T, Org, Goal, Manager)
		want  string
	}{
		{"its goal's work moved on", func(t *testing.T, w *orgWorld, g Goal, _ Manager) {
			o, _ := w.core.LoadOrg()
			now := o.Goals[g.ID]
			now.Summary = "written mid-backfill"
			if err := w.core.Store.SaveGoal(now); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, o Org, g Goal, _ Manager) {
			if o.Goals[g.ID].Summary != "written mid-backfill" {
				t.Errorf("summary %q was reverted", o.Goals[g.ID].Summary)
			}
		}, "slow-name"},
		{"its manager was relaunched", func(t *testing.T, w *orgWorld, _ Goal, m Manager) {
			o, _ := w.core.LoadOrg()
			now := o.Managers[m.ID]
			now.Session, now.Nudges, now.LastError = "relaunched", 7, "written mid-backfill"
			if err := w.core.Store.SaveManager(now); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, o Org, _ Goal, m Manager) {
			if got := o.Managers[m.ID]; got.Session != "relaunched" || got.Nudges != 7 || got.LastError != "written mid-backfill" {
				t.Errorf("manager row reverted: %+v", got)
			}
		}, "slow-name"},
		{"the owner named it first", func(t *testing.T, w *orgWorld, g Goal, _ Manager) {
			if _, err := w.core.Rename(g.ID, "owners-name"); err != nil {
				t.Fatal(err)
			}
		}, func(*testing.T, Org, Goal, Manager) {}, "owners-name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			first, firstM := w.goal(t, "first", "/repo/august")
			second, secondM := w.goal(t, "second", "/repo/august")
			unname(t, w, first.ID, firstM.ID, "first")
			unname(t, w, second.ID, secondM.ID, "second")
			acted := false
			w.core.Namer = func(ask string) string {
				if !acted {
					acted = true
					tc.act(t, w, first, firstM)
				}
				return map[string]string{"first": "slow-name", "second": "next-name"}[ask]
			}
			if _, err := w.core.NameUnnamed(); err != nil {
				t.Fatalf("the backfill stopped: %v", err)
			}
			o, _ := w.core.LoadOrg()
			tc.check(t, o, first, firstM)
			if o.Goals[first.ID].Name != tc.want || o.Managers[firstM.ID].Name != tc.want {
				t.Errorf("first goal %q, manager %q, want both %q", o.Goals[first.ID].Name, o.Managers[firstM.ID].Name, tc.want)
			}
			if o.Goals[second.ID].Name != "next-name" || o.Managers[secondM.ID].Name != "next-name" {
				t.Errorf("second goal %q, manager %q, want both next-name", o.Goals[second.ID].Name, o.Managers[secondM.ID].Name)
			}
		})
	}
}

func TestWhatAManagerIsToldNamesItWithoutItsId(t *testing.T) {
	w := newOrgWorld(t)
	w.core.Namer = func(string) string { return "named-goal" }
	g, m := w.goal(t, "anything", "/repo/august")
	boss := w.chief(t, "named-chief", m.ID)
	w.steer(t, m.ID, "new plan", boss.ID)
	w.clock = w.clock.Add(AdoptAfter + time.Minute)
	w.supervise(t)
	o, _ := w.core.LoadOrg()
	var prompt strings.Builder
	w.core.managerPrompt(&prompt, o, o.Managers[m.ID])
	w.core.chiefPrompt(&prompt, o, o.Managers[boss.ID])
	texts := map[string]string{"prompts": prompt.String(), "plan draft": PlanDraft(o.Managers[m.ID], "text", 1), "steer wake": strings.Join(w.handedTo(m.Session), "\n")}
	for what, text := range texts {
		if !strings.Contains(text, "named-") {
			t.Errorf("%s do not name it:\n%s", what, text)
		}
		for _, id := range []string{m.ID + " ", g.ID + " ", boss.ID + " ", "/" + m.ID} {
			if strings.Contains(text, id) {
				t.Errorf("%s show id %q:\n%s", what, id, text)
			}
		}
	}
}

func TestARenameDoesNotSplitAPendingWake(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		held       bool
	}{
		{"a wake pending under the old name", "", true},
		{"another message pending", "ym: you have not adopted revision 1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Namer = func(string) string { return "before-name" }
			g, m := w.goal(t, "anything", "/repo/august")
			text := tc.text
			if text == "" {
				text = wakeText(g, nil)
			}
			if _, err := w.core.Store.AddMessage(m.Session, text, MsgPending); err != nil {
				t.Fatal(err)
			}
			if _, err := w.core.Rename(g.ID, "after-name"); err != nil {
				t.Fatal(err)
			}
			o, _ := w.core.LoadOrg()
			w.set(m.Session, func(s *Session) { s.Status = Idle })
			if hold, err := w.core.holdWake(w.board(t), o.Managers[m.ID], o.Goals[g.ID]); err != nil || hold != tc.held {
				t.Fatalf("holdWake = %v, %v; want %v", hold, err, tc.held)
			}
		})
	}
}

func TestAChiefOnAnotherHostIsAttachedToByItsNameOrId(t *testing.T) {
	for _, ref := range []string{"box/core-lead", "box/mgr9"} {
		t.Run(ref, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "anything", "/repo/august")
			w.core.Remotes = &fakeRemotes{boards: []RemoteBoard{{Host: "box", Managers: []Manager{{ID: "mgr9", Role: RoleChief, Name: "core-lead"}}}}}
			if err := w.core.Attach(m.ID, ref); err != nil {
				t.Fatal(err)
			}
			got, err := w.core.Store.Manager(m.ID)
			if err != nil || got.ParentID != "box/mgr9" {
				t.Fatalf("parent %q, %v; want box/mgr9", got.ParentID, err)
			}
		})
	}
}

func TestAnItemOfAChiefIsFoundByTheChiefsNameOrId(t *testing.T) {
	w := newOrgWorld(t)
	boss := w.chief(t, "platform")
	it, err := w.core.Store.AddItem(Item{Kind: KindDecision, Question: "which?", Session: boss.Session, Scope: "/state/chiefs/" + boss.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"platform:" + it.ID, boss.ID + ":" + it.ID, "#" + strconv.FormatInt(it.Seq, 10)} {
		t.Run(ref, func(t *testing.T) {
			got, err := w.core.FindItem("", ref)
			if err != nil || got.Seq != it.Seq {
				t.Fatalf("FindItem(%q) = #%d, %v; want #%d", ref, got.Seq, err, it.Seq)
			}
		})
	}
	box := &fakeRemotes{boards: []RemoteBoard{{Host: "box", Managers: []Manager{{ID: "mgr9", Role: RoleChief, Name: "core-lead"}},
		Items: []Item{{Seq: 7, ID: "D5", Kind: KindDecision, Question: "where?", Scope: "box//chiefs/mgr9", State: ItemOpen}}}}}
	w.core.Remotes, w.core.ShowRemotes = box, true
	for _, ref := range []string{"box/core-lead:D5", "box/mgr9:D5"} {
		t.Run(ref, func(t *testing.T) {
			got, err := w.core.FindItem("", ref)
			if err != nil || got.Seq != 7 {
				t.Fatalf("FindItem(%q) = #%d, %v; want #7", ref, got.Seq, err)
			}
		})
	}
}

func TestAnAmbiguousItemRefOffersRefsThatWorkWithoutAnId(t *testing.T) {
	w := newOrgWorld(t)
	boss := w.chief(t, "demo")
	inChief, err := w.core.Store.AddItem(Item{Kind: KindDecision, Question: "which?", Session: boss.Session, Scope: "/state/chiefs/" + boss.ID})
	if err != nil {
		t.Fatal(err)
	}
	inRepo, err := w.core.Store.AddItem(Item{Kind: KindDecision, Question: "where?", Session: boss.Session, Scope: "/src/demo/.git"})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"demo:" + inChief.ID, inChief.ID} {
		_, err = w.core.FindItem("", ref)
		if err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("FindItem(%q) = %v, want it ambiguous", ref, err)
		}
		if _, offered, _ := strings.Cut(err.Error(), "ambiguous:"); strings.Contains(offered, boss.ID) || strings.Contains(offered, "demo:") {
			t.Fatalf("message offers a ref that does not resolve: %v", err)
		}
		for _, it := range []Item{inChief, inRepo} {
			if !strings.Contains(err.Error(), it.Key()) {
				t.Fatalf("message does not offer %s: %v", it.Key(), err)
			}
			if got, err := w.core.FindItem("", it.Key()); err != nil || got.Seq != it.Seq {
				t.Fatalf("the offered %s found #%d, %v", it.Key(), got.Seq, err)
			}
		}
	}
}

func TestAChiefStoredByNameIsSettledToItsIdWhenItsHostsBoardShowsIt(t *testing.T) {
	for _, tc := range []struct {
		name, parent, want string
		board              []Manager
	}{
		{name: "the chief has arrived", parent: "box/fresh-chief", want: "box/mgr9", board: []Manager{{ID: "mgr9", Role: RoleChief, Name: "fresh-chief"}}},
		{name: "not on that board", parent: "box/nosuch", want: "box/nosuch", board: []Manager{{ID: "mgr9", Role: RoleChief, Name: "fresh-chief"}}},
		{name: "that board has no chief by the name", parent: "box/a-goal", want: "box/a-goal", board: []Manager{{ID: "mgr9", Role: RoleManager, Name: "a-goal"}}},
		{name: "another host has the name", parent: "far/fresh-chief", want: "far/fresh-chief", board: []Manager{{ID: "mgr9", Role: RoleChief, Name: "fresh-chief"}}},
		{name: "already an id", parent: "box/mgr9", want: "box/mgr9", board: []Manager{{ID: "mgr9", Role: RoleChief, Name: "fresh-chief"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "anything", "/repo/august")
			if _, err := w.core.Store.db.Exec(`UPDATE ym_managers SET parent_id=? WHERE 'mgr'||num=?`, tc.parent, m.ID); err != nil {
				t.Fatal(err)
			}
			w.core.Store.invalidate()
			w.core.Remotes, w.core.ShowRemotes = &fakeRemotes{boards: []RemoteBoard{{Host: "box", Managers: tc.board}}}, true
			if parent := w.board(t).Org.Managers[m.ID].ParentID; parent != tc.want {
				t.Fatalf("the board's parent %q, want %q", parent, tc.want)
			}
			got, err := w.core.Store.Manager(m.ID)
			if err != nil || got.ParentID != tc.want {
				t.Fatalf("stored parent %q, %v; want %q", got.ParentID, err, tc.want)
			}
		})
	}
}

func TestAQueueRowLeadsWithItsDoLineElseItsGoalsNameAndNeverAKey(t *testing.T) {
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	item := func(do, scope string) *Item {
		return &Item{Seq: 7, ID: "V52", Kind: KindDecision, Question: "which way should it go?", DoLine: do, Scope: scope, CreatedAt: now}
	}
	for _, tc := range []struct {
		name         string
		need         Need
		lead, absent string
		side, detail string
	}{
		{name: "a do-line leads", need: Need{Item: item("fix QA-51 in qa-hub", "/src/august/.git"), Name: "faster-login"},
			lead: "fix QA-51 in qa-hub", absent: "faster-login", side: "faster-login", detail: "faster-login"},
		{name: "no do-line: the goal's name, then the question", need: Need{Item: item("", "/src/august/.git"), Name: "faster-login"},
			lead: "faster-login which way", absent: "august:V52", side: "august:V52", detail: "faster-login"},
		{name: "a chief's item never shows its id", need: Need{Item: item("", "/state/chiefs/mgr4"), Name: "platform"},
			lead: "platform which way", absent: "mgr4", side: "#7", detail: "platform"},
		{name: "a stuck goal by its name", need: Need{Goal: &Goal{ID: "goal9", Name: "faster-login", Ask: "go"}, Name: "faster-login"},
			lead: "unstick faster-login", absent: "goal9", side: "faster-login", detail: "faster-login"},
		{name: "an unadopted steer by its manager's name", need: Need{Steer: &Steer{Revision: 2}, Manager: "mgr9", Name: "faster-login"},
			lead: "faster-login adopt r2", absent: "mgr9", side: "faster-login", detail: "faster-login"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			narrow, side := RowLine(tc.need, "1", QueueWidth, now)
			if side != "" {
				t.Fatalf("a %d-column row has a side %q", QueueWidth, side)
			}
			_, words, _ := strings.Cut(narrow, tc.need.Verb())
			if !strings.Contains(narrow, tc.lead[:min(len(tc.lead), 12)]) || strings.Contains(words, tc.absent) || ansi.StringWidth(narrow) > QueueWidth {
				t.Fatalf("row %q, want %q leading without %q", narrow, tc.lead, tc.absent)
			}
			wide, side := RowLine(tc.need, "1", 120, now)
			if !strings.Contains(wide, tc.lead[:min(len(tc.lead), 12)]) || !strings.Contains(side, tc.side) {
				t.Fatalf("at 120 columns %q | %q, want the side to hold %q", wide, side, tc.side)
			}
			detail := strings.Join(RowDetail(tc.need, 60, now), "\n")
			if !strings.Contains(detail, tc.detail) {
				t.Fatalf("opened row %q lacks %q", detail, tc.detail)
			}
			for _, id := range []string{"goal9", "mgr9", "mgr4"} {
				if strings.Contains(narrow+side+detail, id) {
					t.Fatalf("row shows id %s: %q | %q | %q", id, narrow, side, detail)
				}
			}
		})
	}
}

func TestABoardsQueueRowsFollowARename(t *testing.T) {
	w := newOrgWorld(t)
	w.core.Namer = func(string) string { return "before-name" }
	g, m := w.goal(t, "anything", "/repo/august")
	w.spawn("a1", m.Session)
	if _, err := w.core.AddItem(Item{Kind: KindChore, Question: "rotate the key", Session: m.Session, Recipient: Owner, Rec: "r", Dossier: Dossier{Goal: g.ID}}); err != nil {
		t.Fatal(err)
	}
	find := func() Need {
		t.Helper()
		for _, n := range w.board(t).Needs {
			if n.Item != nil {
				return n
			}
		}
		t.Fatal("no item row in the queue")
		return Need{}
	}
	if got := find().Name; got != "before-name" {
		t.Fatalf("row named %q, want before-name", got)
	}
	if _, err := w.core.Rename(g.ID, "after-name"); err != nil {
		t.Fatal(err)
	}
	if line, _ := RowLine(find(), "1", QueueWidth, w.clock); !strings.Contains(line, "after-name") {
		t.Fatalf("row %q does not follow the rename", line)
	}
}

func TestACloseShowsItsChiefsNameAndIsJudgedByItsId(t *testing.T) {
	for _, tc := range []struct {
		name    string
		closer  string
		stored  func(*orgWorld, Goal, Manager, string) GoalEvent
		settle  bool
		action  bool
		line    string
		hideIDs bool
	}{
		{name: "closed now by the chief reading it", closer: "boss", line: "closed as abandoned by chief boss-chief: not needed", hideIDs: true},
		{name: "closed now by another chief", closer: "other", action: true, line: "by chief other-chief:", hideIDs: true},
		{name: "closed now by the owner", closer: "owner", action: true, line: "by the owner:", hideIDs: true},
		{name: "stored with the id, not settled", closer: "boss", stored: legacyClose, line: "by chief "},
		{name: "stored with the id, settled", closer: "boss", stored: legacyClose, settle: true, line: "closed as abandoned by chief boss-chief: not needed", hideIDs: true},
		{name: "another chief's, stored with the id and settled", closer: "other", stored: legacyClose, settle: true, action: true, line: "by chief other-chief:", hideIDs: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Namer = func(string) string { return "closing-goal" }
			g, m := w.goal(t, "anything", "/repo/august")
			boss := w.chief(t, "boss-chief", m.ID)
			other := w.chief(t, "other-chief", boss.ID)
			author := map[string]string{"boss": boss.ID, "other": other.ID, "owner": Owner}[tc.closer]
			if tc.stored != nil {
				tc.stored(w, g, m, author)
			} else if _, err := w.core.CloseGoal(w.board(t), CloseOrder{Ref: g.ID, As: CloseAbandoned, Why: "not needed", Author: author}); err != nil {
				t.Fatal(err)
			}
			if tc.settle {
				if n, err := w.core.SettleClosures(); err != nil || n != 1 {
					t.Fatalf("settled %d, %v; want 1", n, err)
				}
				if n, err := w.core.SettleClosures(); err != nil || n != 0 {
					t.Fatalf("settling again changed %d, %v", n, err)
				}
			}
			events, err := w.core.Store.GoalEvents(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			at := slices.IndexFunc(events, func(e GoalEvent) bool { return e.Kind == EventGoalClosed })
			if at < 0 {
				t.Fatalf("no close in %+v", events)
			}
			e := events[at]
			if !strings.Contains(e.Payload, tc.line) {
				t.Fatalf("line %q, want %q", e.Payload, tc.line)
			}
			if tc.hideIDs && (strings.Contains(e.Payload, boss.ID) || strings.Contains(e.Payload, other.ID)) {
				t.Fatalf("line %q shows an id", e.Payload)
			}
			split, err := w.core.sortChief(boss.ID, []GoalEvent{e})
			if err != nil {
				t.Fatal(err)
			}
			if got := len(split.action) == 1; got != tc.action {
				t.Fatalf("needs the chief %v, want %v: %+v", got, tc.action, split)
			}
			if cl := parseClosure(e); cl.Disposition != CloseAbandoned || cl.Reason != "not needed" {
				t.Fatalf("closure %+v", cl)
			}
		})
	}
}

// legacyClose stores a close as it was written when its line named its chief by id.
func legacyClose(w *orgWorld, g Goal, _ Manager, author string) GoalEvent {
	e, _, err := w.core.Store.AddGoalEvent(GoalEvent{Goal: g.ID, Kind: EventGoalClosed, Payload: closureText(CloseAbandoned, author, "not needed", nil), Created: w.clock, Delivered: w.clock})
	if err != nil {
		panic(err)
	}
	return e
}

func TestAGoalNamedWhenTheNamerFailedGetsTheNamersNameOnce(t *testing.T) {
	for _, tc := range []struct {
		name, ask, was string
		source         string
		want           string
	}{
		{name: "the first words, before sources were recorded", ask: "I'd like yamnaya Needs You closer", was: "i-d-like-yamnaya", want: "good-name"},
		{name: "with a numeric suffix", ask: "I'd like yamnaya Needs You closer", was: "i-d-like-yamnaya-3", want: "good-name"},
		{name: "an id in them", ask: "Owner-reported via mgr51: the relay drops rows", was: "owner-reported-via-mgr51", want: "good-name"},
		{name: "recorded as the fallback", ask: "anything at all", was: "something-else", source: NamedByFallback, want: "good-name"},
		{name: "a person's, before sources were recorded", ask: "I'd like yamnaya Needs You closer", was: "needs-you-queue", want: "needs-you-queue"},
		{name: "a person's, recorded", ask: "I'd like yamnaya Needs You closer", was: "i-d-like-yamnaya", source: NamedByHand, want: "i-d-like-yamnaya"},
		{name: "the namer's already", ask: "I'd like yamnaya Needs You closer", was: "i-d-like-yamnaya", source: NamedByNamer, want: "i-d-like-yamnaya"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, tc.ask, "/repo/august")
			boss := w.chief(t, "Keeps Its Name")
			if _, err := w.core.Store.db.Exec(`UPDATE ym_goals SET name=?, name_source=? WHERE 'goal'||num=?`, tc.was, tc.source, g.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := w.core.Store.db.Exec(`UPDATE ym_managers SET name=? WHERE 'mgr'||num=?`, tc.was, m.ID); err != nil {
				t.Fatal(err)
			}
			w.core.Store.invalidate()
			calls := 0
			w.core.Namer = func(string) string { calls++; return "good-name" }
			if _, err := w.core.BetterNames(); err != nil {
				t.Fatal(err)
			}
			o, _ := w.core.LoadOrg()
			if o.Goals[g.ID].Name != tc.want || o.Managers[m.ID].Name != tc.want {
				t.Fatalf("goal %q, manager %q, want both %q", o.Goals[g.ID].Name, o.Managers[m.ID].Name, tc.want)
			}
			if got := o.Managers[boss.ID].Name; got != "keeps-its-name" {
				t.Fatalf("chief is now %q", got)
			}
			if tc.want == "good-name" {
				raw, err := os.ReadFile(m.BriefPath)
				if err != nil || !strings.HasPrefix(string(raw), "# good-name: ") {
					t.Fatalf("brief page %q, %v", raw, err)
				}
			}
			calls = 0
			if _, err := w.core.BetterNames(); err != nil || calls != 0 {
				t.Fatalf("a second pass asked the namer %d times, %v", calls, err)
			}
		})
	}
}

func TestANameAPersonGaveStaysThroughBetterNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(*orgWorld) Goal
		want string
	}{
		{"ym goal --name", func(w *orgWorld) Goal {
			g, _, err := w.core.NewGoalNamed("chosen-name", "anything", "/repo/august", "", "")
			if err != nil {
				panic(err)
			}
			return g
		}, "chosen-name"},
		{"ym goal rename to the very first words", func(w *orgWorld) Goal {
			g, _, err := w.core.NewGoalNamed("", "make the export stream", "/repo/august", "", "")
			if err != nil {
				panic(err)
			}
			if _, err := w.core.Rename(g.ID, "make-the-export-stream"); err != nil {
				panic(err)
			}
			return g
		}, "make-the-export-stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g := tc.make(w)
			w.core.Namer = func(string) string { return "good-name" }
			if _, err := w.core.BetterNames(); err != nil {
				t.Fatal(err)
			}
			o, _ := w.core.LoadOrg()
			if got := o.Goals[g.ID].Name; got != tc.want {
				t.Fatalf("name %q, want %q", got, tc.want)
			}
		})
	}
}

func TestANamerThatStaysOutEndsThePassAndALaterOneFinishesIt(t *testing.T) {
	w := newOrgWorld(t)
	var ids []string
	for _, ask := range []string{"first ask here", "second ask here", "third ask here", "fourth ask here", "fifth ask here"} {
		g, _ := w.goal(t, ask, "/repo/august")
		ids = append(ids, g.ID)
		if g.NameSource != NamedByFallback {
			t.Fatalf("%s came by its name as %q", g.Name, g.NameSource)
		}
	}
	calls := 0
	w.core.Namer = func(string) string { calls++; return "" }
	if n, err := w.core.BetterNames(); err != nil || n != 0 || calls != giveUpAfter {
		t.Fatalf("named %d with %d calls, %v; want 0 with %d", n, calls, err, giveUpAfter)
	}
	w.core.Namer = func(string) string { return "good-name" }
	if n, err := w.core.BetterNames(); err != nil || n != len(ids) {
		t.Fatalf("named %d, %v; want %d", n, err, len(ids))
	}
	o, _ := w.core.LoadOrg()
	if got := o.Goals[ids[4]].Name; got != "good-name-5" {
		t.Fatalf("last goal named %q", got)
	}
}
