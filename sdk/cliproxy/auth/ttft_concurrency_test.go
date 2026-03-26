package auth

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestManagerExecuteStreamTTFTHighConcurrency5000Auths(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping high-concurrency ttft coverage in short mode")
	}

	const (
		totalAuths    = 5000
		totalRequests = 4096
		concurrency   = 512
	)

	manager, model := setupTTFTConcurrencyManager(t, totalAuths, ttftBenchmarkStreamExecutor{id: "gemini"})
	runTTFTHighConcurrencyTest(t, manager, model, totalAuths, totalRequests, concurrency, cliproxyexecutor.Request{Model: model})
}

func TestManagerExecuteStreamTTFTHighConcurrency5000Auths_LongConversationPreviousResponseID(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping high-concurrency ttft coverage in short mode")
	}

	const (
		totalAuths    = 5000
		totalRequests = 4096
		concurrency   = 512
	)

	manager, model := setupTTFTConcurrencyManager(t, totalAuths, ttftBenchmarkStreamExecutor{
		id:              "gemini",
		expectedNeedle:  []byte(`"previous_response_id":"resp-long-context-concurrency"`),
		minPayloadBytes: 64 << 10,
	})
	runTTFTHighConcurrencyTest(t, manager, model, totalAuths, totalRequests, concurrency, newTTFTLongConversationRequest(model, "resp-long-context-concurrency"))
}

func runTTFTHighConcurrencyTest(t *testing.T, manager *Manager, model string, totalAuths int, totalRequests int, concurrency int, req cliproxyexecutor.Request) {
	t.Helper()

	ctx := context.Background()
	opts := cliproxyexecutor.Options{Stream: true}

	warmResult, errWarm := manager.ExecuteStream(ctx, []string{"gemini"}, req, opts)
	if errWarm != nil {
		t.Fatalf("warmup ExecuteStream error = %v", errWarm)
	}
	for range warmResult.Chunks {
	}

	jobs := make(chan int, totalRequests)
	start := make(chan struct{})
	errCh := make(chan error, totalRequests)
	ttfts := make([]time.Duration, totalRequests)

	var wg sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for index := range jobs {
				began := time.Now()
				result, errStream := manager.ExecuteStream(ctx, []string{"gemini"}, req, opts)
				if errStream != nil {
					errCh <- fmt.Errorf("ExecuteStream error: %w", errStream)
					continue
				}

				seenDelta := false
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						errCh <- fmt.Errorf("stream chunk error: %w", chunk.Err)
						break
					}
					if bytes.Contains(chunk.Payload, []byte(`"response.output_text.delta"`)) {
						elapsed := time.Since(began)
						if elapsed <= 0 {
							elapsed = time.Nanosecond
						}
						ttfts[index] = elapsed
						seenDelta = true
						break
					}
				}
				if !seenDelta {
					errCh <- fmt.Errorf("stream returned no delta chunk")
				}
			}
		}()
	}

	for i := 0; i < totalRequests; i++ {
		jobs <- i
	}
	close(jobs)

	began := time.Now()
	close(start)
	wg.Wait()
	close(errCh)
	elapsed := time.Since(began)

	for err := range errCh {
		t.Fatal(err)
	}

	p50, p95, p99, avg := summarizeTTFTDurations(t, ttfts)
	rps := float64(totalRequests) / elapsed.Seconds()

	t.Logf(
		"high-concurrency ttft: auths=%d requests=%d concurrency=%d elapsed=%s rps=%.0f avg=%s p50=%s p95=%s p99=%s",
		totalAuths,
		totalRequests,
		concurrency,
		elapsed,
		rps,
		avg,
		p50,
		p95,
		p99,
	)
}

func setupTTFTConcurrencyManager(t *testing.T, totalAuths int, executor ProviderExecutor) (*Manager, string) {
	t.Helper()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	model := "ttft-bench-model"
	for index := 0; index < totalAuths; index++ {
		authID := fmt.Sprintf("ttft-gemini-%04d", index)
		auth := &Auth{ID: authID, Provider: "gemini"}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", authID, errRegister)
		}
		reg.RegisterClient(authID, "gemini", []*registry.ModelInfo{{ID: model}})
	}
	manager.syncScheduler()

	t.Cleanup(func() {
		for index := 0; index < totalAuths; index++ {
			reg.UnregisterClient(fmt.Sprintf("ttft-gemini-%04d", index))
		}
	})

	return manager, model
}

func summarizeTTFTDurations(t *testing.T, values []time.Duration) (time.Duration, time.Duration, time.Duration, time.Duration) {
	t.Helper()
	if len(values) == 0 {
		t.Fatal("no ttft samples recorded")
	}

	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var total time.Duration
	for _, value := range sorted {
		if value <= 0 {
			t.Fatalf("invalid ttft sample: %s", value)
		}
		total += value
	}

	avg := total / time.Duration(len(sorted))
	return percentileDurationAt(sorted, 0.50), percentileDurationAt(sorted, 0.95), percentileDurationAt(sorted, 0.99), avg
}

func percentileDurationAt(sorted []time.Duration, fraction float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)-1) * fraction)
	return sorted[index]
}
