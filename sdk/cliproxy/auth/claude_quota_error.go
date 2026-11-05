package auth

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// ClaudeQuotaErrorMetadata carries safe routing and recovery hints. NextCheckAt
// is a cache recheck hint, not a promise that upstream quota will have reset.
type ClaudeQuotaErrorMetadata struct {
	Code              string     `json:"code"`
	Retryable         bool       `json:"retryable"`
	Provider          string     `json:"provider"`
	Model             string     `json:"model"`
	ResetAt           *time.Time `json:"reset_at,omitempty"`
	NextCheckAt       *time.Time `json:"next_check_at,omitempty"`
	RetryAfterSeconds int64      `json:"retry_after_seconds,omitempty"`
}

// ClaudeQuotaError preserves the auth Error layout and exposes only locally
// computed Retry-After headers, even when upstream header passthrough is off.
type ClaudeQuotaError struct {
	ClaudeQuotaErrorMetadata
	Message  string `json:"message"`
	Type     string `json:"type"`
	base     *Error
	deadline time.Time
}

func newClaudeQuotaError(code, message, model string, now, reset, nextCheck time.Time) *ClaudeQuotaError {
	status, kind := http.StatusServiceUnavailable, "server_error"
	if code == "claude_quota_exhausted" {
		status, kind = http.StatusTooManyRequests, "rate_limit_error"
	}
	result := &ClaudeQuotaError{
		ClaudeQuotaErrorMetadata: ClaudeQuotaErrorMetadata{Code: code, Retryable: true, Provider: "claude", Model: model},
		Message:                  message, Type: kind,
		base: &Error{Code: code, Message: message, Retryable: true, HTTPStatus: status},
	}
	deadline := reset
	if reset.After(now) {
		result.ResetAt = &reset
	} else {
		if !nextCheck.After(now) {
			nextCheck = now.Add(time.Minute)
		}
		result.NextCheckAt = &nextCheck
		deadline = nextCheck
	}
	result.deadline = deadline
	retryAfter := deadline.Sub(now)
	result.RetryAfterSeconds = int64(retryAfter / time.Second)
	if retryAfter%time.Second != 0 {
		result.RetryAfterSeconds++
	}
	return result
}

func (e *ClaudeQuotaError) Error() string   { return e.base.Error() }
func (e *ClaudeQuotaError) Unwrap() error   { return e.base }
func (e *ClaudeQuotaError) StatusCode() int { return e.base.HTTPStatus }
func (e *ClaudeQuotaError) SafeResponseHeaders() http.Header {
	return safeRetryAfterHeader(time.Until(e.deadline))
}

var claudeQuotaFamilies = []string{"opus", "sonnet", "haiku", "fable", "mythos"}

func claudeQuotaFamily(label string) string {
	words := strings.FieldsFunc(strings.ToLower(label), func(r rune) bool { return r < 'a' || r > 'z' })
	family := ""
	for _, word := range words {
		for _, known := range claudeQuotaFamilies {
			if word == known {
				if family != "" && family != known {
					return ""
				}
				family = known
			}
		}
	}
	return family
}

// blockingReset requires every relevant exhausted window to have a future
// reset. Summary ordering fields alone cannot identify which window blocked.
func (account claudeQuotaAccount) blockingReset(model string, now time.Time) time.Time {
	type window struct {
		Utilization *float64        `json:"utilization"`
		Percent     *float64        `json:"percent"`
		Reset       string          `json:"resets_at"`
		Locked      json.RawMessage `json:"locked_reason"`
		Scope       struct {
			Model struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"model"`
		} `json:"scope"`
	}
	var usage map[string]json.RawMessage
	if json.Unmarshal(account.Usage, &usage) != nil {
		return time.Time{}
	}
	var latest time.Time
	found, unknown := false, false
	family := claudeQuotaFamily(model)
	inspect := func(raw json.RawMessage, scoped string) {
		if scoped != "" && family != "" && scoped != family {
			return
		}
		var w window
		if json.Unmarshal(raw, &w) != nil {
			unknown = true
			return
		}
		value := w.Utilization
		if w.Percent != nil {
			value = w.Percent
		}
		locked := len(w.Locked) > 0 && string(w.Locked) != "null" && string(w.Locked) != `""` && string(w.Locked) != "false"
		if !locked && (value == nil || *value < 100) {
			return
		}
		found = true
		// Administrative locks have no guaranteed quota recovery time.
		reset, err := time.Parse(time.RFC3339Nano, w.Reset)
		if locked || err != nil || !reset.After(now) || reset.Year() >= 9999 {
			unknown = true
			return
		}
		if reset.After(latest) {
			latest = reset
		}
	}
	for key, raw := range usage {
		if string(raw) == "null" {
			continue
		}
		if key == "limits" {
			var limits []json.RawMessage
			if json.Unmarshal(raw, &limits) != nil {
				unknown = true
				continue
			}
			for _, limit := range limits {
				var w window
				if json.Unmarshal(limit, &w) != nil {
					unknown = true
					continue
				}
				scoped := claudeQuotaFamily(w.Scope.Model.ID + " " + w.Scope.Model.DisplayName)
				inspect(limit, scoped)
			}
		} else if key == "five_hour" || key == "seven_day" || strings.HasPrefix(key, "five_hour_") || strings.HasPrefix(key, "seven_day_") {
			inspect(raw, claudeQuotaFamily(strings.TrimPrefix(strings.TrimPrefix(key, "five_hour_"), "seven_day_")))
		}
	}
	if !found || unknown {
		return time.Time{}
	}
	return latest
}
