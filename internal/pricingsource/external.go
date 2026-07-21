package pricingsource

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ExternalSource is an operator-managed pricing catalog loaded on refresh.
// The bundled JSON files under data/ always remain available; external
// sources are merged on top so operator-supplied catalogs (e.g. a LiteLLM
// model_prices_and_context_window.json hosted on a private fork) can fill
// gaps the bundled data does not cover.
type ExternalSource struct {
	Name       string // unique identifier; also used as SuggestedPrice.Source
	SourceType string // "url" | "file"
	URL        string // used when SourceType == "url"
	FilePath   string // used when SourceType == "file"
	Format     string // "litellm" | "native"; only "litellm" supported today
	Enabled    bool
}

// externalRegistry holds the set of operator-managed external sources and
// their last-loaded entries. Guarded by extMu. The bundled catalog (pakIndex)
// is always merged in MatchAll; external entries are additive.
type externalRegistry struct {
	extMu     sync.RWMutex
	sources   map[string]ExternalSource
	entries   map[string][]SuggestedPrice // keyed by lowercase model id
	loadState map[string]externalLoadState
}

type externalLoadState struct {
	entryCount int
	err        string
	fetchedAt  time.Time
}

var extRegistry = &externalRegistry{
	sources:   map[string]ExternalSource{},
	entries:   map[string][]SuggestedPrice{},
	loadState: map[string]externalLoadState{},
}

// RegistrySnapshot captures the current set of external sources plus their
// last refresh outcome. Returned by ListExternalSources for the dashboard.
type RegistrySnapshot struct {
	Source      ExternalSource
	EntryCount  int
	LastError   string
	LastFetched time.Time // zero when never fetched
}

// SetExternalSources replaces the registry's source list. Sources not
// present in the new slice have their cached entries dropped. Existing
// sources keep their cached entries (so a list refresh does not wipe
// loaded data); call RefreshAll to re-fetch.
//
// Sources arriving from the management API pass through here. Disabled
// sources are kept in the registry (so the operator can re-enable them
// without re-entering the URL) but their entries are cleared — they
// contribute nothing to MatchAll until refreshed while enabled.
func SetExternalSources(sources []ExternalSource) {
	extRegistry.extMu.Lock()
	defer extRegistry.extMu.Unlock()
	next := map[string]ExternalSource{}
	for _, s := range sources {
		if s.Name == "" {
			continue
		}
		next[s.Name] = s
	}
	// Drop entries for sources no longer registered or now disabled.
	for name := range extRegistry.entries {
		if s, ok := next[name]; !ok || !s.Enabled {
			delete(extRegistry.entries, name)
		}
	}
	// Drop sources no longer registered (keep load state? no — purge).
	for name := range extRegistry.sources {
		if _, ok := next[name]; !ok {
			delete(extRegistry.loadState, name)
		}
	}
	extRegistry.sources = next
}

// ListExternalSources returns a snapshot of every registered source plus
// its last refresh outcome, ordered by name. The dashboard surfaces this
// under /pricing-sources so operators can see "last fetched N ago · 2321
// entries · no error".
func ListExternalSources() []RegistrySnapshot {
	extRegistry.extMu.RLock()
	defer extRegistry.extMu.RUnlock()
	out := make([]RegistrySnapshot, 0, len(extRegistry.sources))
	for name, s := range extRegistry.sources {
		st := extRegistry.loadState[name]
		out = append(out, RegistrySnapshot{
			Source:      s,
			EntryCount:  st.entryCount,
			LastError:   st.err,
			LastFetched: st.fetchedAt,
		})
	}
	// Sort by name for stable dashboard rendering.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Source.Name > out[j].Source.Name; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// RefreshSource (re)loads one external source by name, parses its
// catalog, and replaces the cached entries under that name. Returns the
// number of entries loaded and any error. The load state is recorded
// regardless of outcome so the dashboard can surface last_error.
func RefreshSource(ctx context.Context, name string) (int, error) {
	extRegistry.extMu.RLock()
	src, ok := extRegistry.sources[name]
	extRegistry.extMu.RUnlock()
	if !ok {
		return 0, fmt.Errorf("pricingsource: unknown external source %q", name)
	}
	if !src.Enabled {
		return 0, fmt.Errorf("pricingsource: external source %q is disabled", name)
	}
	entries, err := loadExternalCatalog(ctx, src)
	count := len(entries)
	extRegistry.extMu.Lock()
	defer extRegistry.extMu.Unlock()
	// Always replace: on error we clear stale entries so MatchAll does not
	// surface prices from a source that has since moved or changed shape.
	if err != nil {
		delete(extRegistry.entries, name)
	} else {
		extRegistry.entries[name] = entries
	}
	loadState := externalLoadState{
		entryCount: count,
		fetchedAt:  time.Now(),
	}
	if err != nil {
		loadState.err = err.Error()
	}
	extRegistry.loadState[name] = loadState
	return count, err
}

// RefreshAll reloads every enabled external source. Errors are recorded per
// source but do not abort the loop. Returns the number of sources refreshed
// and the number that failed.
func RefreshAll(ctx context.Context) (refreshed, failed int) {
	extRegistry.extMu.RLock()
	names := make([]string, 0, len(extRegistry.sources))
	for name, s := range extRegistry.sources {
		if s.Enabled {
			names = append(names, name)
		}
	}
	extRegistry.extMu.RUnlock()
	for _, name := range names {
		_, err := RefreshSource(ctx, name)
		refreshed++
		if err != nil {
			failed++
		}
	}
	return refreshed, failed
}

// externalEntriesFor returns the cached suggestions contributed by external
// sources for a single model id. Called by MatchAll after the bundled
// catalog strategies have run.
func externalEntriesFor(lowerID string) []SuggestedPrice {
	extRegistry.extMu.RLock()
	defer extRegistry.extMu.RUnlock()
	var out []SuggestedPrice
	for _, entries := range extRegistry.entries {
		for _, e := range entries {
			if strings.ToLower(e.Model) == lowerID {
				out = append(out, e)
			}
		}
	}
	return out
}

// loadExternalCatalog fetches + parses one external source. Format "litellm"
// is the only supported value today; "native" is reserved for a future
// in-repo catalog format.
func loadExternalCatalog(ctx context.Context, src ExternalSource) ([]SuggestedPrice, error) {
	raw, err := readExternalBytes(ctx, src)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(src.Format)) {
	case "", "litellm":
		return parseLiteLLMCatalog(raw, src.Name)
	case "native":
		return parseNativeCatalog(raw, src.Name)
	default:
		return nil, fmt.Errorf("pricingsource: unsupported format %q for source %q", src.Format, src.Name)
	}
}

