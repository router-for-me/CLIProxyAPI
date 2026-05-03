package logging

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
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

// mutableTextError carries a text pointer so a test can mutate the
// rendered Error() return value after handing the wrapped *ErrorMessage
// into LogRequest. Used to exercise the BE-R2-1 deep-clone guard.
type mutableTextError struct {
	text *string
}

func (e *mutableTextError) Error() string {
	if e == nil || e.text == nil {
		return ""
	}
	return *e.text
}

// TestAsyncEmitter_CallerMutateAfterEnqueue_DoesNotCorruptLog pins the
// BE-1 ownership-contract invariant from the Codex Stage 1 exit
// review: when LogRequest hands a []byte/map/error/Header to the async
// path, a caller that mutates them after LogRequest returns must NOT
// corrupt the eventual log write. The dispatcher clones into the queue
// defensively so the worker writes the call-time snapshot. The
// ErrorMessage clone (BE-R2-1) freezes the inner Error to its current
// text via errors.New and deep-clones the Addon header.
func TestAsyncEmitter_CallerMutateAfterEnqueue_DoesNotCorruptLog(t *testing.T) {
	l := newTestLoggerForAsync(t)
	now := time.Now()

	body := []byte(`{"original":1}`)
	resp := []byte(`{"original-response":2}`)
	headers := map[string][]string{"X-Trace": {"t-original"}}
	errText := "original-error-text"
	errAddon := http.Header{"X-Addon": {"a-original"}}
	errs := []*interfaces.ErrorMessage{
		{
			StatusCode: 502,
			Error:      &mutableTextError{text: &errText},
			Addon:      errAddon,
		},
	}

	if err := l.LogRequestWithOptions(
		"/v1/messages", "POST", headers, body, 502, headers,
		resp, nil, nil, nil, nil, errs, true, "req-1", now, now,
	); err != nil {
		t.Fatalf("LogRequest: %v", err)
	}

	// Mutate caller-owned containers AFTER LogRequest returned.
	body[0] = 'X'
	resp[len(resp)-1] = 'X'
	headers["X-Trace"][0] = "t-mutated"
	headers["X-Injected"] = []string{"y"}
	errText = "mutated-error-text"
	errAddon["X-Addon"][0] = "a-mutated"
	errAddon["X-Injected-Addon"] = []string{"y"}

	l.Flush()

	files, err := os.ReadDir(l.logsDir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("expected one log file after flush")
	}
	var contents string
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".log") {
			continue
		}
		raw, errRead := os.ReadFile(filepath.Join(l.logsDir, f.Name()))
		if errRead != nil {
			t.Fatalf("read log: %v", errRead)
		}
		contents = string(raw)
		break
	}
	if contents == "" {
		t.Fatalf("no .log file found")
	}
	// The original (un-mutated) bytes should appear in the log.
	if !strings.Contains(contents, `{"original":1}`) {
		t.Fatalf("expected original body in log, got:\n%s", contents)
	}
	if !strings.Contains(contents, `{"original-response":2}`) {
		t.Fatalf("expected original response in log, got:\n%s", contents)
	}
	if !strings.Contains(contents, "t-original") {
		t.Fatalf("expected original X-Trace header in log, got:\n%s", contents)
	}
	if strings.Contains(contents, "t-mutated") || strings.Contains(contents, "X-Injected") ||
		strings.Contains(contents, "X-Injected-Addon") {
		t.Fatalf("post-enqueue mutation leaked into log:\n%s", contents)
	}
	if !strings.Contains(contents, "original-error-text") {
		t.Fatalf("expected original error text in log, got:\n%s", contents)
	}
	if strings.Contains(contents, "mutated-error-text") {
		t.Fatalf("post-enqueue inner-error mutation leaked into log:\n%s", contents)
	}
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

// TestAsyncEmitter_ForcedAfterCloseWritesSynchronously pins the post-close
// forced-log invariant fixed under Codex Phase C BLOCKER #3. After close()
// returns, any further forced LogRequestWithOptions call must still hit
// disk via the sync fallback path — never enqueue into a buffered priority
// channel that has no consumer left to drain it.
func TestAsyncEmitter_ForcedAfterCloseWritesSynchronously(t *testing.T) {
	dir := t.TempDir()
	l := NewFileRequestLogger(true, dir, "", 0)

	// Close drains the worker. Any forced enqueue after this point must
	// fall back to sync writes.
	l.Close()

	now := time.Now()
	headers := map[string][]string{"Content-Type": {"application/json"}}
	if err := l.LogRequestWithOptions(
		"/v1/forced-after-close", "POST", headers, nil, 500, headers,
		[]byte("payload"), nil, nil, nil, nil, nil,
		true, "after-close-id", now, now,
	); err != nil {
		t.Fatalf("forced log after close returned err: %v", err)
	}
	if l.DroppedLogs() != 0 {
		t.Fatalf("forced logs must never drop; counter=%d", l.DroppedLogs())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "after-close-id") {
			return
		}
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	t.Fatalf("forced log not written to disk after close; files=%v", names)
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
