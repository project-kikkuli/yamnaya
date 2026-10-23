package cli

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/profile"
	"github.com/project-kikkuli/yamnaya/internal/usage"
)

func TestInterruptedHandoffsRecoverTheSameNativeConversation(t *testing.T) {
	for _, phase := range []string{"manifest_only", "published", "selected", "recorded"} {
		t.Run(phase, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("YAMNAYA_STATE_DIR", state)
			a := profile.Profile{Harness: "codex", ID: "a", Root: t.TempDir(), Explicit: true}
			b := profile.Profile{Harness: "codex", ID: "b", Root: t.TempDir(), Explicit: true}
			for _, p := range []profile.Profile{a, b} {
				if err := profile.Add(state, p); err != nil {
					t.Fatal(err)
				}
			}
			rel := filepath.Join("sessions", "2026", "10", "23", "rollout-2026-10-23T10-00-00-"+profileConversation+".jsonl")
			path := filepath.Join(a.Root, rel)
			data := profileMeta(profileConversation, "", 0) + profileResponse("codex", time.Now().UTC().Format(time.RFC3339Nano), "first", 100, 20)
			putProfileFile(t, path, data)
			dest := filepath.Join(b.Root, rel)
			old := filepath.Join(b.Root, "sessions", "old.jsonl")
			putProfileFile(t, old, profileMeta(profileConversation, "", 0))
			db, err := sql.Open("sqlite", filepath.Join(b.Root, "state_5.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(`CREATE TABLE threads(id TEXT PRIMARY KEY,rollout_path TEXT);INSERT INTO threads VALUES(?,?)`, profileConversation, old); err != nil {
				t.Fatal(err)
			}
			st, err := core.OpenStore(filepath.Join(state, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			rec := launch.Record{Key: "conversation", Harness: "codex", Profile: "a", ConfigRoot: a.Root, ConfigExplicit: true, Session: profileConversation, Worktree: launch.Worktree{Path: t.TempDir()}, Adopted: true}
			l := launcher{records: launch.Records{Dir: filepath.Join(state, "launches")}, core: &core.Core{Store: st, Procs: func() ([]proc.Proc, error) { return nil, nil }}}
			if err = l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			m := migration{Version: 1, Source: rec, Destination: b, Expected: old, Head: dest, State: "prepared"}
			if phase == "manifest_only" {
				hash, err := profile.HashRange(path, 0, int64(len(data)))
				if err != nil {
					t.Fatal(err)
				}
				seed, err := usage.ImportSeedAt(a.Root, path, int64(len(data)))
				if err != nil {
					t.Fatal(err)
				}
				if err = profile.SaveImports(state, dest, []profile.Import{{Start: 0, End: int64(len(data)), Hash: hash, Seed: seed, SourceRoot: a.Root, SourceProfile: "a", Conversation: profileConversation}}); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err = stageArtifact(state, a, b, path, profileConversation); err != nil {
					t.Fatal(err)
				}
				m.State = "published"
			}
			if phase == "selected" || phase == "recorded" {
				if err = retargetRollout(b.Root, profileConversation, old, dest); err != nil {
					t.Fatal(err)
				}
				m.State = "selected"
			}
			if phase == "recorded" {
				setRecordProfile(&rec, b)
				if err = l.records.Save(rec); err != nil {
					t.Fatal(err)
				}
				m.State = "recorded"
			}
			if err = saveMigration(state, m); err != nil {
				t.Fatal(err)
			}
			// Reopen only durable state.
			l = launcher{records: l.records, core: &core.Core{Store: st, Procs: func() ([]proc.Proc, error) { return nil, nil }}}
			recovered, err := l.recoverMigration(rec)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Session != profileConversation || recovered.Profile != "b" || recovered.ConfigRoot != b.Root {
				t.Fatalf("recovered wrong owner/head: %#v", recovered)
			}
			got, err := selectedRollout(b.Root, profileConversation)
			if err != nil || got != dest {
				t.Fatalf("stale destination selection %s %v", got, err)
			}
			original, _ := os.ReadFile(path)
			copied, _ := os.ReadFile(dest)
			if string(original) != data || string(copied) != data {
				t.Fatal("conversation bytes changed")
			}
			// A second restart retains the same publication.
			if _, err = l.recoverMigration(recovered); err != nil {
				t.Fatalf("recovery is not idempotent: %v", err)
			}
		})
	}
}
func TestImportRejectsCredentialSymlinksAndEscapingDestinationDirectories(t *testing.T) {
	state := t.TempDir()
	a := profile.Profile{Harness: "claude", ID: "a", Root: t.TempDir()}
	b := profile.Profile{Harness: "claude", ID: "b", Root: t.TempDir()}
	credential := filepath.Join(t.TempDir(), "auth.json")
	putProfileFile(t, credential, "fixture credential\n")
	source := filepath.Join(a.Root, "projects", "p", "s.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(credential, source); err != nil {
		t.Fatal(err)
	}
	if _, err := stageArtifact(state, a, b, source, "s"); err == nil {
		t.Fatal("credential symlink copied")
	}
	os.Remove(source)
	putProfileFile(t, source, "native transcript\n")
	escaped := t.TempDir()
	if err := os.Symlink(escaped, filepath.Join(b.Root, "projects")); err != nil {
		t.Fatal(err)
	}
	if _, err := stageArtifact(state, a, b, source, "s"); err == nil {
		t.Fatal("destination symlink escape accepted")
	}
	files, _ := os.ReadDir(escaped)
	if len(files) > 0 {
		t.Fatal("copy wrote outside the profile")
	}
}
