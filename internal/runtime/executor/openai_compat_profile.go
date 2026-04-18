package executor

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAICompatProfile struct {
	Kind                     string
	SupportsResponses        bool
	SupportsStreamUsage      bool
	SupportsParallelToolCall bool
	SupportsReasoning        bool
	SupportsMetadata         bool
	SupportsStore            bool
	DefaultHeaders           map[string]string
}

func genericOpenAICompatProfile() openAICompatProfile {
	return openAICompatProfile{
		SupportsResponses:        true,
		SupportsStreamUsage:      true,
		SupportsParallelToolCall: true,
		SupportsReasoning:        true,
		SupportsMetadata:         true,
		SupportsStore:            true,
	}
}

var openAICompatProfiles = map[string]openAICompatProfile{
	"kimi": {
		Kind:                     "kimi",
		SupportsResponses:        false,
		SupportsStreamUsage:      false,
		SupportsParallelToolCall: false,
		SupportsReasoning:        false,
		SupportsMetadata:         false,
		SupportsStore:            false,
	},
	"minimax": {
		Kind:                     "minimax",
		SupportsResponses:        false,
		SupportsStreamUsage:      false,
		SupportsParallelToolCall: false,
		SupportsReasoning:        false,
		SupportsMetadata:         false,
		SupportsStore:            false,
	},
	"zhipu": {
		Kind:                     "zhipu",
		SupportsResponses:        false,
		SupportsStreamUsage:      false,
		SupportsParallelToolCall: false,
		SupportsReasoning:        false,
		SupportsMetadata:         false,
		SupportsStore:            false,
	},
	"xfyun": {
		Kind:                     "xfyun",
		SupportsResponses:        false,
		SupportsStreamUsage:      false,
		SupportsParallelToolCall: false,
		SupportsReasoning:        false,
		SupportsMetadata:         false,
		SupportsStore:            false,
	},
	"maas": {
		Kind:                     "maas",
		SupportsResponses:        false,
		SupportsStreamUsage:      false,
		SupportsParallelToolCall: false,
		SupportsReasoning:        false,
		SupportsMetadata:         false,
		SupportsStore:            false,
	},
	"langengyun": {
		Kind:                     "langengyun",
		SupportsResponses:        false,
		SupportsStreamUsage:      false,
		SupportsParallelToolCall: false,
		SupportsReasoning:        false,
		SupportsMetadata:         false,
		SupportsStore:            false,
	},
	"newapi": {
		Kind:                     "newapi",
		SupportsResponses:        false,
		SupportsStreamUsage:      false,
		SupportsParallelToolCall: false,
		SupportsReasoning:        false,
		SupportsMetadata:         false,
		SupportsStore:            false,
	},
}

func openAICompatProfileForKind(kind string) openAICompatProfile {
	normalized := config.NormalizeOpenAICompatibilityKind(kind)
	if profile, ok := openAICompatProfiles[normalized]; ok {
		return profile
	}
	profile := genericOpenAICompatProfile()
	profile.Kind = normalized
	return profile
}

func (e *OpenAICompatExecutor) resolveProfile(auth *cliproxyauth.Auth) openAICompatProfile {
	profile := genericOpenAICompatProfile()
	profile.Kind = ""
	compat := e.resolveCompatConfig(auth)
	if compat == nil {
		if auth != nil && auth.Attributes != nil {
			if kind := config.NormalizeOpenAICompatibilityKind(auth.Attributes["compat_kind"]); kind != "" {
				return openAICompatProfileForKind(kind)
			}
		}
		return profile
	}
	resolved := openAICompatProfileForKind(compat.Kind)
	if len(compat.Headers) > 0 {
		resolved.DefaultHeaders = config.NormalizeHeaders(compat.Headers)
	}
	return resolved
}

func applyOpenAICompatDefaultHeaders(req *http.Request, profile openAICompatProfile) {
	if req == nil || len(profile.DefaultHeaders) == 0 {
		return
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	for key, value := range profile.DefaultHeaders {
		if req.Header.Get(key) != "" {
			continue
		}
		req.Header.Set(key, value)
	}
}

func scrubOpenAICompatPayload(payload []byte, profile openAICompatProfile) []byte {
	if len(payload) == 0 {
		return payload
	}
	if !profile.SupportsStore {
		if updated, err := sjson.DeleteBytes(payload, "store"); err == nil {
			payload = updated
		}
	}
	if !profile.SupportsMetadata {
		if updated, err := sjson.DeleteBytes(payload, "metadata"); err == nil {
			payload = updated
		}
	}
	if !profile.SupportsParallelToolCall {
		if updated, err := sjson.DeleteBytes(payload, "parallel_tool_calls"); err == nil {
			payload = updated
		}
	}
	if !profile.SupportsStreamUsage {
		if updated, err := sjson.DeleteBytes(payload, "stream_options"); err == nil {
			payload = updated
		}
	}
	if !profile.SupportsReasoning {
		for _, path := range []string{"reasoning", "reasoning_effort"} {
			if updated, err := sjson.DeleteBytes(payload, path); err == nil {
				payload = updated
			}
		}
		payload = deleteMessageReasoningContent(payload)
	}
	return payload
}

func deleteMessageReasoningContent(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}
	messages.ForEach(func(key, value gjson.Result) bool {
		if !value.Get("reasoning_content").Exists() {
			return true
		}
		updated := value.Raw
		if next, err := sjson.Delete(updated, "reasoning_content"); err == nil {
			updated = next
		}
		if nextPayload, err := sjson.SetRawBytes(payload, fmt.Sprintf("messages.%s", key.String()), []byte(updated)); err == nil {
			payload = nextPayload
		}
		return true
	})
	return payload
}

