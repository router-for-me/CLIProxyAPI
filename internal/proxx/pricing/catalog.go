// Package pricing resolves per-token model prices (config overrides + models.dev catalog)
// and attaches a per-request cost to usage records. See docs/proxx/decisions/0004-pricing-and-cost.md.
package pricing

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// Price holds USD per 1M token prices. A nil field means the price is unknown.
type Price struct {
	Input      *float64 `json:"input,omitempty"`
	Output     *float64 `json:"output,omitempty"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
	Reasoning  *float64 `json:"reasoning,omitempty"`
}

// IsZero reports whether no price field is known.
func (p Price) IsZero() bool {
	return p.Input == nil && p.Output == nil && p.CacheRead == nil && p.CacheWrite == nil && p.Reasoning == nil
}

// mergeOver returns p with nil fields filled from base.
func (p Price) mergeOver(base Price) Price {
	if p.Input == nil {
		p.Input = base.Input
	}
	if p.Output == nil {
		p.Output = base.Output
	}
	if p.CacheRead == nil {
		p.CacheRead = base.CacheRead
	}
	if p.CacheWrite == nil {
		p.CacheWrite = base.CacheWrite
	}
	if p.Reasoning == nil {
		p.Reasoning = base.Reasoning
	}
	return p
}

// Catalog maps provider ID (models.dev naming) → model ID → price.
type Catalog struct {
	providers map[string]map[string]Price
	// normalized maps provider → normalized model ID → price, for date/variant-insensitive matches.
	normalized map[string]map[string]Price
	models     int
	order      []string
}

// Len returns the number of priced models in the catalog.
func (c *Catalog) Len() int {
	if c == nil {
		return 0
	}
	return c.models
}

func (c *Catalog) get(provider, model string) (Price, bool) {
	if c == nil {
		return Price{}, false
	}
	if models, ok := c.providers[provider]; ok {
		if price, okModel := models[model]; okModel {
			return price, true
		}
	}
	return Price{}, false
}

func (c *Catalog) getNormalized(provider, model string) (Price, bool) {
	if c == nil {
		return Price{}, false
	}
	if models, ok := c.normalized[provider]; ok {
		if price, okModel := models[model]; okModel {
			return price, true
		}
	}
	return Price{}, false
}

// ParseCatalog parses the models.dev api.json format:
// {providerID: {id, models: {modelID: {cost: {input, output, cache_read, cache_write, reasoning}}}}}.
// Models without a cost object are skipped. Context tiers are ignored (base tier only).
func ParseCatalog(data []byte) (*Catalog, error) {
	root := ojson.ParseBytes(data)
	if !root.IsObject() {
		return nil, fmt.Errorf("price catalog: root is not an object")
	}
	c := &Catalog{providers: map[string]map[string]Price{}, normalized: map[string]map[string]Price{}}
	root.ForEach(func(providerKey, provider ojson.Result) bool {
		if !provider.IsObject() {
			return true
		}
		providerID := strings.ToLower(strings.TrimSpace(providerKey.String()))
		models := provider.Get("models")
		if providerID == "" || !models.IsObject() {
			return true
		}
		priced := map[string]Price{}
		models.ForEach(func(modelKey, model ojson.Result) bool {
			cost := model.Get("cost")
			if !cost.IsObject() {
				return true
			}
			price := Price{
				Input:      numberPtr(cost.Get("input")),
				Output:     numberPtr(cost.Get("output")),
				CacheRead:  numberPtr(cost.Get("cache_read")),
				CacheWrite: numberPtr(cost.Get("cache_write")),
				Reasoning:  numberPtr(cost.Get("reasoning")),
			}
			if price.IsZero() {
				return true
			}
			priced[strings.ToLower(modelKey.String())] = price
			return true
		})
		if len(priced) == 0 {
			return true
		}
		c.providers[providerID] = priced
		c.models += len(priced)
		norm := map[string]Price{}
		normFrom := map[string]string{}
		for id, price := range priced {
			key := normalizeModelID(id)
			if key == id {
				continue
			}
			if _, exact := priced[key]; exact {
				continue
			}
			// Prefer the lexicographically greatest source ID (usually the newest dated release).
			if prev, seen := normFrom[key]; !seen || id > prev {
				norm[key] = price
				normFrom[key] = id
			}
		}
		c.normalized[providerID] = norm
		return true
	})
	if c.models == 0 {
		return nil, fmt.Errorf("price catalog: no priced models")
	}
	c.order = buildProviderOrder(c)
	return c, nil
}

// ValidateCatalog reports whether data is a usable price catalog.
func ValidateCatalog(data []byte) error {
	_, err := ParseCatalog(data)
	return err
}

func numberPtr(r ojson.Result) *float64 {
	if r.Type != ojson.Number {
		return nil
	}
	v := r.Float()
	if v < 0 {
		return nil
	}
	return &v
}
