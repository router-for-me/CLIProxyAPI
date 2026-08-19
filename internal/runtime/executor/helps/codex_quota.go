package helps

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

const codexRateLimitsEventType = "codex.rate_limits"

// ParseCodexQuotaEventHeaders converts one Codex websocket quota event into the
// same bounded header representation used by HTTP usage observations.
func ParseCodexQuotaEventHeaders(payload []byte) http.Header {
	if gjson.GetBytes(payload, "type").String() != codexRateLimitsEventType {
		return nil
	}

	activeLimit := firstCodexQuotaEventString(payload, "metered_limit_name", "limit_name")
	if activeLimit == "" {
		activeLimit = "codex"
	}
	if !validCodexQuotaEventIdentifier(activeLimit) {
		return nil
	}

	headers := make(http.Header)
	usableWindows := 0
	for _, windowName := range []string{"primary", "secondary"} {
		path := "rate_limits." + windowName
		usedPercent := gjson.GetBytes(payload, path+".used_percent")
		windowMinutes := gjson.GetBytes(payload, path+".window_minutes")
		if !usedPercent.Exists() || !windowMinutes.Exists() {
			continue
		}
		used := usedPercent.Float()
		minutes := windowMinutes.Int()
		if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || minutes <= 0 {
			continue
		}

		prefix := "X-Codex-" + strings.ToUpper(windowName[:1]) + windowName[1:] + "-"
		resetHeader := ""
		resetValue := ""
		if resetAt := gjson.GetBytes(payload, path+".reset_at"); resetAt.Exists() && resetAt.Int() > 0 {
			resetHeader = prefix + "Reset-At"
			resetValue = strconv.FormatInt(resetAt.Int(), 10)
		} else if resetAfter := gjson.GetBytes(payload, path+".reset_after_seconds"); resetAfter.Exists() && resetAfter.Int() >= 0 {
			resetHeader = prefix + "Reset-After-Seconds"
			resetValue = strconv.FormatInt(resetAfter.Int(), 10)
		} else {
			continue
		}
		headers.Set(prefix+"Used-Percent", strconv.FormatFloat(used, 'f', -1, 64))
		headers.Set(prefix+"Window-Minutes", strconv.FormatInt(minutes, 10))
		headers.Set(resetHeader, resetValue)
		usableWindows++
	}
	if usableWindows == 0 {
		return nil
	}

	headers.Set("X-Codex-Active-Limit", activeLimit)
	if planType := firstCodexQuotaEventString(payload, "plan_type"); validCodexQuotaEventText(planType) {
		headers.Set("X-Codex-Plan-Type", planType)
	}
	return headers
}

func firstCodexQuotaEventString(payload []byte, paths ...string) string {
	for _, path := range paths {
		if value := strings.TrimSpace(gjson.GetBytes(payload, path).String()); value != "" {
			return value
		}
	}
	return ""
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
