package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorExtreme_2000Sessions_6KRPM_200MTPM(t *testing.T) {
	if os.Getenv("CLI_PROXY_EXTREME_STRESS") != "1" {
		t.Skip("set CLI_PROXY_EXTREME_STRESS=1 to run the 2000-session extreme stress test")
	}

	sessionCount := extremeStressEnvInt(t, "CLI_PROXY_EXTREME_STRESS_SESSIONS", 2000)
	totalRequests := extremeStressEnvInt(t, "CLI_PROXY_EXTREME_STRESS_REQUESTS", 3000)
	targetRPS := extremeStressEnvFloat(t, "CLI_PROXY_EXTREME_STRESS_TARGET_RPS", 100.0)
	approxTokensPerRequest := extremeStressEnvInt(t, "CLI_PROXY_EXTREME_STRESS_TOKENS_PER_REQUEST", 33333)
	serviceTime := extremeStressEnvDuration(t, "CLI_PROXY_EXTREME_STRESS_SERVICE_TIME", 20*time.Second)
	minPromptBytes := extremeStressEnvInt(t, "CLI_PROXY_EXTREME_STRESS_MIN_PROMPT_BYTES", 100_000)

	if sessionCount <= 0 {
		t.Fatalf("invalid session count: %d", sessionCount)
	}
	if totalRequests <= 0 {
		t.Fatalf("invalid total requests: %d", totalRequests)
	}
	if targetRPS <= 0 {
		t.Fatalf("invalid target rps: %.2f", targetRPS)
	}
	if approxTokensPerRequest <= 0 {
		t.Fatalf("invalid tokens per request: %d", approxTokensPerRequest)
	}
	if serviceTime <= 0 {
		t.Fatalf("invalid service time: %s", serviceTime)
	}
	if minPromptBytes <= 0 {
		t.Fatalf("invalid min prompt bytes: %d", minPromptBytes)
	}

	targetRPM := targetRPS * 60
	targetTPM := targetRPM * float64(approxTokensPerRequest)
	requestInterval := time.Duration(float64(time.Second) / targetRPS)
	if requestInterval <= 0 {
		requestInterval = time.Microsecond
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if gotRole := gjson.GetBytes(body, "input.0.role").String(); gotRole != "developer" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"bad_request","message":"system role was not rewritten"}}`)
			return
		}
		prompt := gjson.GetBytes(body, "input.1.content.0.text").String()
		if len(prompt) < minPromptBytes {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"bad_request","message":"prompt too small for extreme stress"}}`)
			return
		}
		marker := extractExtremeStressMarker(prompt)
		if marker == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"bad_request","message":"missing session marker"}}`)
			return
		}

		time.Sleep(serviceTime)

		switch extremeStressStatusForMarker(marker) {
		case http.StatusOK:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+codexCompletedEventJSON("resp_"+marker, "gpt-5.4", "ack:"+marker)+"\n")
		case http.StatusUnauthorized:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_api_key","message":"unauthorized"}}`)
		case http.StatusTooManyRequests:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"usage_limit_reached","resets_in_seconds":9}}`)
		case http.StatusBadGateway:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":{"type":"upstream_error","message":"bad gateway"}}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"type":"unexpected_status"}}`)
		}
	}))
	defer server.Close()

	runtime.GC()
	debug.FreeOSMemory()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	executor := NewCodexExecutor(&config.Config{})
	errCh := make(chan error, totalRequests)

	var (
		wg           sync.WaitGroup
		inflight     atomic.Int64
		peakInflight atomic.Int64
		successes    atomic.Int64
		unauthorized atomic.Int64
		quota        atomic.Int64
		badGateway   atomic.Int64
		latMu        sync.Mutex
		latencies    = make([]time.Duration, 0, totalRequests)
	)

	samplerDone := make(chan struct{})
	peakSampleCh := startExtremeStressSampler(samplerDone, &inflight)

	feedBegin := time.Now()
	ticker := time.NewTicker(requestInterval)
	defer ticker.Stop()
	for i := 0; i < totalRequests; i++ {
		<-ticker.C

		sessionID := i % sessionCount
		marker := fmt.Sprintf("session-%04d", sessionID)
		req := newExtremeCodexResponsesRequest(marker, approxTokensPerRequest)
		wg.Add(1)
		go func(marker string, req cliproxyexecutor.Request) {
			defer wg.Done()

			currentInflight := inflight.Add(1)
			extremeStressUpdateMaxInt64(&peakInflight, currentInflight)
			started := time.Now()
			resp, err := executor.Execute(
				context.Background(),
				newCodexTestAuth(server.URL, extremeStressAPIKeyForMarker(marker)),
				req,
				cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FromString("openai-response"),
					Metadata: map[string]any{
						cliproxyexecutor.RequestedModelMetadataKey: "gpt-5.4",
					},
				},
			)
			duration := time.Since(started)
			inflight.Add(-1)

			latMu.Lock()
			latencies = append(latencies, duration)
			latMu.Unlock()

			wantStatus := extremeStressStatusForMarker(marker)
			if wantStatus == http.StatusOK {
				if err != nil {
					errCh <- fmt.Errorf("%s: Execute() unexpected error = %v", marker, err)
					return
				}
				if gotText := gjson.GetBytes(resp.Payload, "output.0.content.0.text").String(); gotText != "ack:"+marker {
					errCh <- fmt.Errorf("%s: response text = %q, want %q", marker, gotText, "ack:"+marker)
					return
				}
				successes.Add(1)
				return
			}

			if err == nil {
				errCh <- fmt.Errorf("%s: expected error with status %d, got nil", marker, wantStatus)
				return
			}
			statusErr, ok := err.(codexStatusError)
			if !ok {
				errCh <- fmt.Errorf("%s: error type = %T, want status error", marker, err)
				return
			}
			if statusErr.StatusCode() != wantStatus {
				errCh <- fmt.Errorf("%s: status code = %d, want %d", marker, statusErr.StatusCode(), wantStatus)
				return
			}
			if wantStatus == http.StatusTooManyRequests {
				retryAfter := statusErr.RetryAfter()
				if retryAfter == nil || *retryAfter != 9*time.Second {
					if retryAfter == nil {
						errCh <- fmt.Errorf("%s: RetryAfter = nil, want 9s", marker)
						return
					}
					errCh <- fmt.Errorf("%s: RetryAfter = %v, want 9s", marker, *retryAfter)
					return
				}
			}

			switch wantStatus {
			case http.StatusUnauthorized:
				unauthorized.Add(1)
			case http.StatusTooManyRequests:
				quota.Add(1)
			case http.StatusBadGateway:
				badGateway.Add(1)
			}
		}(marker, req)
	}
	feedElapsed := time.Since(feedBegin)

	wg.Wait()
	totalElapsed := time.Since(feedBegin)
	close(samplerDone)
	peakSample := <-peakSampleCh
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	runtime.GC()
	debug.FreeOSMemory()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	expectedSuccesses, expected401, expected429, expected502 := extremeStressExpectedCounts(totalRequests, sessionCount)
	if got := successes.Load(); got != int64(expectedSuccesses) {
		t.Fatalf("successes = %d, want %d", got, expectedSuccesses)
	}
	if got := unauthorized.Load(); got != int64(expected401) {
		t.Fatalf("401 count = %d, want %d", got, expected401)
	}
	if got := quota.Load(); got != int64(expected429) {
		t.Fatalf("429 count = %d, want %d", got, expected429)
	}
	if got := badGateway.Load(); got != int64(expected502) {
		t.Fatalf("502 count = %d, want %d", got, expected502)
	}
	expectedPeakInflight := extremeStressExpectedPeakInflight(sessionCount, totalRequests, targetRPS, serviceTime)
	slack := expectedPeakInflight/100 + 25
	minPeakInflight := expectedPeakInflight - slack
	if minPeakInflight < 1 {
		minPeakInflight = 1
	}
	if peakSample.Inflight < int64(minPeakInflight) {
		t.Fatalf("peak inflight = %d, want at least %d (expected_peak=%d, slack=%d)", peakSample.Inflight, minPeakInflight, expectedPeakInflight, slack)
	}

	p50 := extremeStressPercentileDuration(latencies, 0.50)
	p95 := extremeStressPercentileDuration(latencies, 0.95)
	p99 := extremeStressPercentileDuration(latencies, 0.99)
	feedRPM := float64(totalRequests) / feedElapsed.Seconds() * 60
	scaledTPM := feedRPM * float64(approxTokensPerRequest)

	t.Logf(
		"extreme codex stress: sessions=%d requests=%d feed_elapsed=%s total_elapsed=%s feed_rpm=%.0f scaled_tpm=%.0f peak_inflight=%d p50=%s p95=%s p99=%s heap_before=%d(%.1fMB) heap_peak_alloc=%d(%.1fMB) heap_peak_inuse=%d(%.1fMB) heap_after=%d(%.1fMB) heap_peak_delta=%d(%.1fMB) num_gc_delta=%d pause_total_ms_delta=%.2f target_rpm=%.0f target_tpm=%.0f",
		sessionCount,
		totalRequests,
		feedElapsed,
		totalElapsed,
		feedRPM,
		scaledTPM,
		peakSample.Inflight,
		p50,
		p95,
		p99,
		before.HeapAlloc,
		extremeStressBytesToMB(before.HeapAlloc),
		peakSample.HeapAlloc,
		extremeStressBytesToMB(peakSample.HeapAlloc),
		peakSample.HeapInuse,
		extremeStressBytesToMB(peakSample.HeapInuse),
		after.HeapAlloc,
		extremeStressBytesToMB(after.HeapAlloc),
		peakSample.HeapAlloc-before.HeapAlloc,
		extremeStressBytesToMB(peakSample.HeapAlloc-before.HeapAlloc),
		after.NumGC-before.NumGC,
		float64(after.PauseTotalNs-before.PauseTotalNs)/1e6,
		targetRPM,
		targetTPM,
	)
}

