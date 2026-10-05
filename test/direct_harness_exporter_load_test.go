package test

import (
	"context"
	"net/http"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"
)

type burstResult struct {
	elapsed time.Duration
	p50     time.Duration
	p99     time.Duration
	failed  int
}

// burst sends total chat requests with the given concurrency and records per-request latency.
func burst(t *testing.T, g *gateway, c contract, total, concurrency int) burstResult {
	t.Helper()
	latencies := make([]time.Duration, total)
	var failed int
	var mu sync.Mutex
	jobs := make(chan int)
	var wg sync.WaitGroup

	start := time.Now()
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				began := time.Now()
				got := post(t, context.Background(), g.URL+c.path, c.request(false), nil)
				latencies[i] = time.Since(began)
				if got.Status != http.StatusOK {
					mu.Lock()
					failed++
					mu.Unlock()
				}
			}
		}()
	}
	for i := range total {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	return burstResult{elapsed: time.Since(start), p50: latencies[total/2], p99: latencies[total*99/100], failed: failed}
}

func heapInUse() uint64 {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapInuse
}

// TestSlowOrFailedExporterDoesNotDegradeInference runs two request bursts per mode. The second
// starts after the exporter has engaged its receiver, so it measures inference while the
// exporter is blocked on a stalled receiver, backing off from an offline one, or retrying 5xx.
func TestSlowOrFailedExporterDoesNotDegradeInference(t *testing.T) {
	const total, concurrency = 300, 8
	chat := contracts[0]
	results := map[accountingMode]burstResult{}

	for _, mode := range allAccountingModes {
		t.Run(string(mode), func(t *testing.T) {
			upstream := newFakeUpstream(t, nil)
			g := newGateway(t, gatewayOptions{
				mode: mode, upstreamURL: upstream.server.URL, accounts: contractAccounts("load-"+string(mode), chat),
				exportClients: true,
			})
			if mode != accountingDisabled {
				recordAccounting(t)
			}

			goroutinesBefore, heapBefore := runtime.NumGoroutine(), heapInUse()
			first := burst(t, g, chat, total, concurrency)
			if first.failed != 0 {
				t.Fatalf("%d of %d requests failed in the first burst", first.failed, total)
			}

			waitForExporter(t, g, mode)
			second := burst(t, g, chat, total, concurrency)
			results[mode] = second
			if second.failed != 0 {
				t.Errorf("%d of %d requests failed while the exporter was %s", second.failed, total, mode)
			}

			goroutinesAfter, heapAfter := runtime.NumGoroutine(), heapInUse()
			t.Logf("mode=%s burst2: elapsed=%s p50=%s p99=%s goroutines %d->%d heap %dKiB->%dKiB",
				mode, second.elapsed.Round(time.Millisecond), second.p50, second.p99,
				goroutinesBefore, goroutinesAfter, heapBefore/1024, heapAfter/1024)

			if mode == accountingDisabled {
				return
			}
			assertAccountingHealthy(t, g, mode, 2*total)
			if growth := goroutinesAfter - goroutinesBefore; growth > 40 {
				t.Errorf("goroutines grew by %d under %s, want bounded growth", growth, mode)
			}
		})
	}

	baseline := results[accountingDisabled]
	for _, mode := range allAccountingModes[1:] {
		got := results[mode]
		// Generous bounds: this guards against blocking, not against scheduler noise.
		if got.elapsed > 5*baseline.elapsed+time.Second || got.p99 > baseline.p99*10+500*time.Millisecond {
			t.Errorf("%s degraded inference: elapsed %s p99 %s vs baseline %s %s", mode, got.elapsed, got.p99, baseline.elapsed, baseline.p99)
		}
	}
}

// waitForExporter returns once the exporter has made its first receiver contact for the mode.
func waitForExporter(t *testing.T, g *gateway, mode accountingMode) {
	t.Helper()
	if mode == accountingDisabled || mode == accountingEnabled {
		return
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		switch mode {
		case accountingLiteLLMStalled, accountingLiteLLMFailing:
			if g.receiver.logsHits.Load() >= 1 {
				return
			}
		case accountingLiteLLMOffline:
			if status := g.exporter.Status(); status != "ready" && status != "disabled" {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("exporter never engaged the %s receiver, status=%s", mode, g.exporter.Status())
}

// assertAccountingHealthy checks the accounting counters after the traffic completed.
func assertAccountingHealthy(t *testing.T, g *gateway, mode accountingMode, accepted int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		stats, err := g.outbox.Stats()
		if err != nil {
			t.Fatal(err)
		}
		if stats.Backlog >= accepted || time.Now().After(deadline) {
			if stats.DroppedEvents != 0 || stats.PersistenceFailures != 0 {
				t.Errorf("%s lost events: %+v", mode, stats)
			}
			if stats.Backlog != accepted {
				t.Errorf("%s backlog = %d, want all %d events retained locally", mode, stats.Backlog, accepted)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	switch mode {
	case accountingLiteLLMStalled:
		if hits := g.receiver.logsHits.Load(); hits != 1 {
			t.Errorf("stalled receiver saw %d export requests, want exactly the one in flight", hits)
		}
	case accountingLiteLLMOffline, accountingLiteLLMFailing:
		if status := g.exporter.Status(); status == "exported" || status == "ready" {
			t.Errorf("%s exporter status = %q, want a visible degraded status", mode, status)
		}
	}
	if g.receiver != nil {
		if leaked := g.receiver.leakedSecrets(); len(leaked) > 0 {
			t.Errorf("exported records contain credential material: %v", leaked)
		}
	}
}
