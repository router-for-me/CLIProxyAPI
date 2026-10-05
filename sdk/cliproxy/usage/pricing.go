package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

// Rates are decimal currency units per token. Amounts round once, half up, to nine decimal places.
type CostEstimate struct {
	SchemaVersion int               `json:"schema_version"`
	Kind          string            `json:"kind"`
	Status        string            `json:"status"`
	Currency      string            `json:"currency,omitempty"`
	Amount        *string           `json:"amount,omitempty"`
	Rounding      string            `json:"rounding"`
	Snapshot      string            `json:"snapshot,omitempty"`
	Source        string            `json:"source,omitempty"`
	Model         string            `json:"model,omitempty"`
	Provider      string            `json:"provider,omitempty"`
	Tier          string            `json:"tier,omitempty"`
	Rates         *config.PriceRate `json:"rates,omitempty"`
	Usage         *TokenBreakdown   `json:"usage,omitempty"`
	Missing       []string          `json:"missing,omitempty"`
}

type PriceBook struct {
	rates    []config.PriceRate
	aliases  []config.PriceAlias
	snapshot string
	sources  []string
}

func decimalRate(value string) (*big.Rat, bool) {
	if value == "" || strings.Trim(value, "0123456789.") != "" || strings.Count(value, ".") > 1 || len(value) > 64 {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(value)
	return r, ok && r.Sign() >= 0
}

func NewPriceBook(rates []config.PriceRate, aliases []config.PriceAlias, sources []string) (*PriceBook, error) {
	if len(rates) != len(sources) {
		return nil, fmt.Errorf("price sources do not match rates")
	}
	for _, rate := range rates {
		if rate.Provider == "" || rate.Model == "" || len(rate.Currency) != 3 || rate.MinContext < 0 || rate.MaxContext < 0 || (rate.MaxContext > 0 && rate.MaxContext < rate.MinContext) {
			return nil, fmt.Errorf("invalid price identity or context band")
		}
		for _, value := range []*string{rate.Input, rate.Output, rate.CacheRead, rate.CacheCreation, rate.Reasoning} {
			if value != nil {
				if _, ok := decimalRate(*value); !ok {
					return nil, fmt.Errorf("invalid decimal price")
				}
			}
		}
	}
	raw, err := json.Marshal(struct {
		Rates   []config.PriceRate
		Aliases []config.PriceAlias
		Sources []string
	}{rates, aliases, sources})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	// Detach the snapshot from caller-owned slices and rate pointers.
	var copy struct {
		Rates   []config.PriceRate
		Aliases []config.PriceAlias
		Sources []string
	}
	if err := json.Unmarshal(raw, &copy); err != nil {
		return nil, err
	}
	return &PriceBook{copy.Rates, copy.Aliases, hex.EncodeToString(sum[:]), copy.Sources}, nil
}

func (b *PriceBook) Estimate(event AccountingEvent) *CostEstimate {
	e := &CostEstimate{SchemaVersion: 1, Kind: "api_equivalent_estimate", Status: "unpriced", Rounding: "half_up_9_decimal_places"}
	if event.Tokens == nil {
		e.Missing = []string{"usage"}
		return e
	}
	breakdown := event.Tokens.Breakdown
	e.Usage = &breakdown
	if !breakdown.Valid() {
		e.Missing = []string{"valid_usage"}
		return e
	}
	if b == nil {
		e.Missing = []string{"prices"}
		return e
	}
	e.Snapshot = b.snapshot
	provider, model := event.Provider, event.ExecutedModel
	if event.ResponseModel != "" {
		model = event.ResponseModel
	}
	for _, alias := range b.aliases {
		if alias.Provider == provider && alias.Model == model {
			provider, model = alias.TargetProvider, alias.TargetModel
			break
		}
	}
	tier := event.ReportedServiceTier
	if tier == "" || tier == "default" {
		tier = ""
	}
	e.Provider, e.Model, e.Tier = provider, model, tier
	index := -1
	for i, rate := range b.rates {
		if rate.Provider == provider && rate.Model == model && rate.Tier == tier && breakdown.Input.TotalTokens >= rate.MinContext && (rate.MaxContext == 0 || breakdown.Input.TotalTokens <= rate.MaxContext) {
			index = i
		}
	}
	if index < 0 {
		e.Missing = []string{"model_tier_context_price"}
		return e
	}
	rate := clonePriceRate(b.rates[index])
	e.Rates, e.Currency, e.Source = &rate, rate.Currency, b.sources[index]
	reasoning := rate.Reasoning
	if reasoning == nil {
		reasoning = rate.Output
	}
	buckets := []struct {
		name  string
		count int64
		rate  *string
	}{
		{"input", breakdown.Input.UncachedTokens, rate.Input},
		{"cache_read", breakdown.Input.CacheReadTokens, rate.CacheRead},
		{"cache_creation", breakdown.Input.CacheWriteTokens, rate.CacheCreation},
		{"output", breakdown.Output.NonReasoningTokens, rate.Output},
		{"reasoning", breakdown.Output.ReasoningTokens, reasoning},
	}
	total := new(big.Rat)
	known := 0
	for _, bucket := range buckets {
		if bucket.count == 0 {
			continue
		}
		if bucket.rate == nil {
			e.Missing = append(e.Missing, bucket.name)
			continue
		}
		r, _ := decimalRate(*bucket.rate)
		total.Add(total, r.Mul(r, new(big.Rat).SetInt64(bucket.count)))
		known++
	}
	if breakdown.UnclassifiedTokens > 0 || breakdown.Quality != TokenAccountingQualityComplete {
		e.Missing = append(e.Missing, "unclassified_usage")
	}
	if len(e.Missing) == 0 {
		e.Status = "priced"
	} else if known > 0 {
		e.Status = "partial"
	} else {
		return e
	}
	amount := total.FloatString(9)
	e.Amount = &amount
	return e
}

func importLiteLLM(raw []byte) ([]config.PriceRate, error) {
	var entries map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	rates := []config.PriceRate{}
	// Sort by catalog key so a snapshot has a stable identity.
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entry := entries[key]
		var provider, mode string
		_ = json.Unmarshal(entry["litellm_provider"], &provider)
		_ = json.Unmarshal(entry["mode"], &mode)
		if provider == "" || (mode != "chat" && mode != "completion" && mode != "responses") {
			continue
		}
		rate := config.PriceRate{Provider: provider, Model: strings.TrimPrefix(key, provider+"/"), Currency: "USD"}
		for field, target := range map[string]**string{"input_cost_per_token": &rate.Input, "output_cost_per_token": &rate.Output, "cache_read_input_token_cost": &rate.CacheRead, "cache_creation_input_token_cost": &rate.CacheCreation} {
			if value, ok := entry[field]; ok && string(value) != "null" {
				var number json.Number
				if err := json.Unmarshal(value, &number); err != nil {
					return nil, fmt.Errorf("invalid catalog rate for %s", key)
				}
				r, ok := new(big.Rat).SetString(string(number))
				if !ok || r.Sign() < 0 {
					return nil, fmt.Errorf("invalid catalog rate for %s", key)
				}
				// LiteLLM commonly uses scientific notation. Preserve exact finite decimals.
				decimal := r.FloatString(18)
				parsed, _ := new(big.Rat).SetString(decimal)
				if parsed.Cmp(r) != 0 {
					return nil, fmt.Errorf("catalog precision exceeds 18 places")
				}
				*target = &decimal
			}
		}
		// Unsupported conditional prices cannot safely be treated as the base price.
		for field := range entry {
			if strings.Contains(field, "cost") && (strings.Contains(field, "above_") || strings.Contains(field, "priority") || strings.Contains(field, "flex")) {
				rate.Input, rate.Output, rate.CacheRead, rate.CacheCreation = nil, nil, nil, nil
			}
		}
		rates = append(rates, rate)
	}
	if len(rates) == 0 {
		return nil, fmt.Errorf("catalog contains no supported prices")
	}
	return rates, nil
}

