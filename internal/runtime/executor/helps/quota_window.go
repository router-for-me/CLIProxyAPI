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
