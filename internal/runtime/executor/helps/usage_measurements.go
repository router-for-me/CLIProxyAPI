package helps

import (
	"encoding/json"
	"math/big"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func withUsageMeasurements(detail usage.Detail, node gjson.Result) usage.Detail {
	detail.UsageObserved = node.Exists() && node.IsObject()
	if detail.UsageObserved {
		detail.RawUsage = node.Raw
	}
	detail.CacheCreation5mTokens = node.Get("cache_creation.ephemeral_5m_input_tokens").Int()
	detail.CacheCreation1hTokens = node.Get("cache_creation.ephemeral_1h_input_tokens").Int()
	ticks := node.Get("cost_in_usd_ticks")
	if ticks.Exists() {
		// Decimal arithmetic preserves the provider's 1e-10 USD billing unit.
		if amount, ok := new(big.Int).SetString(ticks.String(), 10); ok && amount.Sign() >= 0 {
			value := new(big.Rat).SetFrac(amount, big.NewInt(10000000000)).FloatString(10)
			detail.CostUSD = &value
		}
	}
	return detail
}

// Keep billing dimensions outside the token object. These must not silently
// disappear when a provider adds hosted tools with independent charges.
func withResponseBilling(detail usage.Detail, root gjson.Result) usage.Detail {
	var raw map[string]any
	if errDecode := json.Unmarshal([]byte(detail.RawUsage), &raw); errDecode != nil {
		raw = map[string]any{}
	}
	if extra := root.Get("tool_usage"); extra.Exists() {
		raw["tool_usage"] = extra.Value()
	}
	for _, candidate := range root.Get("candidates").Array() {
		if candidate.Get("groundingMetadata").Exists() {
			raw["unpriced_server_tools"] = true
		}
	}
	webSearch, fileSearch := 0, 0
	for _, item := range root.Get("output").Array() {
		switch item.Get("type").String() {
		case "web_search_call":
			webSearch++
		case "file_search_call":
			fileSearch++
		case "code_interpreter_call", "shell_call":
			raw["unpriced_server_tools"] = true
		}
	}
	if webSearch > 0 {
		raw["web_search_calls"] = webSearch
	}
	if fileSearch > 0 {
		raw["file_search_calls"] = fileSearch
	}
	if encoded, errEncode := json.Marshal(raw); errEncode == nil && len(raw) > 0 {
		detail.RawUsage = string(encoded)
	}
	return detail
}
