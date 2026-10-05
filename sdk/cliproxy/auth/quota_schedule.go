package auth

import "time"

// QuotaResetSchedule is an in-memory observation from a provider usage endpoint.
// It does not determine credential availability or release quota cooldowns.
type QuotaResetSchedule struct {
	WeeklyResetAt   time.Time
	FiveHourResetAt time.Time
	ObservedAt      time.Time
}
