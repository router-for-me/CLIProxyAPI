package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func routingUsageRequestMatches(req *http.Request, auth *coreauth.Auth) bool {
	if req == nil || auth == nil || req.URL == nil || req.Method != http.MethodGet || req.Body != nil ||
		req.URL.Scheme != "https" || req.URL.User != nil || req.URL.RawQuery != "" || req.URL.ForceQuery || req.URL.Fragment != "" || req.URL.RawPath != "" {
		return false
	}
	token, _ := auth.Metadata["access_token"].(string)
	token = strings.TrimSpace(token)
	if token == "" || len(req.Header.Values("Authorization")) != 1 || req.Header.Get("Authorization") != "Bearer "+token || req.Header.Get("Cookie") != "" || req.Header.Get("X-Api-Key") != "" ||
		(req.Host != "" && req.Host != req.URL.Host) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		return req.URL.Host == "api.anthropic.com" && req.URL.Path == "/api/oauth/usage"
	case "codex":
		if req.URL.Host != "chatgpt.com" || req.URL.Path != "/backend-api/wham/usage" {
			return false
		}
		account, _ := auth.Metadata["account_id"].(string)
		values := req.Header.Values("Chatgpt-Account-Id")
		return (strings.TrimSpace(account) == "" && len(values) == 0) || (account != "" && len(values) == 1 && values[0] == account)
	}
	return false
}

func routingUsageAlias(record map[string]json.RawMessage, names ...string) json.RawMessage {
	var selected json.RawMessage
	for _, name := range names {
		if value, ok := record[name]; ok {
			if selected != nil && !bytes.Equal(bytes.TrimSpace(selected), bytes.TrimSpace(value)) {
				return json.RawMessage(`!`)
			}
			selected = value
		}
	}
	return selected
}

func routingUsageSchedule(provider string, body []byte, observedAt time.Time) (coreauth.QuotaResetSchedule, bool) {
	schedule := coreauth.QuotaResetSchedule{ObservedAt: observedAt}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return schedule, false
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		for _, key := range []string{"seven_day", "five_hour"} {
			var window struct {
				ResetAt     string   `json:"resets_at"`
				Utilization *float64 `json:"utilization"`
			}
			if json.Unmarshal(payload[key], &window) != nil || window.Utilization == nil || *window.Utilization < 0 {
				return schedule, false
			}
			if window.ResetAt == "" {
				continue
			}
			at, err := time.Parse(time.RFC3339Nano, window.ResetAt)
			if err != nil {
				return schedule, false
			}
			if key == "seven_day" {
				schedule.WeeklyResetAt = at
			} else {
				schedule.FiveHourResetAt = at
			}
		}
	case "codex":
		var limit map[string]json.RawMessage
		if json.Unmarshal(routingUsageAlias(payload, "rate_limit", "rateLimit"), &limit) != nil || limit == nil {
			return schedule, false
		}
		for _, names := range [][]string{{"primary_window", "primaryWindow"}, {"secondary_window", "secondaryWindow"}} {
			raw := routingUsageAlias(limit, names...)
			if string(bytes.TrimSpace(raw)) == "null" {
				continue
			}
			var window map[string]json.RawMessage
			if json.Unmarshal(raw, &window) != nil || window == nil {
				return schedule, false
			}
			var seconds, resetAt int64
			if json.Unmarshal(routingUsageAlias(window, "limit_window_seconds", "limitWindowSeconds"), &seconds) != nil ||
				json.Unmarshal(routingUsageAlias(window, "reset_at", "resetAt"), &resetAt) != nil || resetAt <= 0 || resetAt > 253402300799 {
				return schedule, false
			}
			at := time.Unix(resetAt, 0).UTC()
			if seconds == 604800 {
				if !schedule.WeeklyResetAt.IsZero() {
					return schedule, false
				}
				schedule.WeeklyResetAt = at
			} else if seconds == 18000 {
				if !schedule.FiveHourResetAt.IsZero() {
					return schedule, false
				}
				schedule.FiveHourResetAt = at
			}
		}
	default:
		return schedule, false
	}
	return schedule, !schedule.WeeklyResetAt.IsZero() || !schedule.FiveHourResetAt.IsZero()
}
