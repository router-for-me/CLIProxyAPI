package observability

import (
	"expvar"
	"sync"
	"sync/atomic"
	"time"
)

type durationSummary struct {
	count   atomic.Int64
	totalNS atomic.Int64
	maxNS   atomic.Int64
}

func (s *durationSummary) Observe(d time.Duration) {
	if s == nil || d < 0 {
		return
	}
	nanos := d.Nanoseconds()
	s.count.Add(1)
	s.totalNS.Add(nanos)
	for {
		current := s.maxNS.Load()
		if nanos <= current {
			return
		}
		if s.maxNS.CompareAndSwap(current, nanos) {
			return
		}
	}
}

func (s *durationSummary) snapshotMilliseconds() map[string]any {
	if s == nil {
		return map[string]any{
			"count":    int64(0),
			"total_ms": float64(0),
			"avg_ms":   float64(0),
			"max_ms":   float64(0),
		}
	}
	count := s.count.Load()
	totalNS := s.totalNS.Load()
	totalMS := float64(totalNS) / float64(time.Millisecond)
	avgMS := float64(0)
	if count > 0 {
		avgMS = totalMS / float64(count)
	}
	return map[string]any{
		"count":    count,
		"total_ms": totalMS,
		"avg_ms":   avgMS,
		"max_ms":   float64(s.maxNS.Load()) / float64(time.Millisecond),
	}
}

func (s *durationSummary) snapshot() map[string]any {
	if s == nil {
		return map[string]any{
			"count":    int64(0),
			"total_ns": int64(0),
			"avg_ns":   float64(0),
			"max_ns":   int64(0),
		}
	}
	count := s.count.Load()
	totalNS := s.totalNS.Load()
	avgNS := float64(0)
	if count > 0 {
		avgNS = float64(totalNS) / float64(count)
	}
	return map[string]any{
		"count":    count,
		"total_ns": totalNS,
		"avg_ns":   avgNS,
		"max_ns":   s.maxNS.Load(),
	}
}

var (
	watcherBacklog      atomic.Int64
	refreshQueueSize    atomic.Int64
	refreshDueSize      atomic.Int64
	refreshInFlight     atomic.Int64
	refreshConcurrency  atomic.Int64
	activeStreams       atomic.Int64
	streamFirstByte     durationSummary
	streamCancelToExit  durationSummary
	schedulerLockWaitMu sync.Mutex
	schedulerLockWait   = make(map[string]*durationSummary)
)

func init() {
	expvar.Publish("cliproxy_runtime", expvar.Func(func() any {
		return Snapshot()
	}))
}

func SetWatcherBacklog(size int) {
	watcherBacklog.Store(int64(size))
}

func SetRefreshQueueSize(size int) {
	refreshQueueSize.Store(int64(size))
}

func SetRefreshDueSize(size int) {
	refreshDueSize.Store(int64(size))
}

func SetRefreshInFlight(size int) {
	refreshInFlight.Store(int64(size))
}

func SetRefreshConcurrency(limit int) {
	refreshConcurrency.Store(int64(limit))
}

func IncActiveStreams() {
	activeStreams.Add(1)
}

func DecActiveStreams() {
	activeStreams.Add(-1)
}

func ObserveStreamFirstByte(d time.Duration) {
	streamFirstByte.Observe(d)
}

func ObserveStreamCancelToExit(d time.Duration) {
	streamCancelToExit.Observe(d)
}

func ObserveSchedulerLockWait(path string, d time.Duration) {
	if d < 0 {
		return
	}
	schedulerLockWaitFor(path).Observe(d)
}

func schedulerLockWaitFor(path string) *durationSummary {
	schedulerLockWaitMu.Lock()
	defer schedulerLockWaitMu.Unlock()
	summary := schedulerLockWait[path]
	if summary != nil {
		return summary
	}
	summary = &durationSummary{}
	schedulerLockWait[path] = summary
	return summary
}

func Snapshot() map[string]any {
	lockWait := make(map[string]any)
	schedulerLockWaitMu.Lock()
	for path, summary := range schedulerLockWait {
		lockWait[path] = summary.snapshot()
	}
	schedulerLockWaitMu.Unlock()

	return map[string]any{
		"watcher_backlog": watcherBacklog.Load(),
		"refresh_queue": map[string]any{
			"scheduled":   refreshQueueSize.Load(),
			"due":         refreshDueSize.Load(),
			"inflight":    refreshInFlight.Load(),
			"concurrency": refreshConcurrency.Load(),
		},
		"active_streams":               activeStreams.Load(),
		"stream_first_byte_latency_ms": streamFirstByte.snapshotMilliseconds(),
		"cancel_to_exit_latency_ms":    streamCancelToExit.snapshotMilliseconds(),
		"scheduler_lock_wait_ns":       lockWait,
	}
}
