package core

import (
	"encoding/json"
	stderrors "errors"
	"os"
	"path/filepath"
	"regexp"
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
			it, err := w.core.AddItem(Item{Kind: KindDecision, DoLine: "Do it", Question: "which?", Rec: "a", Options: picks, Session: m.Session, Recipient: Owner})
			if err != nil {
				t.Fatal(err)
			}
			return it
		}, "rotate-the-keys"},
		{"an agent's ask leads with the goal it works for", func(t *testing.T, w *orgWorld) Item {
			_, m := w.goal(t, "rotate the keys", "/repo/august")
			w.spawn("agent-1", m.Session)
			it, err := w.core.AddItem(Item{Kind: KindDecision, DoLine: "Do it", Question: "which?", Rec: "a", Options: picks, Session: "agent-1"})
			if err != nil {
				t.Fatal(err)
			}
			return it
		}, "rotate-the-keys"},
		{"a chief's ask leads with the chief, not its workspace", func(t *testing.T, w *orgWorld) Item {
			c := w.chief(t, "arbitrator")
			it, err := w.core.AddItem(Item{Kind: KindDecision, DoLine: "Do it", Question: "which?", Rec: "a", Options: picks, Session: c.Session, Scope: "/state/chiefs/" + c.ID})
			if err != nil {
				t.Fatal(err)
			}
			return it
		}, "arbitrator"},
		{"an ask with no goal leads with its project", func(t *testing.T, w *orgWorld) Item {
			it, err := w.core.AddItem(Item{Kind: KindDecision, DoLine: "Do it", Question: "which?", Rec: "a", Options: picks, Session: "loose", Scope: "/repo/august/.git"})
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
	line := askLine(Org{}, it, "agent abc", "the owner", lineMax)
	if n := len([]rune(line)); n > lineMax || !strings.HasPrefix(line, "rotate-the-keys ask from ") || !strings.HasSuffix(line, "  #12") {
		t.Fatalf("%d runes, room %d: %q", n, lineMax, line)
	}
}

func TestAnAsksWakeLineGivesTheQuestionTheRoomItHas(t *testing.T) {
	const name = "productionize-arbiter-kpis"
	question := "R3 synthetic Postgres TCP credentials are denied: Pg sim's first run got password authentication failed for user postgres on shared g15-pr-shepherd-k3-pg at localhost:32768. Use a dedicated R3 container or provide the shared synthetic DSN?"
	it := Item{Seq: 244, Scope: "/repo/august/.git", ID: "D1", Question: question}
	it.Dossier.GoalName = name
	long := strings.Repeat("a-long-goal-name-", 6)
	for _, tc := range []struct {
		name, from, to, wantRoute string
		label                     func(GoalEvent) string
	}{
		{"to the manager the goal is named for", "agent c3861f18540b", name, "ask from agent c3861f18540b: ", nil},
		{"to the owner", "agent c3861f18540b", "the owner", "ask from agent c3861f18540b to the owner: ", nil},
		{"from the goal's own manager", "manager " + name, "the owner", "ask from manager to the owner: ", nil},
		{"to another chief", "agent c3861f18540b", "platform", "ask from agent c3861f18540b to platform: ", nil},
		{"under a chief's long label", "agent c3861f18540b", name, "ask from agent c3861f18540b: ", func(GoalEvent) string { return long }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := GoalEvent{Seq: 6091304, Kind: EventAsked, Payload: askLine(Org{}, it, tc.from, tc.to, lineMax)}
			line := inboxText("x", []GoalEvent{e}, "", tc.label)
			line = line[strings.Index(line, "\n- ")+3:]
			if n := len([]rune(line)); n != lineMax-2 {
				t.Fatalf("%d runes, want the question to fill the line: %q", n, line)
			}
			if !strings.Contains(line, name+" "+tc.wantRoute) || strings.Count(line, name) != 1 {
				t.Fatalf("want %q and the goal's name once: %q", tc.wantRoute, line)
			}
			if !strings.HasSuffix(line, " #244") {
				t.Fatalf("lost its key: %q", line)
			}
		})
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
			it, err := w.core.AddItem(Item{Kind: KindDecision, DoLine: "Do it", Question: "which?", Rec: "a", Options: picks, Session: asker, Recipient: Owner})
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
	if !strings.Contains(prompt.String(), "ym goal adopt --revision") || !strings.Contains(prompt.String(), "ym goal context prints") || !strings.Contains(prompt.String(), "ym goal list lists") {
		t.Errorf("prompts should name no goal in commands that need none:\n%s", prompt.String())
	}
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
	if _, err := w.core.AddItem(Item{Kind: KindChore, DoLine: "Do it", Question: "rotate the key", Session: m.Session, Recipient: Owner, Rec: "r", Dossier: Dossier{Goal: g.ID}}); err != nil {
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
	if got := find().Name; got != "after-name" {
		t.Fatalf("row named %q, want after-name", got)
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

func TestAWakeOrErrorAboutAGoalOrChiefNamesIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		text func(*testing.T, *orgWorld, Goal, Manager, Manager) string
		want string
	}{
		{"the quiet nudge", func(t *testing.T, w *orgWorld, _ Goal, m, _ Manager) string {
			w.spawn("agent-1", m.Session)
			w.clock = w.clock.Add(2 * QuietAfter)
			w.supervise(t)
			return strings.Join(w.handedTo(m.Session), "\n")
		}, "nothing has moved on goal named-goal since"},
		{"the adopt nudge", func(t *testing.T, w *orgWorld, _ Goal, m, boss Manager) string {
			w.steer(t, m.ID, "new plan", boss.ID)
			w.clock = w.clock.Add(AdoptAfter + time.Minute)
			w.supervise(t)
			return strings.Join(w.handedTo(m.Session), "\n")
		}, "ym goal adopt named-goal --revision 1"},
		{"a goal with outstanding work", func(t *testing.T, w *orgWorld, g Goal, _, _ Manager) string {
			tx, err := w.core.Store.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(`INSERT INTO ym_assignments (id,goal,state) VALUES ('a1',?,'outstanding')`, g.ID); err != nil {
				t.Skip(err)
			}
			return outstandingIn(tx, g.ID).Error()
		}, "named-goal has 1 outstanding"},
		{"a goal with no verification under way", func(t *testing.T, w *orgWorld, g Goal, _, _ Manager) string {
			_, err := w.core.Store.OpenVerification(g.ID)
			return err.Error()
		}, "goal named-goal has no verification"},
		{"attaching under a manager on another host", func(t *testing.T, w *orgWorld, _ Goal, m, _ Manager) string {
			w.core.Remotes = &fakeRemotes{boards: []RemoteBoard{{Host: "box", Managers: []Manager{{ID: "mgr9", Role: RoleManager, Name: "a-manager"}}}}}
			return w.core.Attach(m.ID, "box/mgr9").Error()
		}, "box/a-manager is a manager"},
		{"what off holds, a goal", func(t *testing.T, _ *orgWorld, _ Goal, m, _ Manager) string {
			return OffTarget{Manager: &m}.String()
		}, "goal named-goal and every agent"},
		{"what off holds, a chief", func(t *testing.T, _ *orgWorld, _ Goal, _, boss Manager) string {
			return OffTarget{Manager: &boss}.String()
		}, "chief named-chief and its own agents"},
		{"an item's escalation in its history", func(t *testing.T, w *orgWorld, _ Goal, m, boss Manager) string {
			it, err := w.core.Store.AddItem(Item{Kind: KindChore, Question: "q", DoLine: "do it", Rec: "r", Tried: "t", Evidence: []string{"e"}, Session: m.Session, Recipient: boss.ID})
			if err != nil {
				t.Fatal(err)
			}
			up, err := w.core.Escalate(it.Key())
			if err != nil {
				t.Fatal(err)
			}
			got, _ := w.core.Store.Item(up.Key())
			return got.History[len(got.History)-1].Text
		}, "escalated to owner from named-chief"},
		{"a report's chief on another host in its prompt", func(t *testing.T, w *orgWorld, _ Goal, m, _ Manager) string {
			keepBoard(t, w, "box", Manager{ID: "mgr9", Role: RoleChief, Name: "far-chief"})
			o, _ := w.core.LoadOrg()
			m.ParentID = "box/mgr9"
			o.Managers[m.ID] = m
			return w.core.boss(o, m)
		}, "your chief box/far-chief"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Namer = func(string) string { return "named-goal" }
			g, m := w.goal(t, "anything", "/repo/august")
			boss := w.chief(t, "named-chief", m.ID)
			if m, _ = w.core.Store.Manager(m.ID); m.ID == "" {
				t.Fatal("no manager")
			}
			got := tc.text(t, w, g, m, boss)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for _, id := range []string{g.ID + " ", m.ID + " ", boss.ID + " ", "mgr9"} {
				if strings.Contains(got, id) {
					t.Fatalf("%q shows id %q", got, id)
				}
			}
		})
	}
}

// keepBoard stores a board from host as serve's reader would, with no live read in this process.
func keepBoard(t *testing.T, w *orgWorld, host string, managers ...Manager) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"managers": managers})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.core.Store.KeepBoard(host, time.Now(), raw); err != nil {
		t.Fatal(err)
	}
}

