package helps

import (
	"bytes"
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

// UsageBillingMetadata retains billing dimensions which may arrive in a
// different frame from the final token counters. Observing metadata never
// turns an unmeasured frame into a measured token response.
type UsageBillingMetadata struct {
	fields map[string]any
}

func (b *UsageBillingMetadata) ObservePayload(payload []byte) {
	// Most stream frames contain only text/audio deltas. Avoid parsing their
	// potentially large bodies when no billing dimension can be present.
	candidate := false
	for _, marker := range []string{"\"tool_usage\"", "\"server_tool_use\"", "\"groundingMetadata\"", "\"web_search_call\"", "\"file_search_call\"", "\"code_interpreter_call\"", "\"shell_call\""} {
		if bytes.Contains(payload, []byte(marker)) {
			candidate = true
			break
		}
	}
	if !candidate {
		return
	}
	payload = ExtractStreamJSONPayload(payload)
	if !gjson.ValidBytes(payload) {
		return
	}
	root := gjson.ParseBytes(payload)
	for _, node := range []gjson.Result{root, root.Get("response")} {
		if !node.IsObject() {
			continue
		}
		detail := withResponseBilling(usage.Detail{}, node)
		b.observeDetail(detail)
		for _, path := range []string{"usage", "message.usage"} {
			if raw := node.Get(path); raw.IsObject() {
				b.observeDetail(usage.Detail{RawUsage: raw.Raw})
			}
		}
	}
}

func (b *UsageBillingMetadata) observeDetail(detail usage.Detail) {
	var raw map[string]any
	if json.Unmarshal([]byte(detail.RawUsage), &raw) != nil {
		return
	}
	if b.fields == nil {
		b.fields = make(map[string]any)
	}
	for _, key := range []string{"unpriced_server_tools", "tool_usage", "server_tool_use", "web_search_calls", "file_search_calls"} {
		if value, ok := raw[key]; ok {
			b.fields[key] = mergeBillingMetadata(b.fields[key], value)
		}
	}
}

// Provider stream counters are cumulative snapshots, not additive deltas.
// Keep known keys and the largest count so repeats cannot charge twice and
// later metadata-less frames cannot erase already observed tool usage.
func mergeBillingMetadata(previous, next any) any {
	switch value := next.(type) {
	case map[string]any:
		merged, _ := previous.(map[string]any)
		if merged == nil {
			merged = make(map[string]any)
		}
		for key, child := range value {
			merged[key] = mergeBillingMetadata(merged[key], child)
		}
		return merged
	case float64:
		if old, ok := previous.(float64); ok && old > value {
			return old
		}
	case bool:
		if old, ok := previous.(bool); ok && old {
			return true
		}
	}
	return next
}

func (b *UsageBillingMetadata) Apply(detail usage.Detail) usage.Detail {
	b.observeDetail(detail)
	if len(b.fields) == 0 {
		return detail
	}
	var raw map[string]any
	if json.Unmarshal([]byte(detail.RawUsage), &raw) != nil || raw == nil {
		raw = make(map[string]any)
	}
	for key, value := range b.fields {
		raw[key] = value
	}
	if encoded, errEncode := json.Marshal(raw); errEncode == nil {
		detail.RawUsage = string(encoded)
	}
	return detail
}
