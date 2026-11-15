package pricing

import (
	"regexp"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// Price sources reported in listings and on usage records.
const (
	SourceUpstream = "upstream"
	SourceHeader   = "header"
	SourceConfig   = "config"
	SourceCatalog  = "catalog"
	SourceUnknown  = "unknown"
)

// firstPartyProviders are preferred, in order, when a model ID is found under several
// catalog providers and the serving provider has no direct catalog entry.
var firstPartyProviders = []string{
	"anthropic", "openai", "google", "xai", "deepseek", "mistral", "moonshotai",
	"groq", "openrouter",
}

// Match is a resolved model price.
type Match struct {
	Price Price
	// Source is SourceConfig or SourceCatalog.
	Source string
	// Override reports a config entry with override: true.
	Override bool
}

type configEntry struct {
	model    string
	prefix   bool
	provider string
	price    Price
	override bool
}

func buildConfigEntries(models []config.ProxxModelPrice) []configEntry {
	entries := make([]configEntry, 0, len(models))
	for _, m := range models {
		name := strings.ToLower(strings.TrimSpace(m.Model))
		if name == "" {
			continue
		}
		entry := configEntry{
			provider: strings.ToLower(strings.TrimSpace(m.Provider)),
			price: Price{
				Input:      cloneFloat(m.Input),
				Output:     cloneFloat(m.Output),
				CacheRead:  cloneFloat(m.CacheRead),
				CacheWrite: cloneFloat(m.CacheWrite),
				Reasoning:  cloneFloat(m.Reasoning),
			},
			override: m.Override,
		}
		if strings.HasSuffix(name, "*") {
			entry.prefix = true
			name = strings.TrimSuffix(name, "*")
		}
		entry.model = name
		entries = append(entries, entry)
	}
	return entries
}

func cloneFloat(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// catalogProviders maps a proxx provider key to models.dev provider IDs.
func catalogProviders(provider string) []string {
	p := strings.ToLower(strings.TrimSpace(provider))
	p = strings.TrimPrefix(p, "openai-compatible-")
	switch {
	case p == "":
		return nil
	case p == "claude" || p == "anthropic":
		return []string{"anthropic"}
	case p == "gemini" || p == "vertex" || p == "aistudio" || p == "gemini-cli" || p == "antigravity" || p == "google":
		return []string{"google"}
	case p == "codex" || strings.HasPrefix(p, "codex-") || p == "openai":
		return []string{"openai"}
	case p == "kimi" || strings.HasPrefix(p, "kimi"):
		return []string{"moonshotai"}
	case p == "xai" || p == "grok":
		return []string{"xai"}
	default:
		return []string{p}
	}
}

func providerMatches(entryProvider, provider string) bool {
	if entryProvider == "" {
		return true
	}
	p := strings.ToLower(strings.TrimSpace(provider))
	if entryProvider == p {
		return true
	}
	for _, id := range catalogProviders(p) {
		if entryProvider == id {
			return true
		}
	}
	return false
}

// basicModelID lowercases, strips a "models/" prefix and a trailing thinking "(...)" suffix.
func basicModelID(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "models/")
	if i := strings.LastIndex(m, "("); i > 0 && strings.HasSuffix(m, ")") {
		m = m[:i]
	}
	return strings.TrimSpace(m)
}

var (
	dateSuffixRe    = regexp.MustCompile(`-(\d{8}|\d{4}-\d{2}-\d{2})$`)
	variantSuffixes = []string{"-thinking", "-xhigh", "-high", "-medium", "-low", "-minimal", "-preview", "-latest"}
)

// normalizeModelID strips ":tag" suffixes, date suffixes and thinking/effort/preview variants.
func normalizeModelID(model string) string {
	m := basicModelID(model)
	if i := strings.LastIndex(m, ":"); i > 0 {
		m = m[:i]
	}
	for {
		before := m
		m = dateSuffixRe.ReplaceAllString(m, "")
		for _, suffix := range variantSuffixes {
			if strings.HasSuffix(m, suffix) && len(m) > len(suffix) {
				m = strings.TrimSuffix(m, suffix)
			}
		}
		if m == before {
			return m
		}
	}
}

func isFreeModel(model string) bool {
	return strings.HasSuffix(basicModelID(model), ":free")
}

// lookupConfig finds the best config entry for any of names (in order).
func lookupConfig(entries []configEntry, provider string, names ...string) (configEntry, bool) {
	if len(entries) == 0 {
		return configEntry{}, false
	}
	keys := make([]string, 0, len(names))
	for _, n := range names {
		if k := basicModelID(n); k != "" {
			keys = append(keys, k)
		}
	}
	for _, key := range keys {
		for _, restricted := range []bool{true, false} {
			for _, e := range entries {
				if e.prefix || (e.provider != "") != restricted || e.model != key || !providerMatches(e.provider, provider) {
					continue
				}
				return e, true
			}
		}
	}
	for _, key := range keys {
		for _, restricted := range []bool{true, false} {
			best, bestLen := configEntry{}, -1
			for _, e := range entries {
				if !e.prefix || (e.provider != "") != restricted || !strings.HasPrefix(key, e.model) || !providerMatches(e.provider, provider) {
					continue
				}
				if len(e.model) > bestLen {
					best, bestLen = e, len(e.model)
				}
			}
			if bestLen >= 0 {
				return best, true
			}
		}
	}
	return configEntry{}, false
}

// lookupCatalog resolves model under provider in the catalog.
func lookupCatalog(c *Catalog, provider, model string) (Price, bool) {
	if c == nil || c.models == 0 {
		return Price{}, false
	}
	mapped := catalogProviders(provider)
	raw := basicModelID(model)
	if raw == "" {
		return Price{}, false
	}
	candidates := []string{raw}
	if i := strings.Index(raw, "/"); i > 0 && i < len(raw)-1 {
		candidates = append(candidates, raw[i+1:])
	}
	norm := normalizeModelID(raw)
	if norm != raw {
		candidates = append(candidates, norm)
	}
	if i := strings.Index(norm, "/"); i > 0 && i < len(norm)-1 {
		candidates = append(candidates, norm[i+1:])
	}
	order := c.providerOrder()

	try := func(get func(string, string) (Price, bool), key string) (Price, bool) {
		for _, p := range mapped {
			if price, ok := get(p, key); ok {
				return price, true
			}
		}
		// "vendor/model" IDs (OpenRouter style) map to the vendor's own catalog entry.
		if i := strings.Index(key, "/"); i > 0 {
			for _, p := range catalogProviders(key[:i]) {
				if price, ok := get(p, key[i+1:]); ok {
					return price, true
				}
			}
		}
		for _, p := range order {
			if price, ok := get(p, key); ok {
				return price, true
			}
		}
		return Price{}, false
	}
	for _, key := range candidates {
		if price, ok := try(c.get, key); ok {
			return price, true
		}
		if key == raw && isFreeModel(model) {
			zero := 0.0
			return Price{Input: &zero, Output: &zero, CacheRead: &zero, CacheWrite: &zero}, true
		}
	}
	for _, key := range candidates {
		if price, ok := try(c.getNormalized, normalizeModelID(key)); ok {
			return price, true
		}
	}
	return Price{}, false
}

func (c *Catalog) providerOrder() []string { return c.order }

func buildProviderOrder(c *Catalog) []string {
	seen := map[string]bool{}
	order := make([]string, 0, len(c.providers))
	for _, p := range firstPartyProviders {
		if _, ok := c.providers[p]; ok {
			order = append(order, p)
			seen[p] = true
		}
	}
	rest := make([]string, 0, len(c.providers))
	for p := range c.providers {
		if !seen[p] {
			rest = append(rest, p)
		}
	}
	sort.Strings(rest)
	return append(order, rest...)
}

// lookup resolves the price for model served by provider: config entries, then catalog.
func lookup(entries []configEntry, c *Catalog, provider, model string) (Match, bool) {
	if e, ok := lookupConfig(entries, provider, model); ok {
		base, _ := lookupCatalog(c, provider, model)
		return Match{Price: e.price.mergeOver(base), Source: SourceConfig, Override: e.override}, true
	}
	if price, ok := lookupCatalog(c, provider, model); ok {
		return Match{Price: price, Source: SourceCatalog}, true
	}
	return Match{}, false
}

// lookupAliasExact matches the client alias against exact (non-prefix) config entries only.
// It is the last resort, so an alias pool never prices the served model at the alias price.
func lookupAliasExact(entries []configEntry, provider, alias string) (Match, bool) {
	key := basicModelID(alias)
	if key == "" {
		return Match{}, false
	}
	for _, restricted := range []bool{true, false} {
		for _, e := range entries {
			if e.prefix || (e.provider != "") != restricted || e.model != key || !providerMatches(e.provider, provider) {
				continue
			}
			return Match{Price: e.price, Source: SourceConfig, Override: e.override}, true
		}
	}
	return Match{}, false
}

// NormalizeModelID strips ":tag", date and thinking/effort/preview suffixes from a model ID.
func NormalizeModelID(model string) string { return normalizeModelID(model) }