func TestEveryProcessNamesAChiefFromTheBoardsItStored(t *testing.T) {
	for _, tc := range []struct {
		name, self, id, want string
		live                 bool
	}{
		{name: "another host's, from its stored board", id: "box/mgr9", want: "box/far-chief"},
		{name: "another host's, a live read winning", id: "box/mgr9", live: true, want: "box/live-chief"},
		{name: "another host's goal", id: "box/mgr9", want: "box/far-chief"},
		{name: "this host's under its own relay name", self: "home", id: "home/LOCAL", want: "boss-chief"},
		{name: "this host's", id: "LOCAL", want: "boss-chief"},
		{name: "a host with no board", id: "far/mgr9", want: "chief on far"},
		{name: "a goal on a host with no board", id: "far/goal3", want: "goal on far"},
		{name: "a name typed under its host stays", id: "far/some-chief", want: "far/some-chief"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			boss := w.chief(t, "boss-chief")
			keepBoard(t, w, "box", Manager{ID: "mgr9", Role: RoleChief, Name: "far-chief"})
			if tc.self != "" {
				if err := w.core.Store.setRelaySelf(RelaySelf{Name: tc.self}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.live {
				w.core.Remotes = &fakeRemotes{boards: []RemoteBoard{{Host: "box", Managers: []Manager{{ID: "mgr9", Role: RoleChief, Name: "live-chief"}}}}}
			}
			if got := w.core.RefName(strings.ReplaceAll(tc.id, "LOCAL", boss.ID)); got != tc.want {
				t.Fatalf("RefName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAnAskToAChiefOnAnotherHostNamesItInTheFeed(t *testing.T) {
	w := newOrgWorld(t)
	w.core.Namer = func(string) string { return "named-goal" }
	g, m := w.goal(t, "anything", "/repo/august")
	keepBoard(t, w, "box", Manager{ID: "mgr9", Role: RoleChief, Name: "far-chief"})
	if err := w.core.Attach(m.ID, "box/mgr9"); err != nil {
		t.Fatal(err)
	}
	mine, err := w.core.Store.Manager(m.ID)
	if err != nil || mine.ParentID != "box/mgr9" {
		t.Fatalf("parent %q, %v", mine.ParentID, err)
	}
	if _, err := w.core.AddItem(Item{Kind: KindDecision, Question: "which way?", Session: m.Session}); err != nil {
		t.Fatal(err)
	}
	events, err := w.core.Store.GoalEvents(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(events, func(e GoalEvent) bool { return e.Kind == EventAsked })
	if at < 0 || !strings.Contains(events[at].Payload, "to box/far-chief") || strings.Contains(events[at].Payload, "mgr9") {
		t.Fatalf("feed %+v, want the ask to name box/far-chief", events)
	}
}

func TestASteerOrCloseFromAnotherHostShowsItsChiefsNameAndKeepsItsId(t *testing.T) {
	for _, tc := range []struct {
		name, carried, want string
	}{
		{name: "the relay carried a name", carried: "ops-lead", want: "chief home/ops-lead"},
		{name: "the name came whole", carried: "home/ops-lead", want: "chief home/ops-lead"},
		{name: "an older host sent none", carried: "", want: "chief on home"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "anything", "/repo/august")
			boss := w.chief(t, "boss-chief", m.ID)
			if err := w.core.Attach(boss.ID, "home/mgr2"); err != nil {
				t.Fatal(err)
			}
			st, err := w.core.SteerContextAs(w.board(t), m.ID, "new plan", "home/mgr2", tc.carried)
			if err != nil {
				t.Fatal(err)
			}
			if st.Author != "home/mgr2" {
				t.Fatalf("author key %q, want home/mgr2", st.Author)
			}
			o, _ := w.core.LoadOrg()
			if got := o.SteerBy(st); got != tc.want {
				t.Fatalf("shown as %q, want %q", got, tc.want)
			}
			stored, err := w.core.Store.Steers(m.ID)
			if err != nil || len(stored) != 1 || o.SteerBy(stored[0]) != tc.want {
				t.Fatalf("stored %+v, %v; want it shown as %q", stored, err, tc.want)
			}
		})
	}
}

func TestAVerdictNeedsNoRefFromItsVerifierSession(t *testing.T) {
	for _, tc := range []struct {
		name, session, ref string
		err                string
	}{
		{name: "the verifier, no ref", session: "VERIFIER"},
		{name: "the verifier, naming the goal", session: "VERIFIER", ref: "GOAL"},
		{name: "another session, no ref", session: "someone-else", err: "verifying no goal"},
		{name: "another session, naming the goal", session: "someone-else", ref: "GOAL", err: "may record"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newVerifyWorld(t)
			g := w.goal
			w.done(t, "finished the importer")
			w.tick(t, time.Second)
			session := strings.ReplaceAll(tc.session, "VERIFIER", w.verifiers()[0])
			_, err := w.core.RecordVerdict(session, strings.ReplaceAll(tc.ref, "GOAL", g.Name), VerdictPass, nil)
			if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("RecordVerdict = %v, want error %q", err, tc.err)
			}
		})
	}
}

func TestNameArgsRenamesRefsAndLeavesAPersonsWords(t *testing.T) {
	o := Org{Goals: map[string]Goal{"box/goal3": {ID: "box/goal3", Name: "fix-export", Host: "box"}},
		Managers: map[string]Manager{"box/mgr51": {ID: "box/mgr51", Name: "far-chief", Host: "box"}, "mgr7": {ID: "mgr7", Name: "here-chief"}}}
	for _, tc := range []struct {
		host string
		args []string
		want string
	}{
		{"box", []string{"goal", "send", "--relayed", "mgr51", "see goal3 and mgr51 too"}, "goal send --relayed far-chief see goal3 and mgr51 too"},
		{"box", []string{"goal", "send", "mgr51", "mgr51"}, "goal send far-chief mgr51"},
		{"box", []string{"goal", "close", "goal3", "--as", "abandoned", "--why", "goal3"}, "goal close fix-export --as abandoned --why goal3"},
		{"box", []string{"goal", "steer", "--text", "mgr51", "--by", "home/mgr2", "--by-name", "ops-lead", "goal3"}, "goal steer --text mgr51 fix-export"},
		{"box", []string{"goal", "done", "--summary", "goal3", "--evidence", "mgr51"}, "goal done --summary goal3 --evidence mgr51"},
		{"box", []string{"item", "add", "--question", "goal3", "--rec", "mgr51", "--do", "mgr51"}, "item add --question goal3 --rec mgr51 --do mgr51"},
		{"box", []string{"goal", "goal3", "--name", "mgr51"}, "goal goal3 --name mgr51"},
		{"box", []string{"goal", "--repo", "app", "--", "goal3"}, "goal --repo app -- goal3"},
		{"box", []string{"goal", "list", "--under", "mgr51"}, "goal list --under far-chief"},
		{"box", []string{"kill", "--yes", "goal3"}, "kill --yes fix-export"},
		{"box", []string{"goal", "relaunch", "mgr99"}, "goal relaunch chief on box"},
		{"far", []string{"goal", "relaunch", "mgr51"}, "goal relaunch chief on far"},
		{"", []string{"goal", "relaunch", "mgr7"}, "goal relaunch here-chief"},
	} {
		if got := strings.Join(o.NameArgs(tc.host, tc.args), " "); got != tc.want {
			t.Errorf("NameArgs(%q, %q) = %q, want %q", tc.host, tc.args, got, tc.want)
		}
	}
}

// olderHost refuses the flag an older ym does not have, or fails some other way.
type olderHost struct {
	fakeRemotes
	refuse error
	calls  [][]string
}

func (r *olderHost) Run(_ string, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	if slices.Contains(args, "--by-name") {
		return "", r.refuse
	}
	return "ok", nil
}

func TestASteerOrCloseFromAChiefReachesAHostOnAnOlderYm(t *testing.T) {
	for _, tc := range []struct {
		name   string
		act    func(*testing.T, *orgWorld, Manager) error
		refuse error
		calls  int
		fails  bool
	}{
		{"a steer, the host has the flag", func(t *testing.T, w *orgWorld, boss Manager) error {
			_, err := w.core.SteerContext(w.board(t), "box/mgr1", "new plan", boss.ID)
			return err
		}, nil, 1, false},
		{"a steer, an older host", func(t *testing.T, w *orgWorld, boss Manager) error {
			_, err := w.core.SteerContext(w.board(t), "box/mgr1", "new plan", boss.ID)
			return err
		}, stderrors.New("exit status 1: ym: unknown flag: --by-name"), 2, false},
		{"a close, an older host", func(t *testing.T, w *orgWorld, boss Manager) error {
			_, err := w.core.CloseGoal(w.board(t), CloseOrder{Ref: "box/goal1", As: CloseAbandoned, Why: "x", Author: boss.ID})
			return err
		}, stderrors.New("exit status 1: ym: unknown flag: --by-name"), 2, false},
		{"another refusal is not sent again", func(t *testing.T, w *orgWorld, boss Manager) error {
			_, err := w.core.SteerContext(w.board(t), "box/mgr1", "new plan", boss.ID)
			return err
		}, stderrors.New("exit status 1: ym: no such goal"), 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			boss := w.chief(t, "boss-chief")
			r := &olderHost{refuse: tc.refuse}
			r.boards = []RemoteBoard{{Host: "box", Managers: []Manager{{ID: "mgr1", Role: RoleManager, Name: "far-goal", GoalID: "goal1"}},
				Goals: []Goal{{ID: "goal1", Name: "far-goal", ManagerID: "mgr1"}}}}
			w.core.Remotes = r
			err := tc.act(t, w, boss)
			if (err != nil) != tc.fails {
				t.Fatalf("err %v, want failing %v", err, tc.fails)
			}
			if len(r.calls) != tc.calls {
				t.Fatalf("ran %v, want %d calls", r.calls, tc.calls)
			}
			if tc.fails && (!strings.Contains(err.Error(), "box/far-goal") || !strings.Contains(err.Error(), "no such goal")) {
				t.Fatalf("error %q, want the goal's name and the far side's words", err)
			}
		})
	}
}

func TestAChiefOnAnotherHostIsNamedFromWhatLasts(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed func(*testing.T, *orgWorld)
		want string
	}{
		{"a board that has since gone", func(t *testing.T, w *orgWorld) {
			keepBoard(t, w, "peer", Manager{ID: "mgr51", Role: RoleChief, Name: "master"})
			if _, err := w.core.Store.db.Exec(`DELETE FROM ym_host_boards`); err != nil {
				t.Fatal(err)
			}
			w.core.Store.invalidate()
		}, "peer/master"},
		{"the names a snapshot sends", func(t *testing.T, w *orgWorld) {
			if err := w.core.keepItemSnapshots(map[string]ItemSnapshot{"peer": {At: w.clock, Names: map[string]string{"mgr51": "master"}}}); err != nil {
				t.Fatal(err)
			}
		}, "peer/master"},
		{"an older snapshot's item line", func(t *testing.T, w *orgWorld) {
			snap := ItemSnapshot{At: w.clock, Items: []Item{{Seq: 1, ID: "D1", Dossier: Dossier{From: "chief master (mgr51)"}}}}
			raw, _ := json.Marshal(snap)
			if _, err := w.core.Store.db.Exec(`INSERT INTO ym_host_items(host,at,items) VALUES('peer',1,?)`, raw); err != nil {
				t.Fatal(err)
			}
			if err := w.core.LearnHostNames(); err != nil {
				t.Fatal(err)
			}
		}, "peer/master"},
		{"nothing", func(*testing.T, *orgWorld) {}, "chief on peer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Namer = func(string) string { return "named-goal" }
			g, m := w.goal(t, "anything", "/repo/august")
			tc.seed(t, w)
			if got := w.core.RefName("peer/mgr51"); got != tc.want {
				t.Fatalf("RefName = %q, want %q", got, tc.want)
			}
			if err := w.core.Attach(m.ID, "peer/mgr51"); err != nil {
				t.Fatal(err)
			}
			o, _ := w.core.LoadOrg()
			if got := w.core.boss(o, o.Managers[m.ID]); got != "your "+chiefPhrase(tc.want) {
				t.Fatalf("prompt says %q", got)
			}
			if _, err := w.core.AddItem(Item{Kind: KindDecision, Question: "which way?", Session: m.Session}); err != nil {
				t.Fatal(err)
			}
			events, _ := w.core.Store.GoalEvents(g.ID)
			at := slices.IndexFunc(events, func(e GoalEvent) bool { return e.Kind == EventAsked })
			if at < 0 || !strings.Contains(events[at].Payload, "to "+tc.want) || strings.Contains(events[at].Payload, "mgr51") {
				t.Fatalf("feed %+v, want the ask to %q", events, tc.want)
			}
			if got := o.SteerBy(Steer{Author: "peer/mgr51"}); got != chiefPhrase(tc.want) {
				t.Fatalf("SteerBy = %q", got)
			}
		})
	}
}

