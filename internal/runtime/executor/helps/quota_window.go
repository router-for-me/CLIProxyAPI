package helps

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	claudeFiveHourWindowMinutes = 5 * 60
	claudeSevenDayWindowMinutes = 7 * 24 * 60
)

// QuotaResetHeadersSince returns headers, received at receivedAt, with their
// relative quota resets (a delay-seconds Retry-After and the Codex
// x-codex-*-reset-after-seconds headers) shortened by the time since then, so
// an error read later in the stream reopens when the response said, not that
// long after the error. Only whole seconds count, so a reset never ends early.
// headers is returned as is when nothing changes.
func QuotaResetHeadersSince(headers http.Header, receivedAt time.Time) http.Header {
	elapsed := int64(time.Since(receivedAt) / time.Second)
	if len(headers) == 0 || elapsed <= 0 {
		return headers
	}
	var out http.Header
	set := func(name, value string) {
		if out == nil {
			out = headers.Clone()
		}
		out.Set(name, value)
	}
	if seconds, err := strconv.ParseInt(strings.TrimSpace(headers.Get("Retry-After")), 10, 64); err == nil && seconds >= 0 {
		set("Retry-After", strconv.FormatInt(max(seconds-elapsed, 0), 10))
	}
	for _, slot := range []string{"Primary", "Secondary"} {
		name := "X-Codex-" + slot + "-Reset-After-Seconds"
		if after, err := strconv.ParseFloat(strings.TrimSpace(getHeaderCaseInsensitive(headers, name)), 64); err == nil {
			set(name, strconv.FormatFloat(max(after-float64(elapsed), 0), 'f', -1, 64))
		}
	}
	if out == nil {
		return headers
	}
	return out
}

// CodexExhaustedWindow reports the length, in minutes, of the Codex rate-limit
// window a usage_limit_reached refusal exhausted: of the x-codex-primary-* and
// x-codex-secondary-* windows at 100% use, the one that resets last, because
// the credential stays blocked until then. It also reports when that window
// resets if the headers say so explicitly. It returns 0 and nil when the
// headers name no exhausted window.
func CodexExhaustedWindow(headers http.Header) (int64, *time.Duration) {
	header := func(name string) (float64, bool) {
		value, err := strconv.ParseFloat(strings.TrimSpace(getHeaderCaseInsensitive(headers, name)), 64)
		return value, err == nil
	}
	now := float64(time.Now().Unix())
	var best int64
	var bestReset float64
	var bestExplicit bool
	for _, slot := range []string{"primary", "secondary"} {
		prefix := "X-Codex-" + slot + "-"
		used, okUsed := header(prefix + "used-percent")
		minutes, okMinutes := header(prefix + "window-minutes")
		if !okUsed || used < 100 || !okMinutes || minutes <= 0 {
			continue
		}
		reset, explicit := header(prefix + "reset-at")
		if !explicit {
			var after float64
			if after, explicit = header(prefix + "reset-after-seconds"); explicit {
				reset = now + after
			} else {
				// Without a reset time the longer window is the later one.
				reset = now + minutes*60
			}
		}
		if best == 0 || reset > bestReset {
			best, bestReset, bestExplicit = int64(minutes), reset, explicit
		}
	}
	if !bestExplicit || bestReset <= now {
		return best, nil
	}
	resetAfter := time.Duration((bestReset - now) * float64(time.Second))
	return best, &resetAfter
}
