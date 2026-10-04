package management

import (
	"bytes"
	"encoding/json"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// quotaUsageHasCapacity requires current provider observations, never reset timestamps.
func quotaUsageHasCapacity(provider string, body []byte, auth *coreauth.Auth) bool {
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return false
	}
	switch provider {
	case "claude":
		for _, key := range []string{"five_hour", "seven_day"} {
			if !quotaPercentCapacity(payload[key], "utilization") {
				return false
			}
		}
		for _, key := range []string{"seven_day_oauth_apps", "seven_day_opus", "seven_day_sonnet", "seven_day_cowork", "iguana_necktie"} {
			if raw := payload[key]; len(raw) > 0 && string(raw) != "null" && !quotaPercentCapacity(raw, "utilization") {
				return false
			}
		}
		if raw := payload["limits"]; len(raw) > 0 && string(raw) != "null" {
			var limits []struct {
				Kind    string   `json:"kind"`
				Percent *float64 `json:"percent"`
				Scope   *struct {
					Model struct {
						ID   string `json:"id"`
						Name string `json:"display_name"`
					} `json:"model"`
				} `json:"scope"`
			}
			if json.Unmarshal(raw, &limits) != nil {
				return false
			}
			for _, limit := range limits {
				if limit.Percent == nil || *limit.Percent < 0 || *limit.Percent >= 100 {
					return false
				}
				if limit.Kind != "session" && limit.Kind != "weekly_all" && limit.Kind != "weekly_scoped" {
					return false
				}
				if (limit.Kind == "weekly_scoped" || limit.Scope != nil) && (limit.Scope == nil || !quotaModelFamily(limit.Scope.Model.ID, limit.Scope.Model.Name)) {
					return false
				}
			}
		}
		if auth != nil {
			for model, state := range auth.ModelStates {
				if state != nil && (state.Unavailable || state.Quota.Exceeded) && !quotaModelFamily(model, "") {
					return false
				}
			}
		}
		return true
	case "codex":
		if !quotaCodexLimitCapacity(quotaRawAlias(payload, "rate_limit", "rateLimit")) {
			return false
		}
		if raw := quotaRawAlias(payload, "code_review_rate_limit", "codeReviewRateLimit"); len(raw) > 0 && string(raw) != "null" && !quotaCodexLimitCapacity(raw) {
			return false
		}
		if raw := quotaRawAlias(payload, "additional_rate_limits", "additionalRateLimits"); len(raw) > 0 && string(raw) != "null" {
			var limits []map[string]json.RawMessage
			if json.Unmarshal(raw, &limits) != nil {
				return false
			}
			for _, limit := range limits {
				if !quotaCodexLimitCapacity(quotaRawAlias(limit, "rate_limit", "rateLimit")) {
					return false
				}
			}
		}
		return true
	}
	return false
}

func quotaModelFamily(id, name string) bool {
	for _, value := range []string{id, name} {
		value = strings.ToLower(strings.TrimSpace(value))
		for _, family := range []string{"opus", "sonnet", "haiku", "fable"} {
			if value == family || strings.HasPrefix(value, family+" ") || strings.HasPrefix(value, "claude-"+family+"-") || value == "claude-"+family {
				return true
			}
		}
	}
	return false
}
func quotaRawAlias(record map[string]json.RawMessage, keys ...string) json.RawMessage {
	var chosen json.RawMessage
	for _, key := range keys {
		if value, ok := record[key]; ok {
			if chosen != nil && !bytes.Equal(bytes.TrimSpace(chosen), bytes.TrimSpace(value)) {
				return json.RawMessage(`!`)
			}
			chosen = value
		}
	}
	return chosen
}
func quotaPercentCapacity(raw json.RawMessage, keys ...string) bool {
	var record map[string]json.RawMessage
	if json.Unmarshal(raw, &record) != nil || record == nil {
		return false
	}
	var percent float64
	value := quotaRawAlias(record, keys...)
	return len(value) > 0 && string(value) != "null" && json.Unmarshal(value, &percent) == nil && percent >= 0 && percent < 100
}
func quotaCodexLimitCapacity(raw json.RawMessage) bool {
	var record map[string]json.RawMessage
	if json.Unmarshal(raw, &record) != nil || record == nil {
		return false
	}
	for _, keys := range [][]string{{"allowed"}, {"limit_reached", "limitReached"}} {
		value := quotaRawAlias(record, keys...)
		if len(value) > 0 {
			var flag bool
			if json.Unmarshal(value, &flag) != nil || string(value) == "null" || (keys[0] == "allowed" && !flag) || (keys[0] != "allowed" && flag) {
				return false
			}
		}
	}
	for _, keys := range [][]string{{"primary_window", "primaryWindow"}, {"secondary_window", "secondaryWindow"}} {
		if !quotaPercentCapacity(quotaRawAlias(record, keys...), "used_percent", "usedPercent") {
			return false
		}
	}
	return true
}