func TestAPromptNamesItsChiefOnceAndNeverItsOwnId(t *testing.T) {
	w := newOrgWorld(t)
	w.core.Namer = func(string) string { return "named-goal" }
	_, m := w.goal(t, "anything", "/repo/august")
	boss := w.chief(t, "named-chief", m.ID)
	o, _ := w.core.LoadOrg()
	if got := w.core.boss(o, o.Managers[m.ID]); got != "your chief named-chief" {
		t.Fatalf("boss = %q", got)
	}
	var prompt strings.Builder
	w.core.managerPrompt(&prompt, o, o.Managers[m.ID])
	w.core.chiefPrompt(&prompt, o, o.Managers[boss.ID])
	paths := regexp.MustCompile(`\S*/(chiefs|goals)/\S+`)
	if ids := regexp.MustCompile(`\b(goal|mgr)[0-9]+\b`).FindAllString(paths.ReplaceAllString(prompt.String(), ""), -1); len(ids) != 0 {
		t.Fatalf("prompts show ids %v outside paths:\n%s", ids, prompt.String())
	}
}

func TestWhatARelayedCommandTellsARequesterShowsNoIds(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  RelayRef
		args []string
		want []string
		item bool
	}{
		{name: "it ran", ref: RelayRef{Output: "closed it"},
			args: []string{"goal", "close", "goal3", "--as", "abandoned", "--why", "goal3", "--by", "home/mgr2"},
			want: []string{"ym: ym goal close far-goal --as abandoned --why goal3 on box: closed it"}},
		{name: "it failed, with the far command in its text", ref: RelayRef{Error: "ym goal steer --text mgr51 --by home/mgr2 mgr51 --by-name ops-lead: exit status 3: no such goal"},
			args: []string{"goal", "steer", "--text", "mgr51", "--by", "home/mgr2", "mgr51"},
			want: []string{"ym: ym goal steer --text mgr51 far-chief on box failed: exit status 3: no such goal"}},
		{name: "it failed, the far ym naming only its command", ref: RelayRef{Error: "ym goal steer: exit status 3: no such goal"},
			args: []string{"goal", "steer", "--text", "plan", "--by", "home/mgr2", "mgr51"},
			want: []string{"ym goal steer --text plan far-chief on box failed: exit status 3: no such goal"}},
		{name: "refused for good", ref: RelayRef{Final: true, Error: "ym goal close goal3 --as abandoned --why x --by home/mgr2 --by-name ops-lead: exit status 3: gone"},
			args: []string{"goal", "close", "goal3", "--as", "abandoned", "--why", "x", "--by", "home/mgr2"},
			want: []string{"ym goal close far-goal --as abandoned --why x on box was refused for good: exit status 3: gone"}, item: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "anything", "/repo/august")
			w.spawn("a1", m.Session)
			keepBoard(t, w, "box", Manager{ID: "mgr51", Role: RoleChief, Name: "far-chief"}, Manager{ID: "mgr3", Role: RoleManager, Name: "far-goal", GoalID: "goal3"})
			raw, err := json.Marshal(map[string]any{"managers": []Manager{{ID: "mgr51", Role: RoleChief, Name: "far-chief"}}, "goals": []Goal{{ID: "goal3", Name: "far-goal"}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := w.core.Store.KeepBoard("box", time.Now().Add(time.Hour), raw); err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(relayYM{Args: tc.args, Requester: m.Session, ByName: "ops-lead"})
			if err != nil {
				t.Fatal(err)
			}
			if err := w.core.tellOutcome(Relay{Seq: 9, Dest: "box", Kind: RelayYM, Body: string(body)}, tc.ref); err != nil {
				t.Fatal(err)
			}
			text := strings.Join(w.handedTo(m.Session), "\n")
			if tc.item {
				items, err := w.core.Store.Items(ItemFilter{})
				if err != nil || len(items) != 1 {
					t.Fatalf("items %+v, %v", items, err)
				}
				text = items[0].Question + "\n" + items[0].Rec + "\n" + strings.Join(items[0].Evidence, "\n")
			}
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("told %q, want %q", text, want)
				}
			}
			for _, leak := range []string{"--by", "home/mgr2", "ops-lead"} {
				if strings.Contains(text, leak) {
					t.Errorf("told %q, which shows %q", text, leak)
				}
			}
		})
	}
}

