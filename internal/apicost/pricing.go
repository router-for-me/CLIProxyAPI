// Package apicost estimates what proxied traffic would have cost at public
// API list prices. Prices come from the LiteLLM model price catalog, the same
// source ccusage uses, and costs follow ccusage's per-request formula.
package apicost

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpfetch"
	log "github.com/sirupsen/logrus"
)

// LiteLLMPricingURL is the upstream price catalog used by ccusage.
const LiteLLMPricingURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

const (
	pricingRefreshInterval = 24 * time.Hour
	maxPricingBytes        = 64 << 20
	providerOpenAI         = "openai"
)

// embeddedPricing is a filtered LiteLLM snapshot used until (or instead of)
// a remote refresh. Regenerate it with `go run ./cmd/fetch_api_cost_pricing`.
//
//go:embed litellm_pricing.json
var embeddedPricing []byte

// fastMultiplierOverrides and flexMultiplierOverrides are copied from ccusage
// (MIT, ryoppippi/ccusage, rust/crates/ccusage-core/src). They apply when the
// LiteLLM entry has no provider_specific_entry multiplier.
//
//go:embed fast-multiplier-overrides.json
var fastMultiplierOverrides []byte

//go:embed flex-multiplier-overrides.json
var flexMultiplierOverrides []byte

// Price holds per-token USD rates for one model.
type Price struct {
	Provider string
	Input    float64
	Output   float64
	// CacheWrite is the 5-minute cache write rate (LiteLLM default input*1.25).
	CacheWrite float64
	// CacheWrite12h is OpenAI's extended cache write rate; 0 means CacheWrite.
	CacheWrite12h float64
	// CacheRead defaults to input*0.1 for Anthropic-style pricing and to the
	// input rate for OpenAI when LiteLLM has no explicit value.
	CacheRead float64
	// Above200k holds Anthropic/Gemini-style marginal rates for tokens beyond
	// 200k of one bucket (LiteLLM *_above_200k_tokens).
	Above200k *TierRates
	// Above272k holds OpenAI long-context rates that apply to the whole
	// request once its input exceeds 272k tokens (LiteLLM *_above_272k_tokens).
	Above272k *TierRates
	// Fast and Flex are service tier multipliers (1 when unknown).
	Fast float64
	Flex float64
}

// TierRates are the long-context replacements for the base rates. Zero
// fields fall back to the base rate.
type TierRates struct {
	Input      float64
	Output     float64
	CacheWrite float64
	CacheRead  float64
}

// Catalog is an immutable model price table.
type Catalog struct {
	prices map[string]Price
}

// PricingStatus describes the active price catalog.
type PricingStatus struct {
	Source    string    `json:"source"`
	URL       string    `json:"url,omitempty"`
	LoadedAt  time.Time `json:"loaded_at"`
	Models    int       `json:"models"`
	LastError string    `json:"last_error,omitempty"`
}

type catalogState struct {
	catalog *Catalog
	status  PricingStatus
}

var (
	activeCatalog atomic.Pointer[catalogState]
	lastErrorMu   sync.Mutex
	lastError     string
	updaterOnce   sync.Once
)

func init() {
	catalog, errParse := ParseLiteLLM(embeddedPricing)
	if errParse != nil {
		log.Warnf("api cost: failed to parse embedded pricing: %v", errParse)
		catalog = &Catalog{prices: map[string]Price{}}
	}
	activeCatalog.Store(&catalogState{catalog: catalog, status: PricingStatus{
		Source:   "embedded",
		LoadedAt: time.Now().UTC(),
		Models:   len(catalog.prices),
	}})
}

// CurrentCatalog returns the active price catalog.
func CurrentCatalog() *Catalog { return activeCatalog.Load().catalog }

// CurrentPricingStatus reports where the active prices came from.
func CurrentPricingStatus() PricingStatus {
	status := activeCatalog.Load().status
	lastErrorMu.Lock()
	status.LastError = lastError
	lastErrorMu.Unlock()
	return status
}

// StartPricingUpdater refreshes prices from LiteLLM now and once a day. When
// remote is false the embedded snapshot stays active.
func StartPricingUpdater(ctx context.Context, remote bool) {
	if !remote {
		return
	}
	updaterOnce.Do(func() {
		go func() {
			refreshPricing(ctx)
			ticker := time.NewTicker(pricingRefreshInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					refreshPricing(ctx)
				}
			}
		}()
	})
}

func refreshPricing(ctx context.Context) {
	data, errFetch := httpfetch.GetBytes(ctx, http.DefaultClient, LiteLLMPricingURL, nil, maxPricingBytes)
	if errFetch == nil {
		var catalog *Catalog
		catalog, errFetch = ParseLiteLLM(data)
		if errFetch == nil {
			activeCatalog.Store(&catalogState{catalog: catalog, status: PricingStatus{
				Source:   "remote",
				URL:      LiteLLMPricingURL,
				LoadedAt: time.Now().UTC(),
				Models:   len(catalog.prices),
			}})
		}
	}
	lastErrorMu.Lock()
	defer lastErrorMu.Unlock()
	if errFetch != nil {
		if errors.Is(errFetch, context.Canceled) {
			return
		}
		lastError = errFetch.Error()
		log.Warnf("api cost: pricing refresh failed, keeping previous prices: %v", errFetch)
		return
	}
	lastError = ""
}

