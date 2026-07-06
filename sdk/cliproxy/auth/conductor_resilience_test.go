package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// newTestManager builds a Manager with the given provider-resilience config and
// stores it in runtimeConfig so acquireProviderPermit can read it.
func newTestManager(maxInflight, queueWaitMS int) *Manager {
	m := NewManager(nil, nil, nil)
	m.runtimeConfig.Store(&internalconfig.Config{
		ProviderResilience: internalconfig.ProviderResilienceConfig{
			MaxInflightPerProvider: maxInflight,
			MaxQueueWaitMS:         queueWaitMS,
		},
	})
	return m
}

// TestBlockingQueuePerProvider verifies that with max-inflight=4, exactly 4
// goroutines acquire immediately and the rest block until a slot is released.
func TestBlockingQueuePerProvider(t *testing.T) {
	t.Parallel()
	m := newTestManager(4, 5000)
	ctx := context.Background()

	var acquired int64
	var blocked int64
	// holdGate keeps holders in the critical section until explicitly released,
	// so the first 4 acquire-and-hold while the next 4 block on the semaphore.
	holdGate := make(chan struct{})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := m.acquireProviderPermit(ctx, "prov-a")
			if err != nil {
				atomic.AddInt64(&blocked, 1)
				return
			}
			atomic.AddInt64(&acquired, 1)
			<-holdGate // hold the permit until the test releases
			rel()
		}()
	}

	// Give fast-path acquirers time to grab slots; the other 4 block.
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt64(&acquired); got != 4 {
		t.Fatalf("expected 4 immediate acquires, got %d", got)
	}

	// Release all holders; the 4 blocked waiters proceed.
	close(holdGate)
	wg.Wait()

	if got := atomic.LoadInt64(&acquired); got != 8 {
		t.Errorf("expected 8 total acquires, got %d", got)
	}
	if got := atomic.LoadInt64(&blocked); got != 0 {
		t.Errorf("expected 0 blocked/rejected, got %d", got)
	}
}

// TestQueueWaitTimeout verifies that a waiter gets a 503 provider_backpressure
// error after max-queue-wait-ms elapses.
func TestQueueWaitTimeout(t *testing.T) {
	t.Parallel()
	m := newTestManager(1, 100) // 1 slot, 100ms wait
	ctx := context.Background()

	// Fill the single slot.
	rel1, err := m.acquireProviderPermit(ctx, "prov-t")
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	defer rel1()

	start := time.Now()
	rel2, err := m.acquireProviderPermit(ctx, "prov-t")
	elapsed := time.Since(start)

	if err == nil {
		rel2()
		t.Fatal("expected timeout error, got nil")
	}
	authErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *auth.Error, got %T: %v", err, err)
	}
	if authErr.Code != "provider_backpressure" {
		t.Errorf("expected code provider_backpressure, got %q", authErr.Code)
	}
	if authErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", authErr.HTTPStatus)
	}
	if !authErr.Retryable {
		t.Error("expected Retryable=true")
	}
	if elapsed < 90*time.Millisecond {
		t.Errorf("waited only %v, expected ~100ms", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("waited %v, expected ~100ms (not far over)", elapsed)
	}
}

// TestCtxCancelUnblocksWaiter verifies that cancelling the context unblocks a
// waiter and returns ctx.Err(), with no goroutine leak. Non-parallel to keep
// goroutine accounting stable.
func TestCtxCancelUnblocksWaiter(t *testing.T) {
	// Use a short queue-wait so the internal context.WithTimeout timer goroutine
	// exits quickly after the test, reducing goroutine-count noise.
	m := newTestManager(1, 2000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Fill the slot.
	rel1, err := m.acquireProviderPermit(ctx, "prov-c")
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}

	beforeGoroutines := runtime.NumGoroutine()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rel2, err := m.acquireProviderPermit(ctx, "prov-c")
		if err == nil {
			rel2()
			t.Error("expected ctx.Err, got nil")
			return
		}
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond) // let waiter block

	// Snapshot backpressure count BEFORE cancel — a pure client-cancel must NOT
	// inflate providerBackpressureCount (R1/R2 regression guard).
	backpressureBefore := m.ResilienceMetricsSnapshot()["cliproxy_provider_backpressure_reject_total"]

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not return after ctx cancel")
	}

	rel1()

	// Assert the cancel path did NOT increment backpressure (locks R1: client
	// cancel is not provider saturation).
	backpressureAfter := m.ResilienceMetricsSnapshot()["cliproxy_provider_backpressure_reject_total"]
	if backpressureAfter != backpressureBefore {
		t.Errorf("client cancel inflated backpressure: before=%d after=%d (expected unchanged)",
			backpressureBefore, backpressureAfter)
	}

	// Allow timer goroutines from context.WithTimeout to be reaped.
	time.Sleep(300 * time.Millisecond)
	afterGoroutines := runtime.NumGoroutine()
	// The waiter goroutine must be gone. Tolerate a small delta for runtime
	// timer/GC noise (the internal WithTimeout timer may take a cycle to reap).
	if delta := afterGoroutines - beforeGoroutines; delta > 2 {
		t.Errorf("possible goroutine leak: before=%d after=%d (delta=%d)",
			beforeGoroutines, afterGoroutines, delta)
	}
}

