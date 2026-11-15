package usagestats

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// defaultPricesJSON holds public list prices, in USD per one million tokens.
// The file records its source and retrieval date.
//
//go:embed default_prices.json
var defaultPricesJSON []byte

const settingsKey = "settings"

// Price is a model's list price in USD per one million tokens. Optional
// buckets fall back to the nearest broader price when the model has no
// dedicated rate for them.
type Price struct {
	Input        float64  `json:"input"`
	Output       float64  `json:"output"`
	CacheRead    *float64 `json:"cache_read,omitempty"`
	CacheWrite5m *float64 `json:"cache_write_5m,omitempty"`
	CacheWrite1h *float64 `json:"cache_write_1h,omitempty"`
}

// AccountPlan is what an upstream subscription costs per month, used to
// compare the API-equivalent value an account delivered against its price.
type AccountPlan struct {
	Provider   string  `json:"provider"`
	Account    string  `json:"account"`
	MonthlyUSD float64 `json:"monthly_usd"`
}

// Settings are the operator-editable dashboard settings stored with the data.
type Settings struct {
	// ModelPrices override or extend the built-in price table, keyed by model name.
	ModelPrices  map[string]Price `json:"model_prices"`
	AccountPlans []AccountPlan    `json:"account_plans"`
}

// PriceTable resolves a model name to its price.
type PriceTable struct {
	prices map[string]Price
}

// DefaultPrices returns the built-in price table.
func DefaultPrices() (map[string]Price, error) {
	var file struct {
		Models map[string]Price `json:"models"`
	}
	if errDecode := json.Unmarshal(defaultPricesJSON, &file); errDecode != nil {
		return nil, fmt.Errorf("usagestats: decode built-in prices: %w", errDecode)
	}
	return file.Models, nil
}

// NewPriceTable builds a price table from the built-in prices overlaid with overrides.
func NewPriceTable(overrides map[string]Price) (*PriceTable, error) {
	defaults, errDefaults := DefaultPrices()
	if errDefaults != nil {
		return nil, errDefaults
	}
	prices := make(map[string]Price, len(defaults)+len(overrides))
	for model, price := range defaults {
		prices[normalizeModel(model)] = price
	}
	for model, price := range overrides {
		prices[normalizeModel(model)] = price
	}
	return &PriceTable{prices: prices}, nil
}

// Lookup returns the price for model. It matches the exact name first, then
// the longest priced name that model extends at a separator, so dated or
// suffixed variants ("claude-opus-4-5-20251101", "gemini-4-argon-high")
// resolve to their base model while "gpt-5.4" never matches "gpt-5".
func (t *PriceTable) Lookup(model string) (Price, bool) {
	if t == nil {
		return Price{}, false
	}
	model = normalizeModel(model)
	if price, ok := t.prices[model]; ok {
		return price, true
	}
	for end := len(model) - 1; end > 0; end-- {
		if isModelNameChar(model[end]) {
			continue
		}
		if price, ok := t.prices[model[:end]]; ok {
			return price, true
		}
	}
	return Price{}, false
}

