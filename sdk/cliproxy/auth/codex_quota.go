package auth

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CodexQuotaWindow is a quota window reported by the Codex upstream.
type CodexQuotaWindow struct {
	UsedPercent   float64
	WindowMinutes int64
	ResetAt       time.Time
}

// CodexQuotaHeaders contains quota windows parsed from an upstream response.
type CodexQuotaHeaders struct {
	Primary   *CodexQuotaWindow
	Secondary *CodexQuotaWindow
}

// ParseCodexQuotaHeaders extracts Codex quota windows from upstream response headers.
func ParseCodexQuotaHeaders(headers http.Header) (CodexQuotaHeaders, bool) {
	if len(headers) == 0 {
		return CodexQuotaHeaders{}, false
	}
	primary := parseCodexQuotaWindow(headers, "X-Codex-Primary")
	secondary := parseCodexQuotaWindow(headers, "X-Codex-Secondary")
	if primary == nil && secondary == nil {
		return CodexQuotaHeaders{}, false
	}
	return CodexQuotaHeaders{Primary: primary, Secondary: secondary}, true
}

func parseCodexQuotaWindow(headers http.Header, prefix string) *CodexQuotaWindow {
	usedRaw := strings.TrimSpace(headers.Get(prefix + "-Used-Percent"))
	if usedRaw == "" {
		return nil
	}
	used, errUsed := strconv.ParseFloat(usedRaw, 64)
	if errUsed != nil {
		return nil
	}
	window := &CodexQuotaWindow{UsedPercent: used}
	if minutes, errMinutes := strconv.ParseInt(strings.TrimSpace(headers.Get(prefix+"-Window-Minutes")), 10, 64); errMinutes == nil && minutes > 0 {
		window.WindowMinutes = minutes
	}
	if resetAt, errReset := strconv.ParseInt(strings.TrimSpace(headers.Get(prefix+"-Reset-At")), 10, 64); errReset == nil && resetAt > 0 {
		window.ResetAt = time.Unix(resetAt, 0)
	}
	return window
}

func (q CodexQuotaHeaders) exhaustedWindow() *CodexQuotaWindow {
	for _, window := range []*CodexQuotaWindow{q.Primary, q.Secondary} {
		if window != nil && window.UsedPercent >= 100 {
			return window
		}
	}
	return nil
}
