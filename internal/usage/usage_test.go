package usage

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

const (
	parentID = "11111111-aaaa-4aaa-8aaa-111111111111"
	otherID  = "22222222-bbbb-4bbb-8bbb-222222222222"
	threadA  = "0190aaaa-1111-7111-8111-111111111111"
	threadB  = "0190bbbb-2222-7222-8222-222222222222"
	threadC  = "0190cccc-3333-7333-8333-333333333333"
)

var ts = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func write(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "")); err != nil {
		t.Fatal(err)
	}
}

// reply is one content-block line of a Claude response.
func reply(id, model string, in, out, read, w5, w1 int64) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":%q,"model":%q,"content":[{"type":"text","text":"\"usage\":{} decoy"}],"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":%d},"speed":"standard","inference_geo":"global"}}}`+"\n",
		ts.Format(time.RFC3339), id, model, in, out, read, w5+w1, w5, w1)
}

func meter(t *testing.T) *Meter {
	t.Helper()
	root := t.TempDir()
	m := &Meter{ClaudeDir: filepath.Join(root, "claude"), CodexHome: filepath.Join(root, "codex"), StateDir: filepath.Join(root, "state"), Every: time.Hour}
	if err := os.MkdirAll(m.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func scan(t *testing.T, m *Meter) core.Fleet {
	t.Helper()
	if _, err := m.Scan(ts); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fleet
}

func TestClaudeCountsEachResponseOnceAcrossBlocksFilesAndScans(t *testing.T) {
	session := func(m *Meter, id string) string { return filepath.Join(m.ClaudeDir, "projects", "-Users-me-app", id) }
	cases := []struct {
		name       string
		files      map[string][]string
		append     map[string][]string
		own, total core.Tokens
	}{
		{
			name: "content blocks repeat usage and later blocks carry the final output",
			files: map[string][]string{".jsonl": {
				reply("msg_1", "claude-sonnet-5", 3, 1, 100, 10, 0),
				reply("msg_1", "claude-sonnet-5", 3, 40, 100, 10, 0),
				`{"type":"user","message":{"content":"next"}}` + "\n",
				reply("msg_2", "claude-sonnet-5", 2, 5, 200, 0, 7),
			}},
			own:   core.Tokens{Input: 5, Output: 45, CacheRead: 300, CacheWrite5m: 10, CacheWrite1h: 7},
			total: core.Tokens{Input: 5, Output: 45, CacheRead: 300, CacheWrite5m: 10, CacheWrite1h: 7},
		},
		{
			name: "subagents and workflow agents roll into their session, and a fork's copied history counts once",
			files: map[string][]string{
				".jsonl":                                   {reply("msg_1", "claude-opus-5-5", 1, 10, 0, 0, 0)},
				"/subagents/agent-a1.jsonl":                {reply("msg_2", "claude-sonnet-5", 1, 20, 0, 0, 0)},
				"/subagents/agent-a2.jsonl":                {reply("msg_2", "claude-sonnet-5", 1, 20, 0, 0, 0), reply("msg_3", "claude-sonnet-5", 1, 30, 0, 0, 0)},
				"/subagents/workflows/wf_1/agent-w1.jsonl": {reply("msg_4", "claude-sonnet-5", 1, 40, 0, 0, 0)},
				"/subagents/agent-a1.meta.json":            {`{"agentType":"general-purpose"}`},
			},
			own:   core.Tokens{Input: 1, Output: 10},
			total: core.Tokens{Input: 4, Output: 100},
		},
		{
			name:   "a response split across two scans counts its growth, not twice",
			files:  map[string][]string{".jsonl": {reply("msg_1", "claude-sonnet-5", 3, 1, 100, 0, 0)}},
			append: map[string][]string{".jsonl": {reply("msg_1", "claude-sonnet-5", 3, 9, 100, 0, 0), reply("msg_2", "claude-sonnet-5", 1, 1, 0, 0, 0)}},
			own:    core.Tokens{Input: 4, Output: 10, CacheRead: 100},
			total:  core.Tokens{Input: 4, Output: 10, CacheRead: 100},
		},
		{
			name:   "a half-written line waits for its newline",
			files:  map[string][]string{".jsonl": {reply("msg_1", "claude-sonnet-5", 1, 1, 0, 0, 0), strings.TrimSuffix(reply("msg_2", "claude-sonnet-5", 1, 1, 0, 0, 0), "\n")}},
			append: map[string][]string{".jsonl": {"\n"}},
			own:    core.Tokens{Input: 2, Output: 2},
			total:  core.Tokens{Input: 2, Output: 2},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := meter(t)
			for suffix, lines := range c.files {
				write(t, session(m, parentID)+suffix, lines...)
			}
			scan(t, m)
			for suffix, lines := range c.append {
				write(t, session(m, parentID)+suffix, lines...)
			}
			f := scan(t, m)
			got := f.Sessions[parentID]
			if got.Own.Tokens != c.own || got.Total.Tokens != c.total {
				t.Errorf("own %+v total %+v, want own %+v total %+v", got.Own.Tokens, got.Total.Tokens, c.own, c.total)
			}
			reopened := &Meter{ClaudeDir: m.ClaudeDir, CodexHome: m.CodexHome, StateDir: m.StateDir}
			defer reopened.Close()
			if again := scan(t, reopened).Sessions[parentID]; again.Total.Tokens != c.total {
				t.Errorf("after reopening the ledger total %+v, want %+v", again.Total.Tokens, c.total)
			}
		})
	}
}

func TestClaudeCostStateIsReported(t *testing.T) {
	m := meter(t)
	path := filepath.Join(m.ClaudeDir, "projects", "-Users-me-app", otherID+".jsonl")
	write(t, path,
		`{"type":"cost-state","sessionId":"`+otherID+`","totalCostUSD":1.5}`+"\n",
		reply("msg_1", "claude-sonnet-5", 1, 1, 0, 0, 0),
		`{"type":"cost-state","sessionId":"`+otherID+`","totalCostUSD":2.25}`+"\n")
	if got := scan(t, m).Sessions[otherID].Reported; got != 2.25 {
		t.Errorf("reported = %v, want the latest running total 2.25", got)
	}
}

func rollout(id, parent, model string, totals ...[3]int64) []string {
	src := `"vscode"`
	if parent != "" {
		src = `{"subagent":{"thread_spawn":{"parent_thread_id":"` + parent + `","depth":1}}}`
	}
	lines := []string{
		`{"timestamp":"2026-10-15T12:00:00Z","type":"session_meta","payload":{"id":"` + id + `","source":` + src + `}}` + "\n",
		`{"timestamp":"2026-10-15T12:00:01Z","type":"turn_context","payload":{"model":"` + model + `"}}` + "\n",
	}
	for _, t := range totals {
		lines = append(lines, fmt.Sprintf(`{"timestamp":"2026-10-15T12:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d},"last_token_usage":{"input_tokens":%d}}}}`+"\n", t[0], t[1], t[2], t[0]))
	}
	return lines
}