type extremeStressSample struct {
	HeapAlloc  uint64
	HeapInuse  uint64
	Goroutines int
	Inflight   int64
}

func startExtremeStressSampler(done <-chan struct{}, inflight *atomic.Int64) <-chan extremeStressSample {
	result := make(chan extremeStressSample, 1)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		var peak extremeStressSample
		sample := func() {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peak.HeapAlloc {
				peak.HeapAlloc = ms.HeapAlloc
			}
			if ms.HeapInuse > peak.HeapInuse {
				peak.HeapInuse = ms.HeapInuse
			}
			if goroutines := runtime.NumGoroutine(); goroutines > peak.Goroutines {
				peak.Goroutines = goroutines
			}
			if currentInflight := inflight.Load(); currentInflight > peak.Inflight {
				peak.Inflight = currentInflight
			}
		}

		sample()
		for {
			select {
			case <-ticker.C:
				sample()
			case <-done:
				sample()
				result <- peak
				return
			}
		}
	}()
	return result
}

func newExtremeCodexResponsesRequest(marker string, approxTokens int) cliproxyexecutor.Request {
	return cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(fmt.Sprintf(`{
			"model":"gpt-5.4",
			"input":[
				{"type":"message","role":"system","content":[{"type":"input_text","text":"be precise and preserve latency under load"}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}
			],
			"stream":false,
			"user":%q
		}`, buildExtremeTokenPrompt(marker, approxTokens), marker)),
	}
}