// ParseLiteLLM parses a LiteLLM model_prices_and_context_window.json body.
// Entries without input and output token prices are skipped.
func ParseLiteLLM(data []byte) (*Catalog, error) {
	root := ojson.ParseBytes(data)
	if !root.IsObject() {
		return nil, fmt.Errorf("pricing catalog is not a JSON object")
	}
	fast := parseMultiplierOverrides(fastMultiplierOverrides)
	flex := parseMultiplierOverrides(flexMultiplierOverrides)
	prices := make(map[string]Price)
	root.ForEach(func(key, value ojson.Result) bool {
		name := strings.ToLower(strings.TrimSpace(key.String()))
		if name == "" || name == "sample_spec" || !value.IsObject() {
			return true
		}
		input := value.Get("input_cost_per_token")
		output := value.Get("output_cost_per_token")
		if !input.Exists() || !output.Exists() {
			return true
		}
		price := Price{
			Provider: strings.ToLower(value.Get("litellm_provider").String()),
			Input:    input.Float(),
			Output:   output.Float(),
			Fast:     1,
			Flex:     1,
		}
		price.CacheWrite = floatOr(value.Get("cache_creation_input_token_cost"), price.Input*1.25)
		price.CacheWrite12h = value.Get("cache_creation_input_token_cost_above_12hr").Float()
		if cacheRead := value.Get("cache_read_input_token_cost"); cacheRead.Exists() {
			price.CacheRead = cacheRead.Float()
		} else if price.Provider == providerOpenAI {
			price.CacheRead = price.Input
		} else {
			price.CacheRead = price.Input * 0.1
		}
		price.Above200k = parseTier(value, "_above_200k_tokens")
		price.Above272k = parseTier(value, "_above_272k_tokens")
		if multiplier := value.Get("provider_specific_entry.fast"); multiplier.Exists() {
			price.Fast = multiplier.Float()
		} else if multiplier, ok := fast.lookup(name); ok {
			price.Fast = multiplier
		}
		if multiplier := value.Get("provider_specific_entry.flex"); multiplier.Exists() {
			price.Flex = multiplier.Float()
		} else if multiplier, ok := flex.lookup(name); ok {
			price.Flex = multiplier
		}
		prices[name] = price
		return true
	})
	if len(prices) == 0 {
		return nil, fmt.Errorf("pricing catalog contains no priced models")
	}
	return &Catalog{prices: prices}, nil
}

func parseTier(value ojson.Result, suffix string) *TierRates {
	tier := TierRates{
		Input:      value.Get("input_cost_per_token" + suffix).Float(),
		Output:     value.Get("output_cost_per_token" + suffix).Float(),
		CacheWrite: value.Get("cache_creation_input_token_cost" + suffix).Float(),
		CacheRead:  value.Get("cache_read_input_token_cost" + suffix).Float(),
	}
	if tier == (TierRates{}) {
		return nil
	}
	return &tier
}

func floatOr(value ojson.Result, fallback float64) float64 {
	if value.Exists() {
		return value.Float()
	}
	return fallback
}

type multiplierOverrides struct {
	exact  map[string]float64
	prefix map[string]float64
}

func parseMultiplierOverrides(data []byte) multiplierOverrides {
	out := multiplierOverrides{exact: map[string]float64{}, prefix: map[string]float64{}}
	root := ojson.ParseBytes(data)
	root.Get("exact").ForEach(func(key, value ojson.Result) bool {
		out.exact[strings.ToLower(key.String())] = value.Float()
		return true
	})
	root.Get("normalized_prefix").ForEach(func(key, value ojson.Result) bool {
		out.prefix[strings.ToLower(key.String())] = value.Float()
		return true
	})
	return out
}

func (m multiplierOverrides) lookup(model string) (float64, bool) {
	base := model
	if index := strings.LastIndexAny(base, "/:"); index >= 0 {
		base = base[index+1:]
	}
	base = dateSuffix.ReplaceAllString(base, "")
	if multiplier, ok := m.exact[base]; ok {
		return multiplier, true
	}
	for prefix, multiplier := range m.prefix {
		if strings.HasPrefix(base, prefix) {
			return multiplier, true
		}
	}
	return 0, false
}

var (
	dateSuffix      = regexp.MustCompile(`[-@]\d{8}$`)
	effortSuffix    = regexp.MustCompile(`\([^)]*\)$`)
	lookupPrefixes  = []string{"", "anthropic/", "openai/", "gemini/", "xai/", "deepseek/", "moonshot/"}
	variantSuffixes = []string{"-thinking"}
)

// Lookup resolves a proxy model name to a price. It tries the exact name,
// then common provider prefixes, then the name without a date or thinking
// suffix. The returned key is the catalog entry that matched.
func (c *Catalog) Lookup(model string) (Price, string, bool) {
	if c == nil {
		return Price{}, "", false
	}
	for _, candidate := range modelCandidates(model) {
		for _, prefix := range lookupPrefixes {
			if price, ok := c.prices[prefix+candidate]; ok {
				return price, candidate, true
			}
		}
	}
	return Price{}, "", false
}

func modelCandidates(model string) []string {
	name := strings.ToLower(strings.TrimSpace(model))
	name = strings.TrimSpace(effortSuffix.ReplaceAllString(name, ""))
	name = strings.TrimPrefix(name, "models/")
	if name == "" {
		return nil
	}
	candidates := []string{name}
	add := func(value string) {
		for _, existing := range candidates {
			if existing == value {
				return
			}
		}
		candidates = append(candidates, value)
	}
	undated := dateSuffix.ReplaceAllString(name, "")
	add(undated)
	for _, suffix := range variantSuffixes {
		add(strings.TrimSuffix(name, suffix))
		add(strings.TrimSuffix(undated, suffix))
	}
	return candidates
}
