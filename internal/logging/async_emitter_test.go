package logging

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestLoggerForAsync(t *testing.T) *FileRequestLogger {
	t.Helper()
	dir := t.TempDir()
	l := NewFileRequestLogger(true, dir, "", 0)
	t.Cleanup(l.Close)
	return l
}

func countLogFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			n++
		}
	}
	return n
}

func TestAsyncEmitter_NormalPath_WritesFile(t *testing.T) {
	l := newTestLoggerForAsync(t)
	now := time.Now()
	headers := map[string][]string{"Content-Type": {"application/json"}}
	if err := l.LogRequest("/v1/messages", "POST", headers, []byte(`{"hello":1}`), 200, headers, []byte(`{"ok":true}`), nil, nil, nil, nil, nil, "", now, now); err != nil {
		t.Fatalf("LogRequest: %v", err)
	}
	l.Flush()
	if got := countLogFiles(t, l.logsDir); got != 1 {
		t.Fatalf("expected 1 log file after flush, got %d", got)
	}
}

func TestAsyncEmitter_ForcedPath_WritesEvenWhenDisabled(t *testing.T) {
	l := newTestLoggerForAsync(t)
	l.SetEnabled(false)

	now := time.Now()
	headers := map[string][]string{"Content-Type": {"application/json"}}

	// Normal call when disabled — should NOT write.
	_ = l.LogRequest("/v1/messages", "POST", headers, []byte(`{}`), 200, headers, []byte(`{}`), nil, nil, nil, nil, nil, "n", now, now)
	// Forced call when disabled — MUST write.
	if err := l.LogRequestWithOptions("/v1/messages", "POST", headers, []byte(`{}`), 500, headers, []byte(`{}`), nil, nil, nil, nil, nil, true, "f", now, now); err != nil {
		t.Fatalf("forced LogRequest: %v", err)
	}
	l.Flush()

	files, err := os.ReadDir(l.logsDir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	hasError := false
	hasNonError := false
	for _, e := range files {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		if strings.HasPrefix(e.Name(), "error-") {
			hasError = true
		} else {
			hasNonError = true
		}
	}
	if !hasError {
		t.Fatalf("forced log was not written; files=%v", files)
	}
	if hasNonError {
		t.Fatalf("normal log was written despite enabled=false")
	}
}

func TestAsyncEmitter_NormalQueueOverflow_IncrementsDropped(t *testing.T) {
	// Build a logger and pause its worker so we can fill the queue without
	// racing the drain.
	dir := t.TempDir()
	l := &FileRequestLogger{logsDir: dir}
	l.enabled.Store(true)
	emitter := newAsyncEmitter(l)
	l.async = emitter
	// Intentionally do NOT start the worker, so enqueues accumulate until
	// the channel buffer fills.

	now := time.Now()
	headers := map[string][]string{"Content-Type": {"application/json"}}

	// Fill the normal queue to exactly its capacity.
	for i := 0; i < asyncNormalQueueDepth; i++ {
		if err := l.LogRequest("/v1/messages", "POST", headers, nil, 200, headers, nil, nil, nil, nil, nil, nil, "", now, now); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	if l.DroppedLogs() != 0 {
		t.Fatalf("expected 0 drops at exact capacity, got %d", l.DroppedLogs())
	}

	// One more enqueue — must drop, and increment the counter.
	if err := l.LogRequest("/v1/messages", "POST", headers, nil, 200, headers, nil, nil, nil, nil, nil, nil, "", now, now); err != nil {
		t.Fatalf("over-capacity enqueue returned err: %v", err)
	}
	if l.DroppedLogs() != 1 {
		t.Fatalf("expected drop counter = 1, got %d", l.DroppedLogs())
	}

	// Now start the worker and Close — pending tasks drain.
	emitter.start()
	emitter.close()
}

func TestAsyncEmitter_ForcedFallsBackToSyncWhenPriorityFull(t *testing.T) {
	dir := t.TempDir()
	l := &FileRequestLogger{logsDir: dir}
	l.enabled.Store(true)
	emitter := newAsyncEmitter(l)
	l.async = emitter
	// Worker not started, so priority channel will fill.

	now := time.Now()
	headers := map[string][]string{"Content-Type": {"application/json"}}

	for i := 0; i < asyncPriorityQueueDepth; i++ {
		if err := l.LogRequestWithOptions("/v1/forced", "POST", headers, nil, 500, headers, nil, nil, nil, nil, nil, nil, true, "", now, now); err != nil {
			t.Fatalf("priority enqueue: %v", err)
		}
	}

	// One more forced — priority queue is full, must fall back to sync write.
	if err := l.LogRequestWithOptions("/v1/forced-overflow", "POST", headers, nil, 500, headers, []byte("payload"), nil, nil, nil, nil, nil, true, "overflow-id", now, now); err != nil {
		t.Fatalf("forced overflow sync write failed: %v", err)
	}
	// Drop counter must be 0 — forced logs never drop.
	if l.DroppedLogs() != 0 {
		t.Fatalf("forced logs must not drop; counter=%d", l.DroppedLogs())
	}
	// File for the overflow forced log should exist (sync path wrote it).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	found := false
	for _, e := range entries {
		if strings.Contains(e.Name(), "overflow-id") {
			found = true
			break
		}
	}
	if !found {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("overflow forced log not on disk; files=%v", names)
	}

	emitter.start()
	emitter.close()
}

func TestAsyncEmitter_AtomicEnabledToggle_RaceFree(t *testing.T) {
	l := newTestLoggerForAsync(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Reader goroutines.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = l.IsEnabled()
				}
			}
		}()
	}
	// Writer goroutines toggling.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					l.SetEnabled(seed%2 == 0)
					seed++
				}
			}
		}(i)
	}
	time.Sleep(40 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestAsyncEmitter_CloseDrainsPendingTasks(t *testing.T) {
	dir := t.TempDir()
	l := &FileRequestLogger{logsDir: dir}
	l.enabled.Store(true)
	emitter := newAsyncEmitter(l)
	l.async = emitter
	// Worker not started yet — fill the queue.

	now := time.Now()
	headers := map[string][]string{"Content-Type": {"application/json"}}
	enqueued := 64
	for i := 0; i < enqueued; i++ {
		if err := l.LogRequest("/v1/drain", "POST", headers, nil, 200, headers, nil, nil, nil, nil, nil, nil, "", now, now); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	// Start the worker, then immediately close — drain semantics must
	// flush both queues before the worker exits.
	emitter.start()
	emitter.close()

	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	logs := 0
	for _, e := range files {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".log" {
			logs++
		}
	}
	if logs != enqueued {
		t.Fatalf("expected %d logs after close-drain, got %d", enqueued, logs)
	}
}
