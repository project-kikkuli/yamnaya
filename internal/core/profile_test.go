package core

import "testing"

func TestConversationAliasesNeverOverwriteProfileScopedBoardEntries(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		rows := []Live{{Session: Session{Harness: "codex", ID: "same-conversation", Profile: "work"}}, {Session: Session{Harness: "codex", ID: "same-conversation", Profile: "default"}}, {Session: Session{Harness: "codex", ID: "same-conversation", Profile: "work"}, Remote: "another-host"}}
		if reverse {
			rows[0], rows[2] = rows[2], rows[0]
		}
		b := Board{Sessions: rows}
		projects := group(rows, nil, map[string]string{}, Lineage{}, nil)
		if len(projects) != 1 || len(projects[0].Sessions) != 3 {
			t.Fatalf("profile copies disappeared from project: %#v", projects)
		}
		for _, indexed := range []bool{false, true} {
			if indexed {
				b.indexSessions()
			}
			if _, ok := b.Session("same-conversation"); ok {
				t.Fatal("ambiguous native conversation overwrote a profile entry")
			}
			for _, ref := range []string{"codex:default@same-conversation", "codex:work@same-conversation", "another-host/codex:work@same-conversation"} {
				if _, ok := b.Session(ref); !ok {
					t.Fatalf("qualified session lost: %s", ref)
				}
			}
		}
	}
}

type selectedLaunches struct{ *fakeLaunches }

func (l selectedLaunches) Start(n NewLaunch) (Started, error) {
	if n.ConfigRoot == "" {
		n.ConfigRoot = "/isolated/" + n.Harness + "/" + n.Profile
	}
	s, err := l.fakeLaunches.Start(n)
	s.Profile, s.ConfigRoot = n.Profile, n.ConfigRoot
	return s, err
}

func TestManagersChiefsAndVerifiersKeepSelectedProfileThroughRelaunch(t *testing.T) {
	for _, h := range []string{"claude", "codex"} {
		t.Run(h, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Launches = selectedLaunches{w.launches}
			g, m, err := w.core.NewGoal("implement selected subscription", "/repo/profiles", h, "work")
			if err != nil {
				t.Fatal(err)
			}
			chief, err := w.core.NewChief("chief", "", "/repo/profiles", h, nil, "personal")
			if err != nil {
				t.Fatal(err)
			}
			for _, original := range []Manager{m, chief} {
				w.stop(original.Session)
				loaded, err := w.core.Store.Manager(original.ID)
				if err != nil {
					t.Fatal(err)
				}
				if loaded.Profile != original.Profile || loaded.ConfigRoot != original.ConfigRoot || loaded.ConfigRoot == "" {
					t.Fatalf("persistent selection lost: %#v", loaded)
				}
				b := w.board(t)
				o, err := w.core.LoadOrg()
				if err != nil {
					t.Fatal(err)
				}
				again := w.core.start(b, o, loaded, false)
				if again.LastError != "" || again.Profile != original.Profile || again.ConfigRoot != original.ConfigRoot {
					t.Fatalf("relaunch changed selection: %#v", again)
				}
				last := w.launches.starts[len(w.launches.starts)-1]
				if last.Profile != original.Profile || last.ConfigRoot != original.ConfigRoot {
					t.Fatalf("launch ignored stored profile: %#v", last)
				}
			}
			o, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			v, err := w.core.Store.AddVerification(Verification{Goal: g.ID, Summary: "verify selection", CreatedAt: w.clock})
			if err != nil {
				t.Fatal(err)
			}
			if err = w.core.launchVerifier(o, g, v); err != nil {
				t.Fatal(err)
			}
			last := w.launches.starts[len(w.launches.starts)-1]
			if last.Harness != h || last.Profile != m.Profile || last.ConfigRoot != m.ConfigRoot {
				t.Fatalf("verifier lost manager selection: %#v", last)
			}
		})
	}
}
