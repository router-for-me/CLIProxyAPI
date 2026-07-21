// Package pricingsource bundles publicly-published per-model USD rates
// (USD per 1,000,000 tokens) from upstream providers and marketplaces
// (OpenRouter, Cloudflare AI Gateway, official Anthropic/OpenAI docs). The
// data is shipped in-repo as JSON so the dashboard can show a "review and
// accept" pricing preview without external network calls during normal
// operation.
//
// The package is intentionally dependency-free and read-only. Management
// handlers under internal/api/handlers/management consume MatchAll(items)
// to compute suggested pricing for every row in models_catalog.
package pricingsource

import (
	"embed"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

//go:embed data/*.json
var dataFS embed.FS

// SuggestedPrice is a single provider's published price for a model. A model
// can have multiple entries (one per source); "Source" identifies the
// origin (e.g. "openrouter", "cloudflare", "anthropic-official") and "Notes"
// carries any caveats (e.g. cached input treated as cache_read).
type SuggestedPrice struct {
	Model            string  `json:"model"`
	Source           string  `json:"source"`
	InputPer1M       float64 `json:"input_per_1m_usd"`
	OutputPer1M      float64 `json:"output_per_1m_usd"`
	CachedInputPer1M float64 `json:"cached_input_per_1m_usd"`
	CachedReadPer1M  float64 `json:"cached_read_per_1m_usd"`
	ReasoningPer1M   float64 `json:"reasoning_per_1m_usd"`
	Notes            string  `json:"notes,omitempty"`
}

// index is the in-memory lookup table built lazily from the bundled JSON
// files. It is keyed by lowercase model ID (so match is case-insensitive
// across sources that disagree on casing).
type index struct {
	mu      sync.RWMutex
	entries map[string][]SuggestedPrice
}

var pakIndex = &index{entries: nil}

// loadLocked reads all JSON files under data/ and indexes them by model ID.
// Idempotent; safe to call more than once.
func (i *index) loadLocked() error {
	if i.entries != nil {
		return nil
	}
	i.entries = map[string][]SuggestedPrice{}
	entries, err := dataFS.ReadDir("data")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, err := dataFS.ReadFile("data/" + name)
		if err != nil {
			return err
		}
		var batch []SuggestedPrice
		if err := json.Unmarshal(raw, &batch); err != nil {
			return err
		}
		for _, p := range batch {
			key := strings.ToLower(strings.TrimSpace(p.Model))
			if key == "" {
				continue
			}
			i.entries[key] = append(i.entries[key], p)
		}
	}
	return nil
}

// MatchAll returns the suggested-price entries for every supplied model ID.
// Models without a known price are omitted (callers can compute the
// difference to surface "unknown" entries). The lookup is case-insensitive.
//
// When multiple sources publish prices for the same model, *all* entries are
// returned (deduped by Source) so the dashboard can show a side-by-side
// comparison and let the operator pick.
func MatchAll(modelIDs []string) map[string][]SuggestedPrice {
	pakIndex.mu.Lock()
	if err := pakIndex.loadLocked(); err != nil {
		// Fall through to a query against an empty index rather than panic;
		// the operator will see an empty suggestions set.
		_ = err
	}
	pakIndex.mu.Unlock()

	pakIndex.mu.RLock()
	defer pakIndex.mu.RUnlock()

	out := make(map[string][]SuggestedPrice, len(modelIDs))
	for _, id := range modelIDs {
		key := strings.ToLower(strings.TrimSpace(id))
		if key == "" {
			continue
		}
		// First, try an exact match. If that misses, try a
		// date-stamp-suffix strip: providers often ship snapshots like
		// "claude-3-5-sonnet-20241022" while pricing catalogs are
		// published against "claude-3-5-sonnet".
		if entries, ok := pakIndex.entries[key]; ok {
			out[id] = append([]SuggestedPrice(nil), entries...)
			continue
		}
		normalized := stripDateSuffix(key)
		if normalized != key {
			if entries, ok := pakIndex.entries[normalized]; ok {
				out[id] = append([]SuggestedPrice(nil), entries...)
				continue
			}
		}
		// Finally, try a prefix match against "family/*" — useful for
		// "gpt-4o-mini-2024-07-18" matching "gpt-4o-mini".
		for catalogKey, entries := range pakIndex.entries {
			if strings.HasPrefix(key, catalogKey) && len(catalogKey) >= 6 {
				out[id] = append(out[id], entries...)
			}
		}
		// Last resort: token matching. Split both the query and every
		// catalog key into word tokens (alphanumeric, length ≥ 3, not
		// purely numeric) and pair them when they share at least one
		// non-numeric token. This lets vendor-namespaced IDs like
		// "semut-glm-5.2" borrow pricing published for "glm-5.2" via
		// the shared "glm" token. Applied only when all preceding
		// strategies missed, so exact matches are never shadowed.
		if _, ok := out[id]; !ok {
			queryTokens := wordTokens(key)
			if len(queryTokens) > 0 {
				for catalogKey, entries := range pakIndex.entries {
					if tokensShareWord(queryTokens, catalogKey) {
						out[id] = append(out[id], entries...)
					}
				}
			}
		}
		// Merge in suggestions from operator-managed external catalogs
		// (LiteLLM URLs / uploaded files). These are additive — they fill
		// gaps the bundled data does not cover — so they are appended even
		// when the bundled catalog already matched. Duplicates by Source
		// name are dropped so a multi-source preview does not list the same
		// external source twice for one model.
		extEntries := externalEntriesFor(key)
		if len(extEntries) > 0 {
			seenSources := map[string]struct{}{}
			for _, e := range out[id] {
				seenSources[e.Source] = struct{}{}
			}
			for _, e := range extEntries {
				if _, dup := seenSources[e.Source]; dup {
					continue
				}
				seenSources[e.Source] = struct{}{}
				out[id] = append(out[id], e)
			}
		}
	}
	return out
}

