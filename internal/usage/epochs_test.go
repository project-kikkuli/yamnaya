package usage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func TestUnobservedIdentityIntervalsRemainUnknownAcrossRestarts(t *testing.T) {
	for _, h := range []string{claudeName, codexName} {
		for _, firstAccount := range []string{"account-a", ""} {
			for _, partial := range []bool{false, true} {
				name := h + "/" + firstAccount + "/complete"
				if partial {
					name = h + "/" + firstAccount + "/partial"
				}
				t.Run(name, func(t *testing.T) {
					p := profile.Profile{Harness: h, ID: "p", Root: t.TempDir(), Account: firstAccount}
					state := t.TempDir()
					id := parentID
					path := filepath.Join(p.Root, "projects", "p", id+".jsonl")
					initial := []string{reply("initial", "claude-sonnet-5", 100, 20, 0, 0, 0)}
					event := fmt.Sprintf(`{"timestamp":%q,"type":"assistant","isApiErrorMessage":true,"error":"rate_limit","message":{"model":"<synthetic>"}}`+"\n", ts.Add(time.Minute).Format(time.RFC3339))
					a, b, later := reply("a", "claude-sonnet-5", 30, 5, 0, 0, 0), reply("b", "claude-sonnet-5", 20, 4, 0, 0, 0), reply("later", "claude-sonnet-5", 11, 3, 0, 0, 0)
					if h == codexName {
						id = threadA
						path = filepath.Join(p.Root, "sessions", "rollout-2026-10-15T12-00-00-"+id+".jsonl")
						initial = rollout(id, "", "gpt-5.5", [3]int64{100, 0, 20})
						event = fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"error","codex_error_info":"UsageLimitExceeded"}}`+"\n", ts.Add(time.Minute).Format(time.RFC3339))
						a = rollout(id, "", "gpt-5.5", [3]int64{130, 0, 25})[2]
						b = rollout(id, "", "gpt-5.5", [3]int64{150, 0, 29})[2]
						later = rollout(id, "", "gpt-5.5", [3]int64{161, 0, 32})[2]
					}
					write(t, path, initial...)
					write(t, path, event)
					m := &Meter{StateDir: state, Profiles: []profile.Profile{p}, Every: time.Hour}
					t.Cleanup(func() { m.Close() })
					scan(t, m)
					write(t, path, a)
					p.Account = "account-b"
					m.Profiles = []profile.Profile{p}
					if partial {
						write(t, path, b[:len(b)/2])
						f := scan(t, m)
						s := f.Sessions[profile.SessionKey(h, p.ID, id)]
						unknown := core.Tokens{Input: 30, Output: 5}
						if firstAccount == "" {
							unknown = unknown.Plus(core.Tokens{Input: 100, Output: 20})
						}
						if s.ByAccount[""].Tokens != unknown || s.ByAccount[p.Account].Tokens.Total() != 0 {
							t.Errorf("partial interval invented a payer: %+v", s.ByAccount)
						}
						m.Close()
						m = &Meter{StateDir: state, Profiles: []profile.Profile{p}, Every: time.Hour}
						write(t, path, b[len(b)/2:], later)
					} else {
						write(t, path, b)
						scan(t, m)
						write(t, path, later)
					}
					check := func(f core.Fleet) {
						t.Helper()
						s := f.Sessions[profile.SessionKey(h, p.ID, id)]
						want := map[string]core.Tokens{"": {Input: 50, Output: 9}, "account-b": {Input: 11, Output: 3}}
						want[firstAccount] = want[firstAccount].Plus(core.Tokens{Input: 100, Output: 20})
						for account, tokens := range want {
							if s.ByAccount[account].Tokens != tokens {
								t.Errorf("payer %q got %+v want %+v", account, s.ByAccount[account].Tokens, tokens)
							}
						}
						if s.Total.Tokens != (core.Tokens{Input: 161, Output: 32}) {
							t.Errorf("lost charges: %+v", s.Total.Tokens)
						}
						if len(f.Exhaustions) != 1 || f.Exhaustions[0].Account != firstAccount {
							t.Errorf("prior exhaustion owner changed: %+v", f.Exhaustions)
						}
					}
					check(scan(t, m))
					m.Close()
					for _, rebuild := range []bool{false, true} {
						if rebuild {
							for _, name := range []string{"ym.db", "ym.db-wal", "ym.db-shm"} {
								if err := os.Remove(filepath.Join(state, name)); err != nil && !os.IsNotExist(err) {
									t.Fatal(err)
								}
							}
						}
						m = &Meter{StateDir: state, Profiles: []profile.Profile{p}, Every: time.Hour}
						check(scan(t, m))
						m.Close()
					}
				})
			}
		}
	}
}
