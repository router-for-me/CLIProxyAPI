package auth

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func codexQuotaReservePercent(auth *Auth) float64 {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return 0
	}
	reserve, errParse := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(auth.Metadata["quota_reserve_percent"])), 64)
	if errParse != nil || !(reserve > 0 && reserve < 100) {
		return 0
	}
	return reserve
}

func codexQuotaReserveUntil(auth *Auth, now time.Time) time.Time {
	reserve := codexQuotaReservePercent(auth)
	if reserve == 0 {
		return time.Time{}
	}
	var blockedUntil time.Time
	for _, window := range []string{"Primary", "Secondary"} {
		prefix := "X-Codex-" + window + "-"
		used, errParse := strconv.ParseFloat(strings.TrimSpace(auth.Quota.Signals[prefix+"Used-Percent"]), 64)
		if errParse != nil || !(used >= 100-reserve && used <= 100) {
			continue
		}
		reset := codexQuotaWindowReset(auth.Quota, prefix)
		if reset.After(now) && reset.After(blockedUntil) {
			blockedUntil = reset
		}
	}
	return blockedUntil
}

func codexQuotaWindowReset(quota QuotaState, prefix string) time.Time {
	seconds, errParse := strconv.ParseInt(strings.TrimSpace(quota.Signals[prefix+"Reset-At"]), 10, 64)
	if errParse == nil && seconds > 0 {
		return time.Unix(seconds, 0)
	}
	if quota.ObservedAt.IsZero() {
		return time.Time{}
	}
	delay, errParse := time.ParseDuration(strings.TrimSpace(quota.Signals[prefix+"Reset-After-Seconds"]) + "s")
	if errParse != nil || delay <= 0 {
		return time.Time{}
	}
	return quota.ObservedAt.Add(delay)
}
