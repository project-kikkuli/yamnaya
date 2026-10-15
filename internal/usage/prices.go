// Package usage reads token usage and rate limits from Claude Code and Codex files, prices it at API rates and keeps it current incrementally.
package usage

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

//go:embed prices.json
var embeddedPrices []byte

const pricesURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// tiers are the prompt sizes, in thousands of tokens, past which a model may charge more per token.
var tiers = []int{100, 128, 200, 272}

// class is what besides token counts sets a response's price.
type class struct {
	Tier int
	Fast bool
	US   bool
}

func tierOf(prompt int64) int {
	t := 0
	for _, k := range tiers {
		if prompt > int64(k)*1000 {
			t = k
		}
	}
	return t
}

type model struct {
	rates map[string]float64
	mult  map[string]float64
	tiers []int
}

// Prices maps LiteLLM model names to per-token rates.
type Prices struct {
	models map[string]model
	cache  map[string]*model
}

var (
	keepRate  = regexp.MustCompile(`^(input_cost_per_token|output_cost_per_token|cache_read_input_token_cost|cache_creation_input_token_cost)(_above_1hr)?(_above_\d+k_tokens)?$`)
	tierKey   = regexp.MustCompile(`^input_cost_per_token_above_(\d+)k_tokens$`)
	dateTail  = regexp.MustCompile(`-\d{8}$`)
	keepModel = regexp.MustCompile(`^(claude-|gpt-|o\d|codex-)`)
)

// ParsePrices reads a LiteLLM price file, full or trimmed.
func ParsePrices(raw []byte) (*Prices, error) {
	var all map[string]map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	p := &Prices{models: map[string]model{}, cache: map[string]*model{}}
	for name, fields := range all {
		if strings.Contains(name, "/") || !keepModel.MatchString(name) {
			continue
		}
		m := model{rates: map[string]float64{}, mult: map[string]float64{}}
		for k, v := range fields {
			switch {
			case keepRate.MatchString(k):
				if f, ok := v.(float64); ok {
					m.rates[k] = f
				}
				if t := tierKey.FindStringSubmatch(k); t != nil {
					n, _ := strconv.Atoi(t[1])
					m.tiers = append(m.tiers, n)
				}
			case k == "provider_specific_entry":
				if mm, ok := v.(map[string]any); ok {
					for mk, mv := range mm {
						if f, ok := mv.(float64); ok {
							m.mult[mk] = f
						}
					}
				}
			}
		}
		if _, ok := m.rates["input_cost_per_token"]; !ok {
			continue
		}
		sort.Ints(m.tiers)
		p.models[name] = m
	}
	if len(p.models) == 0 {
		return nil, fmt.Errorf("no priced models")
	}
	return p, nil
}

// Trim keeps only what ParsePrices reads, so the snapshot stays small.
func Trim(raw []byte) ([]byte, error) {
	p, err := ParsePrices(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]any{}
	for name, m := range p.models {
		e := map[string]any{}
		for k, v := range m.rates {
			e[k] = v
		}
		if len(m.mult) > 0 {
			e["provider_specific_entry"] = m.mult
		}
		out[name] = e
	}
	return json.MarshalIndent(out, "", " ")
}

func (p *Prices) lookup(name string) *model {
	if m, ok := p.cache[name]; ok {
		return m
	}
	var found *model
	base := name
	if i := strings.IndexByte(base, '['); i > 0 {
		base = base[:i]
	}
	for _, n := range []string{base, dateTail.ReplaceAllString(base, "")} {
		if m, ok := p.models[n]; ok {
			found = &m
			break
		}
	}
	p.cache[name] = found
	return found
}

func (m *model) rate(key string, tier int) float64 {
	if tier > 0 {
		if r, ok := m.rates[fmt.Sprintf("%s_above_%dk_tokens", key, tier)]; ok {
			return r
		}
	}
	return m.rates[key]
}

// Cost prices one response or a sum of responses that share a model and class.
func (p *Prices) Cost(name string, t core.Tokens, c class) (float64, bool) {
	m := p.lookup(name)
	if m == nil {
		return 0, false
	}
	tier := 0
	for _, k := range m.tiers {
		if k <= c.Tier {
			tier = k
		}
	}
	in := m.rate("input_cost_per_token", tier)
	read, ok := m.rates["cache_read_input_token_cost"]
	if ok {
		read = m.rate("cache_read_input_token_cost", tier)
	} else {
		read = in
	}
	write5, ok := m.rates["cache_creation_input_token_cost"]
	if ok {
		write5 = m.rate("cache_creation_input_token_cost", tier)
	} else {
		write5 = in
	}
	write1, ok := m.rates["cache_creation_input_token_cost_above_1hr"]
	if ok {
		write1 = m.rate("cache_creation_input_token_cost_above_1hr", tier)
	} else {
		write1 = 2 * in
	}
	usd := float64(t.Input)*in + float64(t.Output)*m.rate("output_cost_per_token", tier) +
		float64(t.CacheRead)*read + float64(t.CacheWrite5m)*write5 + float64(t.CacheWrite1h)*write1
	if c.US {
		usd *= multiplier(m.mult, "us", 1.1)
	}
	if c.Fast {
		usd *= multiplier(m.mult, "fast", 1)
	}
	return usd, true
}

func multiplier(m map[string]float64, key string, fallback float64) float64 {
	if v, ok := m[key]; ok && v > 0 {
		return v
	}
	return fallback
}