func TestACloseRefusedOnTheFarHostNamesTheChief(t *testing.T) {
	for _, tc := range []struct{ name, carried, want string }{
		{"it came with its name", "ops-lead", "does not report to home/ops-lead"},
		{"it came with none", "", "does not report to chief on home"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, _ := w.goal(t, "anything", "/repo/august")
			_, err := w.core.CloseGoal(w.board(t), CloseOrder{Ref: g.ID, As: CloseAbandoned, Why: "x", Author: "home/mgr2", AuthorName: tc.carried})
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "mgr2") {
				t.Fatalf("err %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAClosureReadsItsChiefByNameInTheEndedList(t *testing.T) {
	for _, tc := range []struct{ name, actor, want string }{
		{"an old host's, naming this host's chief", "chief home/LOCAL", "chief boss-chief"},
		{"another host's chief, name known", "chief peer/mgr51", "chief peer/master"},
		{"another host's chief, no name", "chief peer/mgr99", "chief on peer"},
		{"already by name", "chief home/some-chief", "chief home/some-chief"},
		{"already with no name", "chief on peer", "chief on peer"},
		{"the owner", "the owner", "the owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			if err := w.core.Store.setRelaySelf(RelaySelf{Name: "home"}); err != nil {
				t.Fatal(err)
			}
			boss := w.chief(t, "boss-chief")
			if err := w.core.Store.noteHostNames(map[string]string{"peer/mgr51": "master"}); err != nil {
				t.Fatal(err)
			}
			o, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			if got := o.ActorShown(strings.ReplaceAll(tc.actor, "LOCAL", boss.ID)); got != tc.want {
				t.Fatalf("ActorShown = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestARefusedRelaysRecNamesWhatItWasForAndNoCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"a close of a goal by its id", []string{"goal", "close", "goal3", "--as", "abandoned", "--why", "x", "--by", "home/mgr2"}, "send the same request for box/far-goal again once box can take it"},
		{"a close by its name", []string{"goal", "close", "far-goal", "--as", "abandoned", "--why", "x"}, "send the same request for box/far-goal again once box can take it"},
		{"a steer of a chief by its id", []string{"goal", "steer", "--text", "plan", "mgr51"}, "send the same request for box/far-chief again once box can take it"},
		{"a chief no name is known for", []string{"goal", "close", "mgr99", "--as", "abandoned", "--why", "x"}, "send the same request for chief on box again once box can take it"},
		{"a goal no name is known for", []string{"goal", "close", "goal99", "--as", "abandoned", "--why", "x"}, "send the same request for goal on box again once box can take it"},
		{"nothing it acts on", []string{"item", "answer", "#3", "yes"}, "send the same request again once box can take it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "anything", "/repo/august")
			raw, err := json.Marshal(map[string]any{"managers": []Manager{{ID: "mgr51", Role: RoleChief, Name: "far-chief"}}, "goals": []Goal{{ID: "goal3", Name: "far-goal"}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := w.core.Store.KeepBoard("box", time.Now(), raw); err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(relayYM{Args: tc.args, Requester: m.Session})
			if err != nil {
				t.Fatal(err)
			}
			if err := w.core.tellOutcome(Relay{Seq: 9, Dest: "box", Kind: RelayYM, Body: string(body)}, RelayRef{Final: true, Error: "exit status 3: refused"}); err != nil {
				t.Fatal(err)
			}
			items, err := w.core.Store.Items(ItemFilter{})
			if err != nil || len(items) != 1 {
				t.Fatalf("items %+v, %v", items, err)
			}
			if items[0].Rec != tc.want {
				t.Fatalf("rec %q, want %q", items[0].Rec, tc.want)
			}
		})
	}
}

func TestABackfillRetitlesTheBriefPage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		brief bool
		chief bool
		first string
		keeps string
	}{
		{name: "a goal with no brief yet", first: "# good-name: make the export stream", keeps: "Not started yet"},
		{name: "a goal with a recorded brief", brief: true, first: "# good-name: make the export stream", keeps: "halfway there"},
		{name: "a chief with no brief yet", chief: true, first: "# ops-lead", keeps: "its remit"},
		{name: "a chief with a recorded brief", chief: true, brief: true, first: "# ops-lead", keeps: "halfway there"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Namer = func(string) string { return "before-name" }
			var id, path, session, oldTitle string
			if tc.chief {
				m, err := w.core.NewChief("ops-lead", "its remit", "/repo/august", "", nil)
				if err != nil {
					t.Fatal(err)
				}
				id, path, session, oldTitle = m.ID, m.BriefPath, m.Session, "# "+m.ID
				if _, err := w.core.Store.db.Exec(`UPDATE ym_managers SET name='Ops Lead' WHERE 'mgr'||num=?`, m.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				g, m := w.goal(t, "make the export stream", "/repo/august")
				id, path, session, oldTitle = g.ID, m.BriefPath, m.Session, "# "+g.ID+": make the export stream"
				unname(t, w, g.ID, m.ID, "make the export stream")
			}
			w.core.Store.invalidate()
			if tc.brief {
				if _, err := w.core.WriteBrief(session, Brief{Assessment: "halfway there", Changed: "a", Uncertainty: "b", Next: "c", WaitingOn: "d"}); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			_, rest, _ := strings.Cut(string(raw), "\n")
			if err := os.WriteFile(path, []byte(oldTitle+"\n"+rest), 0o644); err != nil {
				t.Fatal(err)
			}
			w.core.Namer = func(string) string { return "good-name" }
			if n, err := w.core.NameUnnamed(); err != nil || n != 1 {
				t.Fatalf("named %d, %v", n, err)
			}
			raw, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			first, rest, _ := strings.Cut(string(raw), "\n")
			if first != tc.first || !strings.Contains(rest, tc.keeps) || strings.Contains(string(raw), id+":") {
				t.Fatalf("brief page opens %q, want %q keeping %q:\n%s", first, tc.first, tc.keeps, raw)
			}
		})
	}
}

func TestARenameTakesTheOldNameOffWhatTheRowStores(t *testing.T) {
	for _, tc := range []struct {
		name, lastError string
		want            []string
	}{
		{"a stuck goal", "", []string{"manager new-name did not stay up"}},
		{"a stuck goal whose last launch names it", "branch old-name exists", []string{"manager new-name did not stay up", "last launch failed: branch new-name exists"}},
		{"a longer name stays", "see old-name-2 here", []string{"manager new-name did not stay up", "see old-name-2 here"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Namer = func(string) string { return "old-name" }
			g, m := w.goal(t, "anything", "/repo/august")
			now, err := w.core.Store.Manager(m.ID)
			if err != nil {
				t.Fatal(err)
			}
			now.Session, now.Failures, now.LastError, now.NextAttempt = "", stuckFailures, tc.lastError, w.clock.Add(time.Hour)
			if err := w.core.Store.SaveManager(now); err != nil {
				t.Fatal(err)
			}
			w.supervise(t)
			was, err := w.core.Store.Goal(g.ID)
			if err != nil || was.State != GoalStuck || !strings.Contains(was.Evidence, "old-name") {
				t.Fatalf("goal %+v, %v; want it stuck naming old-name", was, err)
			}
			before, _ := w.core.Store.GoalEvents(g.ID)
			if _, err := w.core.Rename(g.ID, "new-name"); err != nil {
				t.Fatal(err)
			}
			got, _ := w.core.Store.Goal(g.ID)
			for _, want := range tc.want {
				if !strings.Contains(got.Evidence, want) {
					t.Errorf("evidence %q, want %q", got.Evidence, want)
				}
			}
			if rest := strings.ReplaceAll(got.Evidence, "old-name-2", ""); strings.Contains(rest, "old-name") {
				t.Errorf("evidence %q still shows the old name", got.Evidence)
			}
			mine, _ := w.core.Store.Manager(m.ID)
			if rest := strings.ReplaceAll(mine.LastError, "old-name-2", ""); strings.Contains(rest, "old-name") {
				t.Errorf("last error %q still shows the old name", mine.LastError)
			}
			w.supervise(t)
			after, _ := w.core.Store.GoalEvents(g.ID)
			if len(after) != len(before) {
				t.Errorf("a rename raised events: %+v", after[len(before):])
			}
			if again, _ := w.core.Store.Goal(g.ID); again.Evidence != got.Evidence {
				t.Errorf("the next check rewrote evidence to %q", again.Evidence)
			}
		})
	}
}

func TestARenameReachesAnItemsFromLine(t *testing.T) {
	w := newOrgWorld(t)
	w.core.Namer = func(string) string { return "old-name" }
	g, m := w.goal(t, "anything", "/repo/august")
	it, err := w.core.Store.AddItem(Item{Kind: KindChore, Question: "q", Session: m.Session})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.core.Store.SaveDossier(it.Seq, Dossier{Goal: g.ID, GoalName: "old-name", From: "agent a1, working for manager old-name"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.Rename(g.ID, "new-name"); err != nil {
		t.Fatal(err)
	}
	got, err := w.core.Store.Item(it.Key())
	if err != nil || got.Dossier.From != "agent a1, working for manager new-name" {
		t.Fatalf("from %q, %v", got.Dossier.From, err)
	}
}

func TestAFromLineSaysAManagersNameOnce(t *testing.T) {
	for _, tc := range []struct{ name, from, want string }{
		{"a stored stutter", "manager x-name (x-name)", "manager x-name"},
		{"an agent's stutter", "agent 9e99c130, working for manager x-name (x-name)", "agent 9e99c130, working for manager x-name"},
		{"from before names", "chief master (mgr51)", "chief master"},
		{"already once", "manager x-name", "manager x-name"},
		{"a session the owner started", "agent 9e99c130, in a session you started", "agent 9e99c130, in a session you started"},
		{"brackets that say something else", "manager x-name (old-note)", "manager x-name (old-note)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := DossierLines(Dossier{From: tc.from})
			if len(lines) == 0 || lines[0][0] != "from" || lines[0][1] != tc.want {
				t.Fatalf("lines %v, want from %q", lines, tc.want)
			}
		})
	}
}

func TestARenameLeavesAnOldFromLineSayingTheNameOnce(t *testing.T) {
	w := newOrgWorld(t)
	w.core.Namer = func(string) string { return "old-name" }
	g, m := w.goal(t, "anything", "/repo/august")
	it, err := w.core.Store.AddItem(Item{Kind: KindChore, Question: "q", Session: m.Session})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.core.Store.SaveDossier(it.Seq, Dossier{Goal: g.ID, From: "manager old-name (" + m.ID + ")"}); err != nil {
		t.Fatal(err)
	}
	if err := w.core.renameInItems(g.ID, "new-name", []string{"old-name", m.ID}); err != nil {
		t.Fatal(err)
	}
	got, err := w.core.Store.Item(it.Key())
	if err != nil || got.Dossier.From != "manager new-name" {
		t.Fatalf("from %q, %v", got.Dossier.From, err)
	}
}

func TestAnItemFromBeforeNamesFollowsItsChief(t *testing.T) {
	for _, tc := range []struct {
		name  string
		from  string
		scope string
		state string
		via   string
		want  string
	}{
		{name: "open, waiting on a chief, a backfill then a rename", from: "chief Sub Chief (ID)", scope: "/src/app/.git", state: ItemOpen, via: "backfill", want: "chief under-chief"},
		{name: "closed, a backfill then a rename", from: "chief Sub Chief (ID)", scope: "/src/app/.git", state: ItemDismissed, via: "backfill", want: "chief under-chief"},
		{name: "already backfilled, settled at serve start", from: "chief Sub Chief (ID)", scope: "/src/app/.git", state: ItemOpen, via: "settle", want: "chief under-chief"},
		{name: "its id lost, the chief's workspace left", from: "chief Sub Chief", scope: "/state/chiefs/ID", state: ItemOpen, via: "settle", want: "chief under-chief"},
		{name: "an agent of the chief", from: "agent 9e99c130, working for chief Sub Chief (ID)", scope: "/src/app/.git", state: ItemOpen, via: "settle", want: "agent 9e99c130, working for chief under-chief"},
		{name: "another chief's item stays", from: "chief Other Chief (mgr77)", scope: "/src/app/.git", state: ItemOpen, via: "settle", want: "chief Other Chief"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Namer = func(string) string { return "" }
			boss := w.chief(t, "sub-chief")
			_, m := w.goal(t, "anything", "/repo/august")
			it, err := w.core.Store.AddItem(Item{Kind: KindDecision, Question: "q", Session: m.Session, Recipient: boss.ID, Scope: strings.ReplaceAll(tc.scope, "ID", boss.ID)})
			if err != nil {
				t.Fatal(err)
			}
			if err := w.core.Store.SaveDossier(it.Seq, Dossier{From: strings.ReplaceAll(tc.from, "ID", boss.ID)}); err != nil {
				t.Fatal(err)
			}
			if tc.state != ItemOpen {
				if _, err := w.core.Store.Transition(it.Key(), tc.state, "done with"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.via == "backfill" {
				if _, err := w.core.Store.db.Exec(`UPDATE ym_managers SET name='Sub Chief' WHERE 'mgr'||num=?`, boss.ID); err != nil {
					t.Fatal(err)
				}
				w.core.Store.invalidate()
				if _, err := w.core.NameUnnamed(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := w.core.SettleItemFroms(); err != nil {
				t.Fatal(err)
			}
			if _, err := w.core.Rename(boss.ID, "under-chief"); err != nil {
				t.Fatal(err)
			}
			got, err := w.core.Store.Item(it.Key())
			if err != nil {
				t.Fatal(err)
			}
			lines := DossierLines(got.Dossier)
			at := slices.IndexFunc(lines, func(l [2]string) bool { return l[0] == "from" })
			if at < 0 || lines[at][1] != tc.want {
				t.Fatalf("from %v, want %q; stored %+v", lines, tc.want, got.Dossier)
			}
			if n, err := w.core.SettleItemFroms(); err != nil || n != 0 {
				t.Fatalf("settling again changed %d, %v", n, err)
			}
		})
	}
}