// readExternalBytes pulls the raw JSON bytes from a URL or local file.
func readExternalBytes(ctx context.Context, src ExternalSource) ([]byte, error) {
	switch src.SourceType {
	case "url":
		if src.URL == "" {
			return nil, fmt.Errorf("pricingsource: source %q has empty url", src.Name)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
		if err != nil {
			return nil, fmt.Errorf("pricingsource: build request for %q: %w", src.Name, err)
		}
		req.Header.Set("Accept", "application/json")
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("pricingsource: fetch %q: %w", src.Name, err)
		}
		defer func() {
			if cerr := resp.Body.Close(); cerr != nil {
				_ = cerr
			}
		}()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("pricingsource: fetch %q: HTTP %d", src.Name, resp.StatusCode)
		}
		// Cap the read at 64 MB to bound memory usage against a malformed/giant
		// catalog. The canonical LiteLLM file is ~1.5 MB.
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
		if err != nil {
			return nil, fmt.Errorf("pricingsource: read body %q: %w", src.Name, err)
		}
		return body, nil
	case "file":
		if src.FilePath == "" {
			return nil, fmt.Errorf("pricingsource: source %q has empty file_path", src.Name)
		}
		raw, err := os.ReadFile(src.FilePath)
		if err != nil {
			return nil, fmt.Errorf("pricingsource: read file %q: %w", src.Name, err)
		}
		return raw, nil
	default:
		return nil, fmt.Errorf("pricingsource: source %q has unsupported source_type %q", src.Name, src.SourceType)
	}
}

// litellmPricingEntry mirrors the per-model shape LiteLLM ships in
// model_prices_and_context_window.json. Only the pricing-relevant fields
// are captured; capability flags are ignored. Costs are per-token (USD),
// so we multiply by 1_000_000 to match the per-1M convention used by the
// bundled catalog.
type litellmPricingEntry struct {
	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
	OutputCostPerReasoningToken *float64 `json:"output_cost_per_reasoning_token"`
	LiteLLMProvider             string   `json:"litellm_provider"`
	Mode                        string   `json:"mode"`
}

// parseLiteLLMCatalog decodes the LiteLLM JSON catalog (a flat object keyed
// by model id) into SuggestedPrice entries. The "sample_spec" key is
// skipped. Per-token costs are converted to per-1M-token USD by
// multiplying by 1e6.
func parseLiteLLMCatalog(raw []byte, sourceName string) ([]SuggestedPrice, error) {
	var doc map[string]litellmPricingEntry
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("pricingsource: parse litellm catalog %q: %w", sourceName, err)
	}
	out := make([]SuggestedPrice, 0, len(doc))
	for id, e := range doc {
		id = strings.TrimSpace(id)
		if id == "" || id == "sample_spec" {
			continue
		}
		// Skip entries with no pricing signal — they would contribute $0/$0
		// suggestions that shadow the bundled catalog without adding value.
		if e.InputCostPerToken == nil && e.OutputCostPerToken == nil &&
			e.CacheReadInputTokenCost == nil && e.CacheCreationInputTokenCost == nil &&
			e.OutputCostPerReasoningToken == nil {
			continue
		}
		out = append(out, SuggestedPrice{
			Model:            id,
			Source:           sourceName,
			InputPer1M:       perM(e.InputCostPerToken),
			OutputPer1M:      perM(e.OutputCostPerToken),
			CachedReadPer1M:  perM(e.CacheReadInputTokenCost),
			CachedInputPer1M: perM(e.CacheCreationInputTokenCost),
			ReasoningPer1M:   perM(e.OutputCostPerReasoningToken),
		})
	}
	return out, nil
}

// parseNativeCatalog decodes the bundled native format (a JSON array of
// SuggestedPrice, same shape as the files under data/). Used when an
// operator uploads a hand-curated catalog in native shape.
func parseNativeCatalog(raw []byte, sourceName string) ([]SuggestedPrice, error) {
	var batch []SuggestedPrice
	if err := json.Unmarshal(raw, &batch); err != nil {
		return nil, fmt.Errorf("pricingsource: parse native catalog %q: %w", sourceName, err)
	}
	for i := range batch {
		if batch[i].Source == "" {
			batch[i].Source = sourceName
		}
	}
	return batch, nil
}

// perM converts a per-token USD cost (the LiteLLM convention) to per-1M-
// token USD (the bundled-catalog convention). Nil pointers yield 0.
func perM(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p * 1_000_000
}