func summarizeOpenAICompatError(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" || !gjson.ValidBytes(body) {
		return trimmed
	}
	message := firstNonEmptyJSONValue(body,
		"error.message",
		"message",
		"msg",
		"error.msg",
		"detail",
		"error.detail",
		"reason",
		"error.reason",
		"error.metadata.message",
		"error.metadata.reason",
		"error.details.0.message",
		"error.details.0.reason",
		"error.details.0.description",
	)
	if message == "" {
		return trimmed
	}
	label := firstNonEmptyJSONValue(body, "error.type", "type", "error.code", "code", "error.err_code")
	if label == "" {
		return message
	}
	lowerMessage := strings.ToLower(message)
	lowerLabel := strings.ToLower(label)
	if strings.Contains(lowerMessage, lowerLabel) {
		return message
	}
	return label + ": " + message
}

func firstNonEmptyJSONValue(body []byte, paths ...string) string {
	for _, path := range paths {
		value := gjson.GetBytes(body, path)
		if !value.Exists() {
			continue
		}
		switch value.Type {
		case gjson.String:
			if trimmed := strings.TrimSpace(value.String()); trimmed != "" {
				return trimmed
			}
		case gjson.Number:
			if raw := strings.TrimSpace(value.Raw); raw != "" {
				return raw
			}
		}
	}
	return ""
}

func openAICompatRetryAfter(headers http.Header, body []byte) *time.Duration {
	now := time.Now()
	if headers != nil {
		if retry := parseOpenAICompatRetryAfterString(headers.Get("Retry-After"), false, now); retry != nil {
			return retry
		}
	}

	candidates := []struct {
		path      string
		timestamp bool
	}{
		{path: "retry_after"},
		{path: "retryAfter"},
		{path: "retry_after_seconds"},
		{path: "retryAfterSeconds"},
		{path: "retry_delay"},
		{path: "retryDelay"},
		{path: "reset_after"},
		{path: "resetAfter"},
		{path: "reset_in"},
		{path: "resetIn"},
		{path: "reset_in_seconds"},
		{path: "resetInSeconds"},
		{path: "cooldown"},
		{path: "cooldown_seconds"},
		{path: "cooldownSeconds"},
		{path: "error.retry_after"},
		{path: "error.retryAfter"},
		{path: "error.retry_after_seconds"},
		{path: "error.retryAfterSeconds"},
		{path: "error.retry_delay"},
		{path: "error.retryDelay"},
		{path: "error.reset_after"},
		{path: "error.resetAfter"},
		{path: "error.reset_in"},
		{path: "error.resetIn"},
		{path: "error.reset_in_seconds"},
		{path: "error.resetInSeconds"},
		{path: "error.cooldown"},
		{path: "error.cooldown_seconds"},
		{path: "error.cooldownSeconds"},
		{path: "error.metadata.retry_after"},
		{path: "error.metadata.retry_after_seconds"},
		{path: "error.metadata.retryDelay"},
		{path: "error.metadata.reset_after"},
		{path: "error.metadata.reset_in_seconds"},
		{path: "retry_at", timestamp: true},
		{path: "retryAt", timestamp: true},
		{path: "reset_at", timestamp: true},
		{path: "resetAt", timestamp: true},
		{path: "error.retry_at", timestamp: true},
		{path: "error.retryAt", timestamp: true},
		{path: "error.reset_at", timestamp: true},
		{path: "error.resetAt", timestamp: true},
		{path: "error.metadata.retry_at", timestamp: true},
		{path: "error.metadata.retryAt", timestamp: true},
		{path: "error.metadata.reset_at", timestamp: true},
		{path: "error.metadata.resetAt", timestamp: true},
	}
	for _, candidate := range candidates {
		value := gjson.GetBytes(body, candidate.path)
		if !value.Exists() {
			continue
		}
		if retry := parseOpenAICompatRetryAfterValue(value, candidate.timestamp, now); retry != nil {
			return retry
		}
	}
	return nil
}

