package observability

import (
	"testing"
	"time"
)

func TestDurationSummaryObserve(t *testing.T) {
	t.Parallel()

	var summary durationSummary
	summary.Observe(10 * time.Millisecond)
	summary.Observe(30 * time.Millisecond)

	got := summary.snapshotMilliseconds()
	if got["count"].(int64) != 2 {
		t.Fatalf("count = %v, want 2", got["count"])
	}
	if got["max_ms"].(float64) < 30 {
		t.Fatalf("max_ms = %v, want >= 30", got["max_ms"])
	}
	if got["avg_ms"].(float64) < 19.9 || got["avg_ms"].(float64) > 20.1 {
		t.Fatalf("avg_ms = %v, want about 20", got["avg_ms"])
	}
}

func TestSnapshotIncludesExpectedKeys(t *testing.T) {
	t.Parallel()

	SetWatcherBacklog(3)
	SetRefreshQueueSize(5)
	SetRefreshDueSize(2)
	SetRefreshInFlight(1)
	SetRefreshConcurrency(16)
	IncActiveStreams()
	defer DecActiveStreams()
	ObserveSchedulerLockWait("pick_single", 5*time.Millisecond)

	snapshot := Snapshot()
	if snapshot["watcher_backlog"].(int64) < 3 {
		t.Fatalf("watcher_backlog = %v, want >= 3", snapshot["watcher_backlog"])
	}
	if snapshot["active_streams"].(int64) < 1 {
		t.Fatalf("active_streams = %v, want >= 1", snapshot["active_streams"])
	}
	refreshQueue, ok := snapshot["refresh_queue"].(map[string]any)
	if !ok {
		t.Fatalf("refresh_queue type = %T, want map[string]any", snapshot["refresh_queue"])
	}
	if refreshQueue["scheduled"].(int64) < 5 {
		t.Fatalf("refresh_queue.scheduled = %v, want >= 5", refreshQueue["scheduled"])
	}
	lockWait, ok := snapshot["scheduler_lock_wait_ns"].(map[string]any)
	if !ok {
		t.Fatalf("scheduler_lock_wait_ns type = %T, want map[string]any", snapshot["scheduler_lock_wait_ns"])
	}
	if _, ok := lockWait["pick_single"]; !ok {
		t.Fatalf("scheduler lock wait missing pick_single")
	}
}
