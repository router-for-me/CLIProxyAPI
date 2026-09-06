package auth

import (
	"strconv"
	"sync"
	"sync/atomic"
)

var (
	upstreamSuccessCount atomic.Int64
	upstreamErrorCounts  sync.Map // status label -> *atomic.Int64
)

func recordUpstreamResult(result Result) {
	if result.Success {
		upstreamSuccessCount.Add(1)
		return
	}
	status := "unknown"
	if result.Error != nil && result.Error.HTTPStatus > 0 {
		status = strconv.Itoa(result.Error.HTTPStatus)
	}
	value, _ := upstreamErrorCounts.LoadOrStore(status, &atomic.Int64{})
	counter, _ := value.(*atomic.Int64)
	if counter != nil {
		counter.Add(1)
	}
}

// SnapshotUpstreamMetrics returns process-wide upstream success and error counts.
func SnapshotUpstreamMetrics() (success int64, errorsByStatus map[string]int64) {
	success = upstreamSuccessCount.Load()
	errorsByStatus = make(map[string]int64)
	upstreamErrorCounts.Range(func(key, value any) bool {
		label, _ := key.(string)
		counter, _ := value.(*atomic.Int64)
		if label == "" || counter == nil {
			return true
		}
		errorsByStatus[label] = counter.Load()
		return true
	})
	return success, errorsByStatus
}

// ResetUpstreamMetricsForTest clears process-wide upstream counters. Tests only.
func ResetUpstreamMetricsForTest() {
	upstreamSuccessCount.Store(0)
	upstreamErrorCounts.Range(func(key, _ any) bool {
		upstreamErrorCounts.Delete(key)
		return true
	})
}
