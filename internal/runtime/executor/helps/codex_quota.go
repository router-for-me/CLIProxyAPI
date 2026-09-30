package helps

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	codexRateLimitsEventType      = "codex.rate_limits"
	codexQuotaAdditionalHeaderKey = "X-Codex-Additional-"
	maxCodexAdditionalRateLimits  = 8
)

// ParseCodexQuotaEventHeaders converts one Codex websocket quota event into the
// same bounded header representation used by HTTP usage observations. Codex
// sends additional_rate_limits as an object on the websocket path, while the
// /wham/usage probe exposes the equivalent data as an array; both forms are
// normalized into namespaced X-Codex headers here.
//
// The websocket payload identifies each additional limit only by limit name
// ("GPT-5.3-Codex-Spark"), while HTTP responses namespace the same limit by a
// short name (x-codex-bengalfox-*). The two forms therefore cannot produce
// identical header names; the X-Codex-Additional- prefix marks the websocket
// origin, and the snapshot-replacement semantics in QuotaState keep the two
// spellings from accumulating side by side.
func ParseCodexQuotaEventHeaders(payload []byte) http.Header {
	if len(payload) == 0 {
		return nil
	}

	eventKind := codexQuotaEventKind(payload)
	if eventKind == codexQuotaEventOther {
		return nil
	}
	root := gjson.ParseBytes(payload)
	if eventKind == codexQuotaEventError {
		return parseCodexQuotaHeadersObject(root.Get("headers"))
	}

	headers := make(http.Header)
	hasQuotaData := false

	baseRateLimits := firstCodexQuotaResult(root, "rate_limits", "rateLimit")
	if addCodexQuotaRateLimitHeaders(headers, "X-Codex-", baseRateLimits) {
		// The main rate-limit object has no user-facing name in the websocket
		// payload, so its fields retain the HTTP response header names.
		hasQuotaData = true
	}

	additional := firstCodexQuotaResult(root, "additional_rate_limits", "additionalRateLimits")
	additionalCount := 0
	if additional.Exists() && (additional.IsObject() || additional.IsArray()) {
		additional.ForEach(func(key, value gjson.Result) bool {
			if additionalCount >= maxCodexAdditionalRateLimits {
				return false
			}
			limitName := strings.TrimSpace(key.String())
			if additional.IsArray() {
				limitName = firstCodexQuotaResultString(value, "limit_name", "limitName", "name")
			}
			if limitName == "" {
				return true
			}
			identifier := normalizeCodexQuotaHeaderIdentifier(limitName)
			if identifier == "" {
				return true
			}
			rateInfo := firstCodexQuotaResult(value, "rate_limit", "rateLimit")
			if !rateInfo.Exists() {
				rateInfo = value
			}
			prefix := codexQuotaAdditionalHeaderKey + identifier + "-"
			if addCodexQuotaRateLimitHeaders(headers, prefix, rateInfo) {
				// The limit name is upstream-controlled and lands in the
				// plain-text request log, so reject control characters here the
				// same way the plan type is validated below.
				if validCodexQuotaEventText(limitName) {
					headers.Set(prefix+"Limit-Name", limitName)
				}
				hasQuotaData = true
				additionalCount++
			}
			return true
		})
	}

	codeReview := firstCodexQuotaResult(root, "code_review_rate_limits", "codeReviewRateLimits")
	if addCodexQuotaRateLimitHeaders(headers, "X-Codex-Code-Review-", codeReview) {
		hasQuotaData = true
	}

	credits := firstCodexQuotaResult(root, "credits")
	if credits.Exists() && credits.IsObject() {
		hasQuotaData = setCodexQuotaScalarHeader(headers, "X-Codex-Credits-Has-Credits", credits, "has_credits", "hasCredits") || hasQuotaData
		hasQuotaData = setCodexQuotaScalarHeader(headers, "X-Codex-Credits-Unlimited", credits, "unlimited") || hasQuotaData
		hasQuotaData = setCodexQuotaScalarHeader(headers, "X-Codex-Credits-Balance", credits, "balance") || hasQuotaData
	}

	if !hasQuotaData {
		return nil
	}
	// A malformed active-limit name only invalidates that one header; the
	// window percentages collected above stay valid and must not be discarded.
	activeLimit := firstCodexQuotaResultString(root, "metered_limit_name", "meteredLimitName", "limit_name", "limitName")
	if validCodexQuotaEventIdentifier(activeLimit) {
		headers.Set("X-Codex-Active-Limit", activeLimit)
	}
	if planType := firstCodexQuotaResultString(root, "plan_type", "planType"); validCodexQuotaEventText(planType) {
		headers.Set("X-Codex-Plan-Type", planType)
	}
	return headers
}