// stripDateSuffix removes a trailing -YYYYMMDD stamp from a model id, e.g.
// "claude-3-5-sonnet-20241022" -> "claude-3-5-sonnet". Returns the input
// unchanged when no suffix is present.
func stripDateSuffix(id string) string {
	if len(id) < 10 {
		return id
	}
	tail := id[len(id)-9:]
	// Expect: "-YYYYMMDD"
	if tail[0] != '-' {
		return id
	}
	for _, r := range tail[1:] {
		if r < '0' || r > '9' {
			return id
		}
	}
	return id[:len(id)-9]
}

// wordTokens splits an id into the word-shaped tokens used by the fuzzy
// pricing match. A token is a maximal run of letters/digits at least 3
// characters long that is not purely numeric — pure-numeric tokens (e.g.
// "5", "20241022") carry no identifying signal and are skipped to avoid
// spurious pairings across unrelated models that happen to share a version
// digit. Tokens are returned in input order and are already lower-cased
// (callers pass a lower-cased id).
func wordTokens(id string) []string {
	var out []string
	start := -1
	flush := func(s, e int) {
		if s < 0 {
			return
		}
		tok := id[s:e]
		if len(tok) >= 3 && !isAllDigits(tok) {
			out = append(out, tok)
		}
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		isWord := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if isWord {
			if start < 0 {
				start = i
			}
		} else {
			flush(start, i)
			start = -1
		}
	}
	flush(start, len(id))
	return out
}

// isAllDigits reports whether s is non-empty and consists solely of ASCII
// digits. Used to skip numeric-only tokens during fuzzy token matching.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// tokensShareWord reports whether the query token set and the candidate id
// share at least one non-numeric word token. The candidate is split on the
// fly so callers do not need to pre-tokenize the entire catalog.
func tokensShareWord(queryTokens []string, candidate string) bool {
	if len(queryTokens) == 0 {
		return false
	}
	candTokens := wordTokens(candidate)
	if len(candTokens) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(candTokens))
	for _, t := range candTokens {
		seen[t] = struct{}{}
	}
	for _, t := range queryTokens {
		if _, ok := seen[t]; ok {
			return true
		}
	}
	return false
}

// Sources returns a sorted list of source identifiers present in the
// bundled catalog (e.g. "anthropic-official", "cloudflare", "openrouter").
// Useful for the dashboard to render a filter dropdown.
func Sources() []string {
	pakIndex.mu.Lock()
	if err := pakIndex.loadLocked(); err != nil {
		_ = err
	}
	pakIndex.mu.Unlock()

	pakIndex.mu.RLock()
	defer pakIndex.mu.RUnlock()

	seen := map[string]struct{}{}
	for _, entries := range pakIndex.entries {
		for _, e := range entries {
			if e.Source != "" {
				seen[e.Source] = struct{}{}
			}
		}
	}
	// Include operator-managed external sources so the dashboard filter
	// dropdown offers them as a filter option.
	for _, snap := range ListExternalSources() {
		if snap.Source.Enabled && snap.Source.Name != "" {
			seen[snap.Source.Name] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for src := range seen {
		out = append(out, src)
	}
	sort.Strings(out)
	return out
}
