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

// CodexExhaustedWindowMinutes reports the length, in minutes, of the Codex
// rate-limit window a usage_limit_reached refusal exhausted: of the
// x-codex-primary-* and x-codex-secondary-* windows at 100% use, the one that
// resets last, because the credential stays blocked until then. It returns 0
// when the headers name no exhausted window.
func CodexExhaustedWindowMinutes(headers http.Header) int64 {
	header := func(name string) (float64, bool) {
		value, err := strconv.ParseFloat(strings.TrimSpace(getHeaderCaseInsensitive(headers, name)), 64)
		return value, err == nil
	}
	now := float64(time.Now().Unix())
	var best int64
	var bestReset float64
	for _, slot := range []string{"primary", "secondary"} {
		prefix := "X-Codex-" + slot + "-"
		used, okUsed := header(prefix + "used-percent")
		minutes, okMinutes := header(prefix + "window-minutes")
		if !okUsed || used < 100 || !okMinutes || minutes <= 0 {
			continue
		}
		reset, okReset := header(prefix + "reset-at")
		if !okReset {
			if after, okAfter := header(prefix + "reset-after-seconds"); okAfter {
				reset = now + after
			} else {
				// Without a reset time the longer window is the later one.
				reset = now + minutes*60
			}
		}
		if best == 0 || reset > bestReset {
			best, bestReset = int64(minutes), reset
		}
	}
	return best
}