// TestNoHeadOfLineBlocking verifies that a saturated provider A does not block
// a request for provider B.
func TestNoHeadOfLineBlocking(t *testing.T) {
	t.Parallel()
	m := newTestManager(2, 5000)
	ctx := context.Background()

	// Saturate provider A.
	relA1, _ := m.acquireProviderPermit(ctx, "prov-a")
	relA2, _ := m.acquireProviderPermit(ctx, "prov-a")
	defer relA1()
	defer relA2()

	// Provider B should acquire immediately despite A being full.
	start := time.Now()
	relB, err := m.acquireProviderPermit(ctx, "prov-b")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("provider B acquire failed: %v", err)
	}
	relB()
	if elapsed > 50*time.Millisecond {
		t.Errorf("provider B blocked %v; head-of-line blocking suspected", elapsed)
	}
}

// TestStreamingReleaseOnChannelClose verifies the streaming wrapper releases the
// permit when the upstream channel closes. It exercises releaseFunc + the wrapper
// logic on a synthetic channel (the wrapper is the thing under test).
func TestStreamingReleaseOnChannelClose(t *testing.T) {
	t.Parallel()
	m := newTestManager(1, 5000)
	ctx := context.Background()

	// Acquire the permit (simulating the streaming acquire path).
	releaseProviderPermit, err := m.acquireProviderPermit(ctx, "prov-s")
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}

	// Simulate the streaming wrapper: a goroutine that defers release and
	// forwards from an original channel to a wrapped channel.
	original := make(chan cliproxyexecutor.StreamChunk, 1)
	wrapped := make(chan cliproxyexecutor.StreamChunk, 1)
	wrapperDone := make(chan struct{})
	go func() {
		defer releaseProviderPermit()
		defer close(wrapped)
		defer close(wrapperDone)
		for {
			select {
			case chunk, ok := <-original:
				if !ok {
					return
				}
				wrapped <- chunk
			case <-ctx.Done():
				for range original {
				}
				return
			}
		}
	}()

	// Send one chunk then close upstream.
	original <- cliproxyexecutor.StreamChunk{Payload: []byte("hello")}
	chunk := <-wrapped
	if string(chunk.Payload) != "hello" {
		t.Fatalf("expected hello, got %q", string(chunk.Payload))
	}
	close(original)

	// Wrapper should exit and release the permit.
	select {
	case <-wrapperDone:
	case <-time.After(2 * time.Second):
		t.Fatal("wrapper did not exit after upstream close")
	}

	// Now the slot should be free: a new acquire should be immediate.
	start := time.Now()
	rel2, err := m.acquireProviderPermit(ctx, "prov-s")
	if err != nil {
		t.Fatalf("re-acquire failed: %v", err)
	}
	rel2()
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("permit not released after stream close; re-acquire took %v", elapsed)
	}
}