func TestCodexKeepsLatestTotalsAndRollsChildThreadsIntoParents(t *testing.T) {
	m := meter(t)
	path := func(id string) string {
		return filepath.Join(m.CodexHome, "sessions", "2026", "10", "15", "rollout-2026-10-15T12-00-00-"+id+".jsonl")
	}
	write(t, path(threadA), rollout(threadA, "", "gpt-5.5", [3]int64{100, 40, 10}, [3]int64{100, 40, 10}, [3]int64{250, 140, 30})...)
	write(t, path(threadB), rollout(threadB, threadA, "gpt-5.5", [3]int64{50, 0, 5})...)
	write(t, path(threadC), rollout(threadC, threadB, "gpt-5.5", [3]int64{20, 10, 2})...)
	f := scan(t, m)
	a, b := f.Sessions[threadA], f.Sessions[threadB]
	if want := (core.Tokens{Input: 110, CacheRead: 140, Output: 30}); a.Own.Tokens != want {
		t.Errorf("parent own %+v, want %+v (cached input is part of input)", a.Own.Tokens, want)
	}
	if want := (core.Tokens{Input: 110 + 50 + 10, CacheRead: 140 + 0 + 10, Output: 30 + 5 + 2}); a.Total.Tokens != want {
		t.Errorf("parent total %+v, want %+v", a.Total.Tokens, want)
	}
	if want := (core.Tokens{Input: 60, CacheRead: 10, Output: 7}); b.Total.Tokens != want || b.Parent != threadA {
		t.Errorf("child total %+v parent %q, want %+v under %s", b.Total.Tokens, b.Parent, want, threadA)
	}
	write(t, path(threadA), rollout(threadA, "", "gpt-5.5", [3]int64{300, 140, 40})[2:]...)
	if got := scan(t, m).Sessions[threadA].Own.Tokens; got != (core.Tokens{Input: 160, CacheRead: 140, Output: 40}) {
		t.Errorf("after an appended turn own %+v", got)
	}
}