const (
	codexQuotaEventOther = iota
	codexQuotaEventError
	codexQuotaEventRateLimits
)

// codexQuotaEventKind performs an allocation-free discriminator check for the
// common non-quota websocket event path. Codex frames carry the top-level
// type field within their opening bytes (after sequence_number at most), so
// only a bounded prefix is scanned instead of the whole frame; full JSON
// parsing is reserved for quota and error events.
func codexQuotaEventKind(payload []byte) int {
	const scanLimit = 256
	end := len(payload)
	if end > scanLimit {
		end = scanLimit
	}
	for i := 0; i+6 < end; i++ {
		if payload[i] != '"' || payload[i+1] != 't' || payload[i+2] != 'y' || payload[i+3] != 'p' || payload[i+4] != 'e' || payload[i+5] != '"' {
			continue
		}
		j := i + 6
		for j < end && (payload[j] == ' ' || payload[j] == '\t' || payload[j] == '\r' || payload[j] == '\n') {
			j++
		}
		if j >= end || payload[j] != ':' {
			continue
		}
		j++
		for j < end && (payload[j] == ' ' || payload[j] == '\t' || payload[j] == '\r' || payload[j] == '\n') {
			j++
		}
		if j+7 <= end && payload[j] == '"' && payload[j+1] == 'e' && payload[j+2] == 'r' && payload[j+3] == 'r' && payload[j+4] == 'o' && payload[j+5] == 'r' && payload[j+6] == '"' {
			return codexQuotaEventError
		}
		const rateLimits = `"codex.rate_limits"`
		if j+len(rateLimits) <= end {
			matched := true
			for offset := range rateLimits {
				if payload[j+offset] != rateLimits[offset] {
					matched = false
					break
				}
			}
			if matched {
				return codexQuotaEventRateLimits
			}
		}
	}
	return codexQuotaEventOther
}

func addCodexQuotaRateLimitHeaders(headers http.Header, prefix string, rateInfo gjson.Result) bool {
	if !rateInfo.Exists() || !rateInfo.IsObject() {
		return false
	}

	changed := false
	if setCodexQuotaScalarHeaderFromResult(headers, prefix+"Allowed", rateInfo, "allowed") {
		changed = true
	}
	if setCodexQuotaScalarHeaderFromResult(headers, prefix+"Limit-Reached", rateInfo, "limit_reached", "limitReached") {
		changed = true
	}
	for _, windowName := range []string{"primary", "secondary"} {
		window := firstCodexQuotaResult(rateInfo, windowName)
		if !window.Exists() || !window.IsObject() {
			continue
		}
		used := firstCodexQuotaResult(window, "used_percent", "usedPercent")
		minutes := firstCodexQuotaResult(window, "window_minutes", "windowMinutes")
		resetAfter := firstCodexQuotaResult(window, "reset_after_seconds", "resetAfterSeconds")
		resetAt := firstCodexQuotaResult(window, "reset_at", "resetAt")
		hasResetAfter := resetAfter.Exists() && resetAfter.Int() >= 0
		hasResetAt := resetAt.Exists() && resetAt.Int() > 0
		if !used.Exists() || !minutes.Exists() ||
			used.Float() < 0 || used.Float() > 100 || minutes.Int() <= 0 ||
			(!hasResetAfter && !hasResetAt) {
			continue
		}
		windowPrefix := prefix + strings.ToUpper(windowName[:1]) + windowName[1:] + "-"
		setCodexQuotaScalarHeaderFromResult(headers, windowPrefix+"Used-Percent", window, "used_percent", "usedPercent")
		setCodexQuotaScalarHeaderFromResult(headers, windowPrefix+"Window-Minutes", window, "window_minutes", "windowMinutes")
		if hasResetAfter {
			setCodexQuotaScalarHeaderFromResult(headers, windowPrefix+"Reset-After-Seconds", window, "reset_after_seconds", "resetAfterSeconds")
		}
		if hasResetAt {
			setCodexQuotaScalarHeaderFromResult(headers, windowPrefix+"Reset-At", window, "reset_at", "resetAt")
		}
		changed = true
	}
	return changed
}

