package pricing

import "strings"

// builtinPrices covers models the remote catalog (models.dev) does not list. They are used only
// when the catalog has no entry, and config entries still override them (ADR 0008).
var builtinPrices = []struct {
	provider string
	prefix   string
	price    Price
}{
	// TypeSafe Jev (System One): $0.042 per 1M input tokens, output tokens free.
	{provider: "typesafe", prefix: "jev", price: Price{Input: f64(0.042), Output: f64(0), CacheRead: f64(0.042)}},
}

func f64(v float64) *float64 { return &v }

func builtinPrice(provider, model string) (Price, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	for _, b := range builtinPrices {
		if b.provider == provider && strings.HasPrefix(model, b.prefix) {
			return b.price, true
		}
	}
	return Price{}, false
}