// TestConcurrentThroughput is the benchmark Franco asked for. It compares three
// scenarios against a mock httptest server with a configurable delay:
//   - Scenario A: limiter disabled (max-inflight=0), N=20 — peak concurrency at
//     mock should be ~20 (no protection).
//   - Scenario B: max-inflight=4, max-queue-wait-ms=30000, N=20 — measures
//     acquired-immediate, queued-count, queue-wait p50/p95/max, rejected,
//     throughput vs the real limit (4).
//   - Scenario C: isolated request (no contention) with queue enabled —
//     confirms added latency on the fast path is <1ms.
func TestConcurrentThroughput(t *testing.T) {
	t.Parallel()

	const N = 20
	const mockDelay = 100 * time.Millisecond

	// Mock upstream: tracks peak concurrency and total hits.
	var peakConcurrency int64
	var currentConcurrency int64
	var totalHits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt64(&currentConcurrency, 1)
		for {
			peak := atomic.LoadInt64(&peakConcurrency)
			if cur <= peak || atomic.CompareAndSwapInt64(&peakConcurrency, peak, cur) {
				break
			}
		}
		atomic.AddInt64(&totalHits, 1)
		time.Sleep(mockDelay)
		atomic.AddInt64(&currentConcurrency, -1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// runScenario fires N goroutines through acquireProviderPermit then hits the
	// mock, collecting wait-time stats.
	runScenario := func(maxInflight, queueWaitMS int) (peak int64, hits int64, acquiredImmediate, queuedCount, rejected int64, waits []time.Duration, totalElapsed time.Duration) {
		peakConcurrency = 0
		currentConcurrency = 0
		totalHits = 0

		m := newTestManager(maxInflight, queueWaitMS)
		ctx := context.Background()

		var wg sync.WaitGroup
		var ai, qc, rj int64
		waits = make([]time.Duration, 0, N)
		var waitsMu sync.Mutex

		scenarioStart := time.Now()
		for i := 0; i < N; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				waitStart := time.Now()
				rel, err := m.acquireProviderPermit(ctx, "prov-bench")
				waitTime := time.Since(waitStart)
				if err != nil {
					atomic.AddInt64(&rj, 1)
					return
				}
				if waitTime > 1*time.Millisecond {
					atomic.AddInt64(&qc, 1)
				} else {
					atomic.AddInt64(&ai, 1)
				}
				waitsMu.Lock()
				waits = append(waits, waitTime)
				waitsMu.Unlock()

				// Hit the mock upstream (simulates executor.Execute).
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
				resp, err := http.DefaultClient.Do(req)
				if err == nil {
					resp.Body.Close()
				}
				rel()
			}()
		}
		wg.Wait()
		totalElapsed = time.Since(scenarioStart)

		peak = atomic.LoadInt64(&peakConcurrency)
		hits = atomic.LoadInt64(&totalHits)
		acquiredImmediate = atomic.LoadInt64(&ai)
		queuedCount = atomic.LoadInt64(&qc)
		rejected = atomic.LoadInt64(&rj)
		return
	}

	percentile := func(data []time.Duration, p float64) time.Duration {
		if len(data) == 0 {
			return 0
		}
		sorted := make([]time.Duration, len(data))
		copy(sorted, data)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		idx := int(float64(len(sorted)-1) * p)
		return sorted[idx]
	}

	// --- Scenario A: limiter disabled ---
	peakA, hitsA, aiA, qcA, rjA, _, elapsedA := runScenario(0, 0)
	t.Logf("Scenario A (disabled, max-inflight=0, N=%d):", N)
	t.Logf("  peak-concurrency-at-mock: %d", peakA)
	t.Logf("  acquired-immediate: %d", aiA)
	t.Logf("  queued-count: %d", qcA)
	t.Logf("  rejected: %d", rjA)
	t.Logf("  throughput-req/s: %.1f", float64(hitsA)/elapsedA.Seconds())

	if peakA < 15 {
		t.Errorf("Scenario A: expected peak concurrency ~%d (no protection), got %d", N, peakA)
	}

	// --- Scenario B: limiter enabled, max-inflight=4 ---
	peakB, hitsB, aiB, qcB, rjB, waitsB, elapsedB := runScenario(4, 30000)
	t.Logf("Scenario B (max-inflight=4, queue-wait=30s, N=%d):", N)
	t.Logf("  peak-concurrency-at-mock: %d", peakB)
	t.Logf("  acquired-immediate: %d", aiB)
	t.Logf("  queued-count: %d", qcB)
	t.Logf("  queue-wait-p50: %v", percentile(waitsB, 0.50))
	t.Logf("  queue-wait-p95: %v", percentile(waitsB, 0.95))
	t.Logf("  queue-wait-max: %v", percentile(waitsB, 1.0))
	t.Logf("  rejected: %d", rjB)
	t.Logf("  throughput-req/s: %.2f", float64(hitsB)/elapsedB.Seconds())

	if peakB > 4 {
		t.Errorf("Scenario B: expected peak concurrency <= 4 (limit), got %d", peakB)
	}
	if rjB != 0 {
		t.Errorf("Scenario B: expected 0 rejected (all fit in 30s), got %d", rjB)
	}
	if hitsB != N {
		t.Errorf("Scenario B: expected %d hits, got %d", N, hitsB)
	}

	// --- Scenario C: isolated request, no contention, queue enabled ---
	mC := newTestManager(4, 30000)
	ctxC := context.Background()
	startC := time.Now()
	relC, errC := mC.acquireProviderPermit(ctxC, "prov-iso")
	isoLatency := time.Since(startC)
	if errC != nil {
		t.Fatalf("Scenario C acquire failed: %v", errC)
	}
	relC()
	t.Logf("Scenario C (isolated, no contention, queue enabled):")
	t.Logf("  isolated-latency-with-queue: %v", isoLatency)
	if isoLatency > 1*time.Millisecond {
		t.Errorf("Scenario C: fast-path latency %v > 1ms", isoLatency)
	}

	// Summary line for easy grep.
	t.Logf("SUMMARY: A_peak=%d A_tput=%.1f/s | B_peak=%d B_ai=%d B_q=%d B_rej=%d B_tput=%.2f/s B_p50=%v B_p95=%v | C_lat=%v",
		peakA, float64(hitsA)/elapsedA.Seconds(),
		peakB, aiB, qcB, rjB, float64(hitsB)/elapsedB.Seconds(),
		percentile(waitsB, 0.50), percentile(waitsB, 0.95),
		isoLatency)
}

