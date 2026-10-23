package cli

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/profile"
	"github.com/project-kikkuli/yamnaya/internal/usage"
)

const profileConversation = "22222222-2222-4222-8222-222222222222"

func putProfileFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func appendProfileFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(content)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
}
func profileResponse(h, stamp, id string, input, output int) string {
	if h == "claude" {
		return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":%q,"model":"claude-sonnet-4-6","usage":{"input_tokens":%d,"output_tokens":%d}}}`+"\n", stamp, id, input, output)
	}
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"output_tokens":%d},"last_token_usage":{"input_tokens":%d,"output_tokens":%d}}}}`+"\n", stamp, input, output, input, output)
}
func profileMeta(id, base string, cutoff int) string {
	extra := ""
	if base != "" {
		extra = fmt.Sprintf(`,"history_base":{"thread_id":%q,"end_byte_offset":%d,"end_ordinal_exclusive":3}`, base, cutoff)
	}
	return fmt.Sprintf(`{"timestamp":"2026-10-23T10:00:00Z","type":"session_meta","payload":{"id":%q%s}}`+"\n", id, extra) + `{"type":"turn_context","payload":{"model":"gpt-5"}}` + "\n"
}
func profileMeter(t *testing.T, state string, ps []profile.Profile) *usage.Meter {
	t.Helper()
	m := usage.New(state)
	m.Fetch = nil
	m.Profiles = ps
	m.Every = time.Hour
	return m
}
func scanProfiles(t *testing.T, m *usage.Meter, now time.Time) core.Fleet {
	t.Helper()
	if _, err := m.Scan(now); err != nil {
		t.Fatal(err)
	}
	return m.Fleet(now)
}
func TestProfileTransfersPreservePayersThroughRoundTripsAndColdRebuild(t *testing.T) {
	now := time.Now()
	stamp := func(n int) string { return now.Add(time.Duration(n) * time.Second).UTC().Format(time.RFC3339Nano) }
	for _, h := range []string{"claude", "codex"} {
		t.Run(h, func(t *testing.T) {
			state := t.TempDir()
			ps := []profile.Profile{}
			for _, id := range []string{"a", "b", "c"} {
				ps = append(ps, profile.Profile{Harness: h, ID: id, Label: "same label", Root: t.TempDir(), Account: h + "/user/" + id, Explicit: true})
			}
			rel := filepath.Join("projects", "project", profileConversation+".jsonl")
			prefix := ""
			if h == "codex" {
				rel = filepath.Join("sessions", "2026", "10", "23", "rollout-2026-10-23T10-00-00-"+profileConversation+".jsonl")
				prefix = profileMeta(profileConversation, "", 0)
			}
			paths := []string{filepath.Join(ps[0].Root, rel), filepath.Join(ps[1].Root, rel), filepath.Join(ps[2].Root, rel)}
			putProfileFile(t, paths[0], prefix+profileResponse(h, stamp(0), "a1", 100, 20))
			if h == "codex" {
				appendProfileFile(t, paths[0], fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","primary":{"used_percent":100,"window_minutes":300,"resets_at":%d}}}}`+"\n", stamp(0), now.Add(time.Hour).Unix()))
			}
			m := profileMeter(t, state, ps)
			defer m.Close()
			scanProfiles(t, m, now)
			if _, err := stageArtifact(state, ps[0], ps[1], paths[0], profileConversation); err != nil {
				t.Fatal(err)
			}
			f := scanProfiles(t, m, now)
			for _, l := range f.Limits {
				if l.Profile == "b" {
					t.Fatal("copied source exhaustion leaked into B")
				}
			}
			input, output := 30, 5
			if h == "codex" {
				input, output = 130, 25
			}
			appendProfileFile(t, paths[1], profileResponse(h, stamp(1), "b1", input, output))
			scanProfiles(t, m, now)
			if _, err := stageArtifact(state, ps[1], ps[0], paths[1], profileConversation); err != nil {
				t.Fatal(err)
			}
			input, output = 10, 2
			if h == "codex" {
				input, output = 140, 27
			}
			appendProfileFile(t, paths[0], profileResponse(h, stamp(2), "a2", input, output))
			if _, err := stageArtifact(state, ps[0], ps[2], paths[0], profileConversation); err != nil {
				t.Fatal(err)
			}
			input, output = 10, 1
			if h == "codex" {
				input, output = 150, 28
			}
			appendProfileFile(t, paths[2], profileResponse(h, stamp(3), "c1", input, output))
			check := func(f core.Fleet) {
				t.Helper()
				for i, want := range []core.Tokens{{Input: 110, Output: 22}, {Input: 30, Output: 5}, {Input: 10, Output: 1}} {
					key := profile.SessionKey(h, ps[i].ID, profileConversation)
					got, ok := f.Sessions[key]
					if !ok || got.Own.Tokens != want || got.Account != ps[i].Account {
						t.Fatalf("%s: got %#v want %#v", key, got, want)
					}
				}
				if f.Today.Tokens.Input != 150 || f.Today.Tokens.Output != 28 {
					t.Fatalf("copied usage counted twice: %#v", f.Today.Tokens)
				}
			}
			check(scanProfiles(t, m, now.Add(10*time.Second)))
			m.Close()
			m = profileMeter(t, state, ps)
			check(scanProfiles(t, m, now.Add(11*time.Second)))
			m.Close()
			for _, name := range []string{"ym.db", "ym.db-wal", "ym.db-shm"} {
				os.Remove(filepath.Join(state, name))
			}
			m = profileMeter(t, state, ps)
			defer m.Close()
			check(scanProfiles(t, m, now.Add(12*time.Second)))
			imports, err := profile.Imports(state, paths[2])
			if err != nil {
				t.Fatal(err)
			}
			accounts := map[string]bool{}
			for _, r := range imports {
				for _, o := range r.Origins {
					accounts[o.Account] = true
				}
			}
			if !accounts[ps[0].Account] || !accounts[ps[1].Account] {
				t.Fatalf("transitive payer provenance lost: %#v", imports)
			}
		})
	}
}
func TestRevertedRolloutHasStableConversationAndSeedsInheritedTotals(t *testing.T) {
	state := t.TempDir()
	a := profile.Profile{Harness: "codex", ID: "a", Root: t.TempDir(), Account: "codex/user/a"}
	b := profile.Profile{Harness: "codex", ID: "b", Root: t.TempDir(), Account: "codex/user/b"}
	now := time.Now()
	stamp := now.UTC().Format(time.RFC3339Nano)
	rel := filepath.Join("sessions", "2026", "10", "23", "rollout-2026-10-23T10-00-00-"+profileConversation+".jsonl")
	first := profileMeta(profileConversation, "", 0) + profileResponse("codex", stamp, "first", 100, 20)
	ap := filepath.Join(a.Root, rel)
	putProfileFile(t, ap, first)
	bp, err := stageArtifact(state, a, b, ap, profileConversation)
	if err != nil {
		t.Fatal(err)
	}
	appendProfileFile(t, bp, profileResponse("codex", now.Add(time.Second).UTC().Format(time.RFC3339Nano), "discarded", 130, 25))
	newID := "33333333-3333-4333-8333-333333333333"
	head := filepath.Join(b.Root, strings.Replace(rel, ".jsonl", "_"+newID+".jsonl", 1))
	putProfileFile(t, head, profileMeta(profileConversation, profileConversation, len(first))+profileResponse("codex", now.Add(2*time.Second).UTC().Format(time.RFC3339Nano), "replacement", 120, 23))
	paths, err := artifactClosure(b.Root, head)
	if err != nil || len(paths) != 2 {
		t.Fatalf("closure %v %v", paths, err)
	}
	for _, path := range paths {
		if _, err = stageArtifact(state, b, a, path, profileConversation); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(a.Root, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE threads(id TEXT PRIMARY KEY,rollout_path TEXT,title TEXT);INSERT INTO threads VALUES(?,?,'unchanged')`, profileConversation, ap); err != nil {
		t.Fatal(err)
	}
	destHead := filepath.Join(a.Root, strings.TrimPrefix(head, b.Root+string(os.PathSeparator)))
	if err = retargetRollout(a.Root, profileConversation, ap, destHead); err != nil {
		t.Fatal(err)
	}
	if selected, err := selectedRollout(a.Root, profileConversation); err != nil || selected != destHead {
		t.Fatalf("stale selected head %s %v", selected, err)
	}
	if err = retargetRollout(a.Root, profileConversation, ap, "/wrong"); err == nil {
		t.Fatal("stale CAS overwrote selection")
	}
	var title string
	if err = db.QueryRow(`SELECT title FROM threads`).Scan(&title); err != nil || title != "unchanged" {
		t.Fatal("unrelated metadata changed")
	}
	appendProfileFile(t, destHead, profileResponse("codex", now.Add(3*time.Second).UTC().Format(time.RFC3339Nano), "a-new", 130, 25))
	m := profileMeter(t, state, []profile.Profile{a, b})
	defer m.Close()
	f := scanProfiles(t, m, now.Add(5*time.Second))
	if f.Today.Tokens.Input != 160 || f.Today.Tokens.Output != 30 {
		t.Fatalf("revert replay billing: %#v", f.Today.Tokens)
	}
	for _, s := range f.Sessions {
		if s.ID != profileConversation {
			t.Fatalf("rollout UUID confused with conversation: %#v", s)
		}
	}
}
func TestProfileWriterAndPublicationConflictsPreserveArtifacts(t *testing.T) {
	root := t.TempDir()
	release, err := writerLock(root, profileConversation)
	if err != nil {
		t.Fatal(err)
	}
	if unlock, err := writerLock(root, profileConversation); err == nil {
		unlock()
		t.Fatal("live native writer not excluded")
	}
	release()
	release, err = writerLock(root, profileConversation)
	if err != nil {
		t.Fatal(err)
	}
	release()
	state := t.TempDir()
	a := profile.Profile{Harness: "claude", ID: "a", Root: t.TempDir()}
	b := profile.Profile{Harness: "claude", ID: "b", Root: t.TempDir()}
	path := filepath.Join(a.Root, "projects", "p", "s.jsonl")
	dest := filepath.Join(b.Root, "projects", "p", "s.jsonl")
	putProfileFile(t, path, "original\n")
	putProfileFile(t, dest, "diverged\n")
	if _, err = stageArtifact(state, a, b, path, "s"); err == nil {
		t.Fatal("divergent immutable prefix overwritten")
	}
	data, _ := os.ReadFile(dest)
	if string(data) != "diverged\n" {
		t.Fatal("destination was modified")
	}
}
func TestAccountEpochsNeverRelabelHistoryAfterRelogin(t *testing.T) {
	for _, h := range []string{"claude", "codex"} {
		for _, first := range []string{"", h + "/user/a"} {
			t.Run(h+"/"+first, func(t *testing.T) {
				state := t.TempDir()
				p := profile.Profile{Harness: h, ID: "p", Root: t.TempDir(), Account: first}
				rel := filepath.Join("projects", "p", profileConversation+".jsonl")
				prefix := ""
				if h == "codex" {
					rel = filepath.Join("sessions", "rollout-2026-10-23T10-00-00-"+profileConversation+".jsonl")
					prefix = profileMeta(profileConversation, "", 0)
				}
				path := filepath.Join(p.Root, rel)
				now := time.Now()
				putProfileFile(t, path, prefix+profileResponse(h, now.UTC().Format(time.RFC3339Nano), "first", 100, 20))
				m := profileMeter(t, state, []profile.Profile{p})
				scanProfiles(t, m, now)
				p.Account = h + "/user/b"
				m.Profiles = []profile.Profile{p}
				in, out := 30, 5
				if h == "codex" {
					in, out = 130, 25
				}
				appendProfileFile(t, path, profileResponse(h, now.Add(time.Second).UTC().Format(time.RFC3339Nano), "second", in, out))
				check := func(f core.Fleet) {
					s := f.Sessions[profile.SessionKey(h, "p", profileConversation)]
					if s.ByAccount[first].Tokens.Input != 100 || s.ByAccount[p.Account].Tokens.Input != 30 || s.Account != "" {
						t.Fatalf("historical account relabeled: %#v", s)
					}
				}
				check(scanProfiles(t, m, now.Add(2*time.Second)))
				m.Close()
				for _, name := range []string{"ym.db", "ym.db-wal", "ym.db-shm"} {
					os.Remove(filepath.Join(state, name))
				}
				m = profileMeter(t, state, []profile.Profile{p})
				defer m.Close()
				check(scanProfiles(t, m, now.Add(3*time.Second)))
			})
		}
	}
}