const testPrices = `{
 "claude-sonnet-5": {"input_cost_per_token": 2e-6, "output_cost_per_token": 1e-5, "cache_read_input_token_cost": 2e-7,
  "cache_creation_input_token_cost": 2.5e-6, "cache_creation_input_token_cost_above_1hr": 4e-6, "provider_specific_entry": {"us": 1.1}},
 "claude-opus-5-5": {"input_cost_per_token": 4e-6, "output_cost_per_token": 2e-5, "provider_specific_entry": {"fast": 2}},
 "claude-haiku-5-5": {"input_cost_per_token": 1e-7, "output_cost_per_token": 5e-7, "input_cost_per_token_above_100k_tokens": 5e-7,
  "output_cost_per_token_above_100k_tokens": 2.5e-6, "cache_read_input_token_cost": 1e-8, "cache_read_input_token_cost_above_100k_tokens": 5e-8},
 "gpt-5.5": {"input_cost_per_token": 5e-6, "output_cost_per_token": 3e-5, "cache_read_input_token_cost": 5e-7,
  "input_cost_per_token_above_272k_tokens": 1e-5, "output_cost_per_token_above_272k_tokens": 4.5e-5, "cache_read_input_token_cost_above_272k_tokens": 1e-6}
}`

func TestPricing(t *testing.T) {
	p, err := ParsePrices([]byte(testPrices))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		model  string
		tokens core.Tokens
		class  class
		want   float64
		priced bool
	}{
		{"5m and 1h cache writes take their own rates", "claude-sonnet-5", core.Tokens{CacheWrite5m: 1e6, CacheWrite1h: 1e6}, class{}, 2.5 + 4, true},
		{"a context-window suffix is ignored", "claude-sonnet-5[1m]", core.Tokens{Input: 1e6}, class{}, 2, true},
		{"us inference costs LiteLLM's multiplier", "claude-sonnet-5", core.Tokens{Output: 1e6}, class{US: true}, 11, true},
		{"us inference defaults to 1.1x without one", "claude-opus-5-5", core.Tokens{Output: 1e6}, class{US: true}, 22, true},
		{"fast mode uses LiteLLM's multiplier", "claude-opus-5-5", core.Tokens{Output: 1e6}, class{Fast: true}, 40, true},
		{"1h writes default to twice input without a rate", "claude-opus-5-5", core.Tokens{CacheWrite1h: 1e6}, class{}, 8, true},
		{"haiku under 100k uses base rates", "claude-haiku-5-5", core.Tokens{Input: 1e6, CacheRead: 1e6}, class{Tier: 0}, 0.1 + 0.01, true},
		{"haiku past 100k uses the higher tier", "claude-haiku-5-5", core.Tokens{Input: 1e6, CacheRead: 1e6}, class{Tier: tierOf(150_000)}, 0.5 + 0.05, true},
		{"openai past 272k uses the higher tier", "gpt-5.5", core.Tokens{Input: 1e6, CacheRead: 1e6, Output: 1e6}, class{Tier: tierOf(300_000)}, 10 + 1 + 45, true},
		{"openai at 200k stays on base rates", "gpt-5.5", core.Tokens{Input: 1e6}, class{Tier: tierOf(200_001)}, 5, true},
		{"unknown models stay unpriced", "gpt-reserve", core.Tokens{Input: 1e6}, class{}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := p.Cost(c.model, c.tokens, c.class)
			if ok != c.priced || math.Abs(got-c.want) > 1e-9 {
				t.Errorf("Cost = %v, %v; want %v, %v", got, ok, c.want, c.priced)
			}
		})
	}
}