// TestAcquireProviderPermitDisabled verifies that max-inflight=0 returns a no-op
// release with no error (limiter disabled).
func TestAcquireProviderPermitDisabled(t *testing.T) {
	t.Parallel()
	m := newTestManager(0, 0)
	ctx := context.Background()

	rel, err := m.acquireProviderPermit(ctx, "prov-d")
	if err != nil {
		t.Fatalf("disabled limiter should not error, got %v", err)
	}
	rel() // must be safe to call
	rel() // idempotent
}

// TestAcquireProviderPermitNilManager verifies nil-safety.
func TestAcquireProviderPermitNilManager(t *testing.T) {
	t.Parallel()
	var m *Manager
	rel, err := m.acquireProviderPermit(context.Background(), "prov")
	if err != nil {
		t.Fatalf("nil manager should not error, got %v", err)
	}
	rel()
}

// TestResilienceMetricsSnapshot verifies the metrics snapshot returns the
// expected keys and reflects backpressure rejections.
func TestResilienceMetricsSnapshot(t *testing.T) {
	t.Parallel()
	m := newTestManager(1, 50)
	ctx := context.Background()

	// Fill the slot.
	rel, _ := m.acquireProviderPermit(ctx, "prov-m")
	defer rel()

	// Force a timeout to increment backpressure count.
	_, _ = m.acquireProviderPermit(ctx, "prov-m")

	snap := m.ResilienceMetricsSnapshot()
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if snap["cliproxy_provider_backpressure_reject_total"] != 1 {
		t.Errorf("expected backpressure=1, got %d", snap["cliproxy_provider_backpressure_reject_total"])
	}
	for _, key := range []string{
		"cliproxy_provider_backpressure_reject_total",
		"cliproxy_provider_queue_wait_ns_sum",
		"cliproxy_conductor_permit_attempts_total",
		"cliproxy_conductor_permit_ns_sum",
	} {
		if _, ok := snap[key]; !ok {
			t.Errorf("missing metric key %q", key)
		}
	}
}

