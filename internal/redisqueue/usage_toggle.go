package redisqueue

import "sync/atomic"

var usageStatisticsEnabled atomic.Bool

func init() {
	usageStatisticsEnabled.Store(true)
}

// SetUsageStatisticsEnabled toggles whether usage records are enqueued into the redisqueue payload buffer.
// This is controlled by the config field `usage-statistics-enabled` and the corresponding management API.
func SetUsageStatisticsEnabled(enabled bool) { usageStatisticsEnabled.Store(enabled) }

// UsageStatisticsEnabled reports whether the usage queue plugin should publish records.
func UsageStatisticsEnabled() bool { return usageStatisticsEnabled.Load() }