func loadPriceBook(cfg config.PricingConfig, dataPath string) *PriceBook {
	var rates []config.PriceRate
	var sources []string
	cache := filepath.Join(dataPath, "pricing-litellm.json")
	if cfg.LiteLLMCatalogPath != "" {
		raw, err := os.ReadFile(cfg.LiteLLMCatalogPath)
		if err == nil {
			rates, err = importLiteLLM(raw)
		}
		if err == nil {
			_, err = NewPriceBook(rates, nil, makeSources(len(rates), "litellm"))
		}
		if err == nil {
			if errWrite := os.WriteFile(cache+".tmp", raw, 0600); errWrite == nil {
				errWrite = os.Rename(cache+".tmp", cache)
				if errWrite != nil {
					log.WithError(errWrite).Warn("price cache rename failed")
				}
			} else {
				log.WithError(errWrite).Warn("price cache write failed")
			}
		} else {
			log.WithError(err).Warn("price import unavailable, using validated local cache")
			rates = nil
			if cached, errRead := os.ReadFile(cache); errRead == nil {
				rates, _ = importLiteLLM(cached)
			}
		}
		sources = makeSources(len(rates), "litellm")
	}
	rates = append(rates, cfg.Overrides...)
	sources = append(sources, makeSources(len(cfg.Overrides), "local_override")...)
	book, err := NewPriceBook(rates, cfg.Aliases, sources)
	if err != nil {
		log.WithError(err).Warn("pricing unavailable, usage remains unpriced")
		return nil
	}
	return book
}

func makeSources(count int, source string) []string {
	result := make([]string, count)
	for i := range result {
		result[i] = source
	}
	return result
}

func clonePriceRate(rate config.PriceRate) config.PriceRate {
	for _, field := range []**string{&rate.Input, &rate.Output, &rate.CacheRead, &rate.CacheCreation, &rate.Reasoning} {
		if *field != nil {
			value := **field
			*field = &value
		}
	}
	return rate
}