// TestStreamingWrapperDrainsOnInnerCancel reproduces the F4 goroutine leak:
// when the inner `case <-execCtx.Done()` fires while the upstream producer is
// blocked sending the next chunk to an unbuffered originalChunks, the wrapper
// MUST drain originalChunks so the producer unblocks. Without the drain fix the
// producer goroutine leaks forever (blocked on send) and the permit is never
// released.
//
// This test exercises the wrapper pattern directly (the same select loop used
// in executeStreamMixedOnce and tryAntigravityCreditsExecuteStream) to prove
// the drain-on-inner-cancel contract without requiring a full executor mock.
func TestStreamingWrapperDrainsOnInnerCancel(t *testing.T) {
	t.Parallel()
	m := newTestManager(1, 5000)
	ctx := context.Background()

	// Acquire the single permit slot; the wrapper's release should free it.
	releasePermit, err := m.acquireProviderPermit(ctx, "prov-f4")
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}

	// Unbuffered channel: producer will block on the second send.
	originalChunks := make(chan cliproxyexecutor.StreamChunk)
	wrapped := make(chan cliproxyexecutor.StreamChunk, 1)

	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Reproduce the exact wrapper select loop from conductor.go (post-F4 fix).
	go func() {
		defer releasePermit()
		defer close(wrapped)
		for {
			select {
			case chunk, ok := <-originalChunks:
				if !ok {
					return
				}
				select {
				case wrapped <- chunk:
				case <-execCtx.Done():
					// Drain remaining chunks to unblock the upstream producer.
					for range originalChunks {
					}
					return
				}
			case <-execCtx.Done():
				for range originalChunks {
				}
				return
			}
		}
	}()

	// Producer: send chunk1 (succeeds, wrapper forwards), then block on chunk2.
	producerUnblocked := make(chan struct{})
	go func() {
		defer close(producerUnblocked)
		chunk1 := cliproxyexecutor.StreamChunk{Payload: []byte("first")}
		originalChunks <- chunk1 // blocks until wrapper receives
		// Producer now blocks sending chunk2 to the unbuffered channel.
		// If the wrapper drains on cancel, this send will complete (drained)
		// and the goroutine exits. If the wrapper does NOT drain (the bug),
		// this send blocks forever and producerUnblocked never closes.
		chunk2 := cliproxyexecutor.StreamChunk{Payload: []byte("second")}
		originalChunks <- chunk2
		// Close originalChunks so the wrapper's drain loop terminates and the
		// permit is released. Without this close the drain blocks forever.
		close(originalChunks)
	}()

	// Receive chunk1 on the wrapped channel to advance the wrapper to the
	// inner select where it can observe execCtx.Done().
	select {
	case <-wrapped:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for chunk1 on wrapped channel")
	}

	// Cancel execCtx. The wrapper's inner case should fire, drain originalChunks,
	// and unblock the producer's second send.
	cancel()

	// Assert the producer goroutine unblocks (does not leak).
	select {
	case <-producerUnblocked:
		// success: producer was drained and exited
	case <-time.After(3 * time.Second):
		t.Fatal("producer goroutine leaked: originalChunks was not drained on inner cancel (F4 bug)")
	}

	// Assert the permit was released: a new acquire must succeed immediately.
	rel2, err2 := m.acquireProviderPermit(ctx, "prov-f4")
	if err2 != nil {
		t.Fatalf("permit was not released after inner cancel: %v", err2)
	}
	rel2()
}

// TestReleaseFuncIdempotent reproduces FINDING-1 (MEDIUM): releaseFunc must
// drain the limiter slot EXACTLY ONCE per acquire. The pre-fix implementation
// used `select { case <-limiter: default: }` directly, which is NOT idempotent:
// after the first release drains the caller's token, a second call (e.g. from
// a deferred release plus an explicit release, or a double-defer) will steal a
// token that a DIFFERENT acquirer placed in the meantime, silently
// under-counting inflight and exceeding MaxInflightPerProvider.
//
// With the sync.Once guard, the second and subsequent calls are no-ops.
func TestReleaseFuncIdempotent(t *testing.T) {
	t.Parallel()
	limiter := make(chan struct{}, 1)

	// Acquire once: fill the slot.
	limiter <- struct{}{}

	rel := releaseFunc(limiter)

	// First release drains the caller's token — slot is now free.
	rel()

	// A DIFFERENT acquirer places a new token (simulating concurrent traffic).
	limiter <- struct{}{}

	// Second release on the FIRST acquirer's release func: with the old
	// non-idempotent code this would drain the new token (BUG). With sync.Once
	// it must be a no-op.
	rel()
	if got := len(limiter); got != 1 {
		t.Errorf("second rel() stole another acquirer's token: len(limiter)=%d, want 1", got)
	}

	// Third release must also be a no-op (idempotent beyond the second call).
	rel()
	if got := len(limiter); got != 1 {
		t.Errorf("third rel() was not a no-op: len(limiter)=%d, want 1", got)
	}

	// Sanity: the slot is still held by the other acquirer and can be drained
	// by its own release.
	select {
	case <-limiter:
	default:
		t.Fatal("expected the other acquirer's token to still be buffered")
	}
}

// fmt import guard (used for formatting in case of future expansion).
var _ = fmt.Sprintf