func normalizeModel(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// isModelNameChar reports whether c continues a model name component. Dots
// count, so version numbers are never split ("5.4" is one component).
func isModelNameChar(c byte) bool {
	return c == '.' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')
}

// cost returns the USD cost of the given token buckets at this price.
func (p Price) cost(input, cacheRead, cacheWrite, cacheWrite1h, output int64) float64 {
	cacheReadPrice := p.Input
	if p.CacheRead != nil {
		cacheReadPrice = *p.CacheRead
	}
	write5mPrice := p.Input
	if p.CacheWrite5m != nil {
		write5mPrice = *p.CacheWrite5m
	}
	write1hPrice := write5mPrice
	if p.CacheWrite1h != nil {
		write1hPrice = *p.CacheWrite1h
	}
	// Cache writes without a reported TTL are billed at the default (5m) rate.
	otherWrites := max(cacheWrite-cacheWrite1h, 0)
	total := float64(input)*p.Input +
		float64(cacheRead)*cacheReadPrice +
		float64(otherWrites)*write5mPrice +
		float64(cacheWrite1h)*write1hPrice +
		float64(output)*p.Output
	return total / 1e6
}

// priceMetrics fills in the cost of m, the summed token buckets of one model's
// rows, and reports whether the model has a price.
func (t *PriceTable) priceMetrics(model string, m *Metrics) bool {
	price, ok := t.Lookup(model)
	if !ok {
		m.UnpricedTokens = m.TotalTokens
		return false
	}
	m.CostUSD = price.cost(m.InputTokens, m.CacheReadTokens, m.CacheWriteTokens, m.CacheWrite1hTokens, m.OutputTokens+m.ReasoningTokens)
	m.UnpricedTokens = m.UnclassifiedTokens
	return true
}

// Validate reports whether the settings are well formed.
func (s Settings) Validate() error {
	seen := make(map[string]string, len(s.ModelPrices))
	for model, price := range s.ModelPrices {
		normalized := normalizeModel(model)
		if normalized == "" {
			return errors.New("model price has an empty model name")
		}
		// Names match case-insensitively, so two spellings of one model would
		// make its price depend on map order.
		if other, duplicate := seen[normalized]; duplicate {
			return fmt.Errorf("models %q and %q are the same model", min(model, other), max(model, other))
		}
		seen[normalized] = model
		for _, value := range []*float64{&price.Input, &price.Output, price.CacheRead, price.CacheWrite5m, price.CacheWrite1h} {
			if value != nil && !validAmount(*value) {
				return fmt.Errorf("model %q has an invalid price", model)
			}
		}
	}
	for _, plan := range s.AccountPlans {
		if strings.TrimSpace(plan.Account) == "" {
			return errors.New("account plan has an empty account")
		}
		if !validAmount(plan.MonthlyUSD) {
			return fmt.Errorf("account %q has an invalid monthly price", plan.Account)
		}
	}
	return nil
}

func validAmount(v float64) bool {
	return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

// Settings returns the stored settings, or empty settings when none are stored.
func (s *Store) Settings(ctx context.Context) (Settings, error) {
	settings := Settings{ModelPrices: map[string]Price{}, AccountPlans: []AccountPlan{}}
	if s == nil {
		return settings, errors.New("usagestats: store is closed")
	}
	var raw string
	errScan := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", settingsKey).Scan(&raw)
	if errors.Is(errScan, sql.ErrNoRows) {
		return settings, nil
	}
	if errScan != nil {
		return settings, fmt.Errorf("usagestats: read settings: %w", errScan)
	}
	if errDecode := json.Unmarshal([]byte(raw), &settings); errDecode != nil {
		return settings, fmt.Errorf("usagestats: decode settings: %w", errDecode)
	}
	if settings.ModelPrices == nil {
		settings.ModelPrices = map[string]Price{}
	}
	if settings.AccountPlans == nil {
		settings.AccountPlans = []AccountPlan{}
	}
	return settings, nil
}

// SaveSettings validates and stores the settings.
func (s *Store) SaveSettings(ctx context.Context, settings Settings) error {
	if s == nil {
		return errors.New("usagestats: store is closed")
	}
	if errValidate := settings.Validate(); errValidate != nil {
		return errValidate
	}
	raw, errEncode := json.Marshal(settings)
	if errEncode != nil {
		return fmt.Errorf("usagestats: encode settings: %w", errEncode)
	}
	_, errExec := s.db.ExecContext(ctx,
		"INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value",
		settingsKey, string(raw),
	)
	if errExec != nil {
		return fmt.Errorf("usagestats: save settings: %w", errExec)
	}
	return nil
}

// priceTable builds the effective price table from the stored settings.
func (s *Store) priceTable(ctx context.Context) (*PriceTable, error) {
	settings, errSettings := s.Settings(ctx)
	if errSettings != nil {
		return nil, errSettings
	}
	return NewPriceTable(settings.ModelPrices)
}
