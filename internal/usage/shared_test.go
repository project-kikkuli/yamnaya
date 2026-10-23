package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func TestUsageMeterProcess(t *testing.T) {
	state := os.Getenv("YM_USAGE_TEST_STATE")
	if state == "" {
		return
	}
	m := &Meter{StateDir: state, Every: time.Hour}
	defer m.Close()
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var ps []profile.Profile
		if err := dec.Decode(&ps); err == io.EOF {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		m.Profiles = ps
		if _, err := m.Scan(ts); err != nil {
			t.Fatal(err)
		}
		if err := enc.Encode(m.Fleet(ts)); err != nil {
			t.Fatal(err)
		}
	}
}

func usageProcess(t *testing.T, state string) (func([]profile.Profile), func() core.Fleet, func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestUsageMeterProcess$")
	cmd.Env = append(os.Environ(), "YM_USAGE_TEST_STATE="+state)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := false
	t.Cleanup(func() {
		if !done {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	enc, dec := json.NewEncoder(in), json.NewDecoder(out)
	return func(ps []profile.Profile) {
			t.Helper()
			if err := enc.Encode(ps); err != nil {
				t.Fatal(err)
			}
		}, func() core.Fleet {
			t.Helper()
			var f core.Fleet
			if err := dec.Decode(&f); err != nil {
				t.Fatalf("worker Fleet: %v; stderr: %s", err, stderr.String())
			}
			return f
		}, func() {
			t.Helper()
			in.Close()
			io.Copy(io.Discard, out)
			err := cmd.Wait()
			done = true
			if err != nil {
				t.Fatalf("worker: %v; stderr: %s", err, stderr.String())
			}
		}
}

func TestMetersShareCommittedUsageAndOwnershipAcrossProcesses(t *testing.T) {
	for _, h := range []string{claudeName, codexName} {
		t.Run(h, func(t *testing.T) {
			state := t.TempDir()
			p := profile.Profile{Harness: h, ID: "p", Root: t.TempDir(), Account: "account-a"}
			ps := []profile.Profile{p}
			id := parentID
			path := filepath.Join(p.Root, "projects", "p", id+".jsonl")
			first := []string{reply("first", "claude-sonnet-5", 100, 20, 0, 0, 0)}
			suffix := func(id string, in, out int64) []string {
				return []string{reply(id, "claude-sonnet-5", in, out, 0, 0, 0)}
			}
			if h == codexName {
				id = threadA
				path = filepath.Join(p.Root, "sessions", "rollout-2026-10-15T12-00-00-"+id+".jsonl")
				first = rollout(id, "", "gpt-5.5", [3]int64{100, 0, 20})
				suffix = func(_ string, in, out int64) []string {
					return rollout(id, "", "gpt-5.5", [3]int64{in, 0, out})[2:]
				}
			}
			write(t, path, first...)
			sendA, readA, closeA := usageProcess(t, state)
			sendB, readB, closeB := usageProcess(t, state)
			check := func(f core.Fleet, in, out int64) {
				t.Helper()
				want := core.Tokens{Input: in, Output: out}
				if f.Today.Tokens != want || f.Week.Tokens != want {
					t.Fatalf("Fleet today %+v week %+v; want %+v", f.Today.Tokens, f.Week.Tokens, want)
				}
			}
			sendA(ps)
			check(readA(), 100, 20)
			sendB(ps)
			check(readB(), 100, 20)
			if h == claudeName {
				write(t, path, suffix("second", 30, 5)...)
			} else {
				write(t, path, suffix("second", 130, 25)...)
			}
			sendA(ps)
			sendB(ps)
			check(readA(), 130, 25)
			check(readB(), 130, 25)
			if h == claudeName {
				write(t, path, suffix("second", 30, 9)...)
				write(t, path, suffix("third", 10, 2)...)
			} else {
				write(t, path, suffix("third", 140, 31)...)
			}
			sendA(ps)
			check(readA(), 140, 31)
			sendB(ps)
			check(readB(), 140, 31)
			if h == codexName {
				write(t, path,
					fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex","primary":{"used_percent":100,"window_minutes":300,"resets_at":%d}}}}`+"\n", ts.Add(time.Minute).Format(time.RFC3339), ts.Add(time.Hour).Unix()),
					fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"error","codex_error_info":"UsageLimitExceeded"}}`+"\n", ts.Add(2*time.Minute).Format(time.RFC3339)))
				for _, worker := range []struct {
					send func([]profile.Profile)
					read func() core.Fleet
				}{{sendA, readA}, {sendB, readB}} {
					worker.send(ps)
					f := worker.read()
					if len(f.Limits) != 1 || f.Limits[0].Used != 100 || f.Limits[0].Account != p.Account || len(f.Exhaustions) != 1 || f.Exhaustions[0].Account != p.Account {
						t.Fatalf("stale quota/exhaustion state: limits %+v, exhaustion %+v", f.Limits, f.Exhaustions)
					}
				}
			}

			for i, other := range []string{otherID, threadC} {
				otherPath := filepath.Join(p.Root, "projects", "p", other+".jsonl")
				lines := []string{reply(fmt.Sprint("new-", i), "claude-sonnet-5", 7, 1, 0, 0, 0)}
				if h == codexName {
					otherPath = filepath.Join(p.Root, "sessions", "rollout-2026-10-15T12-00-00-"+other+".jsonl")
					lines = rollout(other, "", "gpt-5.5", [3]int64{7, 0, 1})
				}
				write(t, otherPath, lines...)
				if i == 0 {
					sendA(ps)
					check(readA(), 147, 32)
				} else {
					sendB(ps)
					check(readB(), 154, 33)
				}
			}

			p.Account = "account-b"
			ps[0] = p
			if h == claudeName {
				write(t, path, suffix("fourth", 20, 4)...)
			} else {
				write(t, path, suffix("fourth", 160, 35)...)
			}
			sendA(ps)
			check(readA(), 174, 37)
			sendB(ps)
			check(readB(), 174, 37)
			q := profile.Profile{Harness: h, ID: "q", Root: t.TempDir(), Account: "account-c"}
			rel, err := filepath.Rel(p.Root, path)
			if err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(q.Root, rel)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			write(t, dest, string(data))
			end := int64(len(data))
			hash, err := profile.HashRange(dest, 0, end)
			if err != nil {
				t.Fatal(err)
			}
			origins, err := TransferOrigins(state, path, p, 0, end)
			if err != nil {
				t.Fatal(err)
			}
			imp := profile.Import{Start: 0, End: end, Hash: hash, Origins: origins, SourceRoot: p.Root, SourcePath: path, SourceProfile: p.ID, SourceAccount: p.Account}
			if h == codexName {
				imp.Seed, err = ImportSeed(path, end)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := profile.SaveImports(state, dest, []profile.Import{imp}); err != nil {
				t.Fatal(err)
			}
			if h == claudeName {
				write(t, dest, suffix("destination", 11, 3)...)
			} else {
				write(t, dest, suffix("destination", 171, 38)...)
			}
			ps = append(ps, q)
			checkOwnership := func(f core.Fleet) {
				t.Helper()
				check(f, 185, 40)
				s := f.Sessions[profile.SessionKey(h, p.ID, id)]
				if s.ByAccount["account-a"].Tokens != (core.Tokens{Input: 140, Output: 31}) || s.ByAccount["account-b"].Tokens != (core.Tokens{Input: 20, Output: 4}) || s.Account != "" {
					t.Fatalf("source account epochs: %+v", s)
				}
				d := f.Sessions[profile.SessionKey(h, q.ID, id)]
				if d.Own.Tokens != (core.Tokens{Input: 11, Output: 3}) || d.Account != q.Account {
					t.Fatalf("imported history charged to destination: %+v", d)
				}
			}
			sendA(ps)
			sendB(ps)
			checkOwnership(readA())
			checkOwnership(readB())
			closeA()
			closeB()
			for _, rebuild := range []bool{false, true} {
				if rebuild {
					for _, name := range []string{"ym.db", "ym.db-wal", "ym.db-shm"} {
						if err := os.Remove(filepath.Join(state, name)); err != nil && !os.IsNotExist(err) {
							t.Fatal(err)
						}
					}
				}
				m := &Meter{StateDir: state, Profiles: ps, Every: time.Hour}
				checkOwnership(scan(t, m))
				for _, table := range []string{"ym_usage_sums", "ym_usage_slices", "ym_usage_responses"} {
					var in, out int64
					if err := m.ledger.db.QueryRow("SELECT sum(input), sum(output) FROM "+table).Scan(&in, &out); err != nil {
						t.Fatal(err)
					}
					if in != 185 || out != 40 {
						t.Errorf("%s durable totals %d/%d, want 185/40", table, in, out)
					}
				}
				m.Close()
			}
		})
	}
}