func buildExtremeTokenPrompt(marker string, approxTokens int) string {
	var b strings.Builder
	b.Grow(len(marker) + approxTokens*4 + 32)
	b.WriteString("marker=")
	b.WriteString(marker)
	b.WriteByte('\n')
	for i := 0; i < approxTokens; i++ {
		b.WriteString("tok ")
	}
	return b.String()
}

func extractExtremeStressMarker(prompt string) string {
	line, _, _ := strings.Cut(prompt, "\n")
	return strings.TrimPrefix(line, "marker=")
}

func extremeStressStatusForMarker(marker string) int {
	var sessionID int
	if _, err := fmt.Sscanf(marker, "session-%d", &sessionID); err != nil {
		return http.StatusInternalServerError
	}
	slot := sessionID % 20
	switch {
	case slot < 12:
		return http.StatusOK
	case slot < 15:
		return http.StatusUnauthorized
	case slot < 18:
		return http.StatusTooManyRequests
	default:
		return http.StatusBadGateway
	}
}

func extremeStressAPIKeyForMarker(marker string) string {
	switch extremeStressStatusForMarker(marker) {
	case http.StatusOK:
		return "success-key"
	case http.StatusUnauthorized:
		return "invalid-401"
	case http.StatusTooManyRequests:
		return "quota-429"
	case http.StatusBadGateway:
		return "upstream-502"
	default:
		return "unexpected"
	}
}

func extremeStressExpectedCounts(totalRequests, sessionCount int) (successes, unauthorized, quota, badGateway int) {
	for i := 0; i < totalRequests; i++ {
		switch extremeStressStatusForMarker(fmt.Sprintf("session-%04d", i%sessionCount)) {
		case http.StatusOK:
			successes++
		case http.StatusUnauthorized:
			unauthorized++
		case http.StatusTooManyRequests:
			quota++
		case http.StatusBadGateway:
			badGateway++
		}
	}
	return successes, unauthorized, quota, badGateway
}

func extremeStressUpdateMaxInt64(dst *atomic.Int64, candidate int64) {
	for {
		current := dst.Load()
		if candidate <= current {
			return
		}
		if dst.CompareAndSwap(current, candidate) {
			return
		}
	}
}

func extremeStressPercentileDuration(values []time.Duration, fraction float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(float64(len(sorted)-1) * fraction)
	return sorted[index]
}

func extremeStressBytesToMB(v uint64) float64 {
	return float64(v) / (1024 * 1024)
}

func extremeStressEnvInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%s=%q is not a valid int: %v", key, raw, err)
	}
	return parsed
}

func extremeStressEnvFloat(t *testing.T, key string, fallback float64) float64 {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		t.Fatalf("%s=%q is not a valid float: %v", key, raw, err)
	}
	return parsed
}

func extremeStressEnvDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		t.Fatalf("%s=%q is not a valid duration: %v", key, raw, err)
	}
	return parsed
}

func extremeStressExpectedPeakInflight(sessionCount, totalRequests int, targetRPS float64, serviceTime time.Duration) int {
	expected := sessionCount
	if totalRequests < expected {
		expected = totalRequests
	}
	steady := int(targetRPS * serviceTime.Seconds())
	if steady < expected {
		expected = steady
	}
	if expected < 1 {
		return 1
	}
	return expected
}