func parseOpenAICompatRetryAfterValue(value gjson.Result, timestamp bool, now time.Time) *time.Duration {
	switch value.Type {
	case gjson.String:
		return parseOpenAICompatRetryAfterString(value.String(), timestamp, now)
	case gjson.Number:
		number := value.Float()
		if number <= 0 {
			return nil
		}
		if timestamp {
			return durationUntilUnix(number, now)
		}
		duration := time.Duration(number * float64(time.Second))
		if duration <= 0 {
			return nil
		}
		return &duration
	default:
		return nil
	}
}

func parseOpenAICompatRetryAfterString(raw string, timestamp bool, now time.Time) *time.Duration {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if parsed, err := strconv.ParseFloat(trimmed, 64); err == nil {
		if parsed <= 0 {
			return nil
		}
		if timestamp {
			return durationUntilUnix(parsed, now)
		}
		duration := time.Duration(parsed * float64(time.Second))
		if duration <= 0 {
			return nil
		}
		return &duration
	}
	if duration, err := time.ParseDuration(trimmed); err == nil && duration > 0 {
		return &duration
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, http.TimeFormat} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			duration := time.Until(parsed)
			if duration > 0 {
				return &duration
			}
		}
	}
	if timestamp {
		return nil
	}
	if parsed, err := http.ParseTime(trimmed); err == nil {
		duration := parsed.Sub(now)
		if duration > 0 {
			return &duration
		}
	}
	return nil
}

func durationUntilUnix(value float64, now time.Time) *time.Duration {
	if value <= 0 {
		return nil
	}
	var target time.Time
	switch {
	case value >= 1e12:
		target = time.UnixMilli(int64(value))
	case value >= 1e9:
		target = time.Unix(int64(value), 0)
	default:
		return nil
	}
	duration := target.Sub(now)
	if duration <= 0 {
		return nil
	}
	return &duration
}

func normalizeOpenAICompatStatus(code int, message string) int {
	lower := strings.ToLower(strings.TrimSpace(message))
	switch {
	case openAICompatQuotaLikeMessage(lower) && code != http.StatusTooManyRequests:
		return http.StatusTooManyRequests
	case openAICompatAvailabilityMessage(lower) && (code == http.StatusBadRequest || code == http.StatusForbidden):
		return http.StatusServiceUnavailable
	default:
		return code
	}
}

func openAICompatQuotaLikeMessage(message string) bool {
	return containsAny(message,
		"insufficient_quota",
		"quota exhausted",
		"quota_exhausted",
		"rate limit",
		"rate_limit",
		"too many requests",
		"resource exhausted",
		"额度已用尽",
		"额度不足",
		"频率限制",
	)
}

func openAICompatAvailabilityMessage(message string) bool {
	return containsAny(message,
		"no available key",
		"no available api key",
		"no available channel",
		"channel unavailable",
		"upstream unavailable",
		"provider unavailable",
		"no healthy upstream",
		"no available upstream",
		"无可用 key",
		"无可用key",
		"无可用渠道",
		"渠道不可用",
		"上游不可用",
	)
}

func containsAny(message string, patterns ...string) bool {
	for _, pattern := range patterns {
		if pattern != "" && strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}

func logOpenAICompatUpstreamError(profile openAICompatProfile, auth *cliproxyauth.Auth, routeModel string, statusCode int, retryAfter *time.Duration, contentType string, body []byte) {
	entry := log.WithFields(log.Fields{
		"provider":    profile.KindOrFallback(auth),
		"compat_kind": profile.Kind,
		"model":       strings.TrimSpace(routeModel),
		"status":      statusCode,
	})
	if auth != nil {
		if authID := strings.TrimSpace(auth.ID); authID != "" {
			entry = entry.WithField("auth_id", authID)
		}
		if compatName := strings.TrimSpace(auth.Attributes["compat_name"]); compatName != "" {
			entry = entry.WithField("compat_name", compatName)
		}
	}
	if retryAfter != nil {
		entry = entry.WithField("retry_after", retryAfter.String())
	}
	entry.Warnf("openai compat upstream error: %s", helps.SummarizeErrorBody(contentType, body))
}

func (p openAICompatProfile) KindOrFallback(auth *cliproxyauth.Auth) string {
	if p.Kind != "" {
		return p.Kind
	}
	if auth != nil {
		if auth.Attributes != nil {
			if providerKey := strings.TrimSpace(auth.Attributes["provider_key"]); providerKey != "" {
				return providerKey
			}
		}
		if provider := strings.TrimSpace(auth.Provider); provider != "" {
			return provider
		}
	}
	return "openai-compatibility"
}