func parseCodexQuotaHeadersObject(headersNode gjson.Result) http.Header {
	if !headersNode.Exists() || !headersNode.IsObject() {
		return nil
	}
	headers := make(http.Header)
	headersNode.ForEach(func(key, value gjson.Result) bool {
		name := http.CanonicalHeaderKey(strings.TrimSpace(key.String()))
		if !isCodexQuotaHeaderName(name) {
			return true
		}
		if raw := codexQuotaScalarValue(value); raw != "" {
			headers.Set(name, raw)
		}
		return true
	})
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func isCodexQuotaHeaderName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "retry-after" || strings.HasPrefix(lower, "x-ratelimit-") {
		return true
	}
	if lower == "x-codex-active-limit" || lower == "x-codex-plan-type" ||
		strings.HasPrefix(lower, "x-codex-credits-") {
		return true
	}
	if !strings.HasPrefix(lower, "x-codex-") {
		return false
	}
	for _, marker := range []string{
		"-allowed",
		"-limit-reached",
		"-limit-name",
		"-used-percent",
		"-window-minutes",
		"-reset-after-seconds",
		"-reset-at",
		"-over-secondary-limit-percent",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func setCodexQuotaScalarHeader(headers http.Header, name string, object gjson.Result, paths ...string) bool {
	return setCodexQuotaScalarHeaderFromResult(headers, name, object, paths...)
}

func setCodexQuotaScalarHeaderFromResult(headers http.Header, name string, object gjson.Result, paths ...string) bool {
	value := firstCodexQuotaResult(object, paths...)
	if !value.Exists() {
		return false
	}
	raw := codexQuotaScalarValue(value)
	if raw == "" {
		return false
	}
	headers.Set(name, raw)
	return true
}

func codexQuotaScalarValue(value gjson.Result) string {
	switch value.Type {
	case gjson.String:
		return strings.TrimSpace(value.String())
	case gjson.Number, gjson.True, gjson.False:
		return strings.TrimSpace(value.Raw)
	default:
		return ""
	}
}

func firstCodexQuotaResult(object gjson.Result, paths ...string) gjson.Result {
	for _, path := range paths {
		value := object.Get(path)
		if value.Exists() && value.Type != gjson.Null {
			return value
		}
	}
	return gjson.Result{}
}

func firstCodexQuotaResultString(object gjson.Result, paths ...string) string {
	for _, path := range paths {
		value := firstCodexQuotaResult(object, path)
		if raw := codexQuotaScalarValue(value); raw != "" {
			return raw
		}
	}
	return ""
}

func normalizeCodexQuotaHeaderIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(value))
	lastDash := false
	for _, char := range value {
		switch {
		case (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '_':
			builder.WriteRune(char)
			lastDash = false
		case char == '-':
			builder.WriteRune(char)
			lastDash = false
		default:
			if !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	identifier := strings.Trim(builder.String(), "-_.")
	if identifier == "" || !validCodexQuotaEventIdentifier(identifier) {
		return ""
	}
	return identifier
}

func validCodexQuotaEventIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func validCodexQuotaEventText(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "\r\n")
}