func TestLimitsDropExpiredWindowsAndKeepTheLatest(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := []struct {
		name   string
		claude string
		codex  map[string][]string
		want   []string
	}{
		{
			name:   "claude statusline windows, one already reset",
			claude: fmt.Sprintf(`{"model":{"id":"x"},"rate_limits":{"five_hour":{"used_percentage":42.5,"resets_at":%d},"seven_day":{"used_percentage":12,"resets_at":%d}}}`, now.Add(-time.Minute).Unix(), now.Add(48*time.Hour).Unix()),
			want:   []string{"claude 7d 12"},
		},
		{
			name: "codex windows by limit, latest report wins",
			codex: map[string][]string{threadA: {
				fmt.Sprintf(`{"timestamp":"2026-10-15T12:00:00Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex","primary":{"used_percent":5,"window_minutes":299,"resets_at":%d},"secondary":{"used_percent":30,"window_minutes":10079,"resets_at":%d},"plan_type":"pro"}}}`+"\n", now.Add(time.Hour).Unix(), now.Add(72*time.Hour).Unix()),
				fmt.Sprintf(`{"timestamp":"2026-10-15T12:05:00Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex","primary":{"used_percent":9,"window_minutes":299,"resets_at":%d},"secondary":null,"plan_type":"pro"}}}`+"\n", now.Add(time.Hour).Unix()),
				fmt.Sprintf(`{"timestamp":"2026-10-15T12:06:00Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"premium","primary":{"used_percent":80,"window_minutes":10080,"resets_at":%d}}}}`+"\n", now.Add(-time.Hour).Unix()),
			}},
			want: []string{"codex codex 5h 9", "codex codex 7d 30"},
		},
		{
			name: "the latest report wins across rollouts read in one scan",
			codex: map[string][]string{
				threadA: {fmt.Sprintf(`{"timestamp":"2026-10-15T12:10:00Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex","primary":{"used_percent":40,"window_minutes":299,"resets_at":%d}}}}`+"\n", now.Add(time.Hour).Unix())},
				threadB: {fmt.Sprintf(`{"timestamp":"2026-10-15T12:00:00Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex","primary":{"used_percent":10,"window_minutes":299,"resets_at":%d}}}}`+"\n", now.Add(time.Hour).Unix())},
			},
			want: []string{"codex codex 5h 40"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := meter(t)
			if c.claude != "" {
				if err := RecordClaudeLimits(m.StateDir, strings.NewReader(c.claude), now.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
				if err := RecordClaudeLimits(m.StateDir, strings.NewReader(`{"model":{"id":"x"}}`), now); err != nil {
					t.Fatal(err)
				}
			}
			for thread, lines := range c.codex {
				write(t, filepath.Join(m.CodexHome, "sessions", "rollout-2026-10-15T12-00-00-"+thread+".jsonl"), lines...)
			}
			reopened := &Meter{ClaudeDir: m.ClaudeDir, CodexHome: m.CodexHome, StateDir: m.StateDir, Every: time.Hour}
			t.Cleanup(func() { reopened.Close() })
			for _, mm := range []*Meter{m, reopened} {
				if _, err := mm.Scan(now); err != nil {
					t.Fatal(err)
				}
				var got []string
				for _, l := range mm.Fleet(now).Limits {
					got = append(got, fmt.Sprintf("%s %s %.0f", l.Harness, l.Name, l.Used))
				}
				if strings.Join(got, ",") != strings.Join(c.want, ",") {
					t.Errorf("limits = %v, want %v", got, c.want)
				}
			}
		})
	}
}
