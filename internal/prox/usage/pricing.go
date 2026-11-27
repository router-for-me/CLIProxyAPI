// Package usage keeps a token and API-equivalent cost ledger per account,
// model, and client, built from CPA usage records.
package usage

import (
	"sort"
	"strings"

	"github.com/MoeForge/moego/regexp"
)

// Price is USD per million tokens. CacheRead and CacheWrite default to
// 0.1x and 1.25x Input when zero.
type Price struct {
	Input      float64 `yaml:"input" json:"input"`
	Output     float64 `yaml:"output" json:"output"`
	CacheRead  float64 `yaml:"cache_read" json:"cache_read"`
	CacheWrite float64 `yaml:"cache_write" json:"cache_write"`
}

func (p Price) cacheRead() float64 {
	if p.CacheRead > 0 {
		return p.CacheRead
	}
	return p.Input * 0.1
}

func (p Price) cacheWrite() float64 {
	if p.CacheWrite > 0 {
		return p.CacheWrite
	}
	return p.Input * 1.25
}

// DefaultPrices are first-party API list prices (USD per 1M tokens) keyed by
// model ID prefix; the longest matching prefix wins. Subscription traffic is
// not billed this way: the numbers show what the same traffic would cost on
// the API. Models without an entry are counted but not priced; add them under
// `usage.pricing` in the prox settings.
var DefaultPrices = map[string]Price{
	// Anthropic (Claude API pricing as of 2026-11).
	"claude-fable-5-2":  {Input: 8, Output: 40, CacheRead: 0.20},
	"claude-mythos-5-2": {Input: 8, Output: 40, CacheRead: 0.20},
	"claude-fable-5-1":  {Input: 10, Output: 50},
	"claude-mythos-5-1": {Input: 10, Output: 50},
	"claude-fable-5":    {Input: 10, Output: 50},
	"claude-opus-5-5":   {Input: 4, Output: 20, CacheRead: 0.20},
	"claude-opus-5":     {Input: 5, Output: 25},
	"claude-opus-4-8":   {Input: 5, Output: 25},
	"claude-opus-4-7":   {Input: 5, Output: 25},
	"claude-opus-4-6":   {Input: 5, Output: 25},
	"claude-opus-4-5":   {Input: 5, Output: 25},
	"claude-sonnet-5-5": {Input: 2, Output: 10, CacheRead: 0.10},
	"claude-sonnet-5":   {Input: 2, Output: 10},
	"claude-sonnet-4":   {Input: 3, Output: 15},
	"claude-haiku-5-5":  {Input: 0.10, Output: 0.50},
	"claude-haiku-4-5":  {Input: 1, Output: 5},
	// OpenAI (published GPT-5 family prices; newer models need overrides).
	"gpt-5":      {Input: 1.25, Output: 10, CacheRead: 0.125},
	"gpt-5-mini": {Input: 0.25, Output: 2, CacheRead: 0.025},
	"gpt-5-nano": {Input: 0.05, Output: 0.40, CacheRead: 0.005},
}

var (
	dateSuffix   = regexp.MustCompile(`-\d{8}$`)
	effortSuffix = regexp.MustCompile(`\([a-z]+\)$`)
)

// NormalizeModel strips date snapshots, thinking-effort suffixes, and
// provider prefixes so model IDs match price keys.
func NormalizeModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	m = effortSuffix.ReplaceAllString(m, "")
	m = strings.TrimSuffix(m, "-thinking")
	m = dateSuffix.ReplaceAllString(m, "")
	return m
}

// Pricer resolves prices with overrides layered over defaults.
type Pricer struct {
	keys   []string
	prices map[string]Price
}

// NewPricer merges overrides into DefaultPrices.
func NewPricer(overrides map[string]Price) *Pricer {
	p := &Pricer{prices: map[string]Price{}}
	for k, v := range DefaultPrices {
		p.prices[k] = v
	}
	for k, v := range overrides {
		p.prices[NormalizeModel(k)] = v
	}
	for k := range p.prices {
		p.keys = append(p.keys, k)
	}
	// Longest prefix first.
	sort.Slice(p.keys, func(i, j int) bool { return len(p.keys[i]) > len(p.keys[j]) })
	return p
}

// Lookup returns the price for a model.
func (p *Pricer) Lookup(model string) (Price, bool) {
	m := NormalizeModel(model)
	for _, k := range p.keys {
		if m == k || strings.HasPrefix(m, k+"-") {
			return p.prices[k], true
		}
	}
	return Price{}, false
}

// Cost returns USD for one record's tokens.
func (p Price) Cost(t Tokens) float64 {
	return (float64(t.Input)*p.Input + float64(t.Output)*p.Output +
		float64(t.CacheRead)*p.cacheRead() + float64(t.CacheWrite)*p.cacheWrite()) / 1e6
}
