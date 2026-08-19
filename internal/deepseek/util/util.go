// Package util provides small shared helpers for the DeepSeek provider port.
package util

import "strings"

// ToBool loosely converts an interface value to bool. Only explicit bool
// values are recognised; anything else is false.
func ToBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

// IntFrom converts a JSON-decoded numeric value (float64, int, int64) to int.
func IntFrom(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

// ResolveThinkingEnabled returns the effective thinking flag for a request,
// preferring explicit overrides over the model default.
func ResolveThinkingEnabled(req map[string]any, defaultEnabled bool) bool {
	if enabled, ok := ResolveThinkingOverride(req); ok {
		return enabled
	}
	return defaultEnabled
}

// ResolveThinkingOverride inspects a request for an explicit thinking/reasoning
// override. Returns (value, ok); ok is false when no override is present.
func ResolveThinkingOverride(req map[string]any) (bool, bool) {
	if req == nil {
		return false, false
	}
	if enabled, ok := parseThinkingSetting(req["thinking"]); ok {
		return enabled, true
	}
	if enabled, ok := parseReasoningSetting(req["reasoning"]); ok {
		return enabled, true
	}
	if extraBody, ok := req["extra_body"].(map[string]any); ok {
		if enabled, ok := parseThinkingSetting(extraBody["thinking"]); ok {
			return enabled, true
		}
		if enabled, ok := parseReasoningSetting(extraBody["reasoning"]); ok {
			return enabled, true
		}
		if enabled, ok := parseReasoningEffort(extraBody["reasoning_effort"]); ok {
			return enabled, true
		}
	}
	if enabled, ok := parseReasoningEffort(req["reasoning_effort"]); ok {
		return enabled, true
	}
	return false, false
}

func parseThinkingSetting(raw any) (bool, bool) {
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "enabled", "enable", "on", "true":
			return true, true
		case "disabled", "disable", "off", "false", "none":
			return false, true
		default:
			return false, false
		}
	case map[string]any:
		if typ, ok := v["type"]; ok {
			return parseThinkingSetting(typ)
		}
	}
	return false, false
}

func parseReasoningSetting(raw any) (bool, bool) {
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		return parseReasoningEffort(v)
	case map[string]any:
		for _, key := range []string{"effort", "type", "enabled"} {
			if enabled, ok := parseReasoningSetting(v[key]); ok {
				return enabled, true
			}
		}
	}
	return false, false
}

func parseReasoningEffort(raw any) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(toString(raw))) {
	case "minimal", "low", "medium", "high", "xhigh":
		return true, true
	case "none", "disabled", "disable", "off", "false":
		return false, true
	default:
		return false, false
	}
}

func toString(raw any) string {
	if s, ok := raw.(string); ok {
		return s
	}
	return ""
}
