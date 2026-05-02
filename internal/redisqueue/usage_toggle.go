package redisqueue

import "sync/atomic"

var usageStatisticsEnabled atomic.Bool

func init() {
	usageStatisticsEnabled.Store(true)
}

func SetUsageStatisticsEnabled(enabled bool) { usageStatisticsEnabled.Store(enabled) }

func UsageStatisticsEnabled() bool { return usageStatisticsEnabled.Load() }
