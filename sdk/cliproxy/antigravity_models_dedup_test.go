package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"golang.org/x/sync/singleflight"
)

func resetAntigravityCapabilityCache() {
	antigravityCapabilityMu.Lock()
	antigravityCapabilityCache = make(map[string]antigravityCapabilityCacheEntry)
	antigravityAuthFailureCache = make(map[string]time.Time)
	antigravityCapabilityMu.Unlock()
	antigravityCapabilityGroup = singleflight.Group{}
}

// TestAntigravityModelBaseURLs_DefaultDaily verifies that when no custom base_url
// or base_urls attribute is configured, the default base URL is daily-cloudcode-pa.googleapis.com only.
func TestAntigravityModelBaseURLs_DefaultDaily(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "test-ag",
		Provider: "antigravity",
	}
	urls := antigravityModelBaseURLs(auth)
	if len(urls) != 1 {
		t.Fatalf("antigravityModelBaseURLs returned %d URLs (%v), want exactly 1 default URL", len(urls), urls)
	}
	if urls[0] != antigravityModelBaseURLDaily {
		t.Fatalf("antigravityModelBaseURLs[0] = %q, want %q", urls[0], antigravityModelBaseURLDaily)
	}
}

// TestAntigravityCapabilityProbe_DeduplicatesConcurrentRequests verifies that concurrent capability
// probe requests across multiple accounts share a single upstream request via singleflight deduplication
// even before any cache entry has been committed.
func TestAntigravityCapabilityProbe_DeduplicatesConcurrentRequests(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	var requestCount atomic.Int32
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRequest) }) }
	defer release()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		close(requestStarted)
		<-releaseRequest
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"webSearchModelIds":["gemini-3.1-flash-lite"]}`))
	}))
	t.Cleanup(func() {
		release()
		server.CloseClientConnections()
		server.Close()
	})

	svc := &Service{
		cfg: &config.Config{},
	}

	const numCallers = 5
	var wg sync.WaitGroup
	wg.Add(numCallers)

	// Caller 1: initiates request and blocks inside the server handler
	go func() {
		defer wg.Done()
		auth := &coreauth.Auth{
			ID:       "test-ag-dedup-1",
			Provider: "antigravity",
			Attributes: map[string]string{
				"base_url": server.URL,
			},
			Metadata: map[string]any{
				"access_token": "ya29.test-token-1",
			},
		}
		hints := svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth)
		if _, ok := hints.WebSearchModelIDs["gemini-3.1-flash-lite"]; !ok {
			t.Errorf("caller 1: expected gemini-3.1-flash-lite in hints")
		}
	}()

	// Wait until caller 1's request is actively inside the server handler (before cache is written)
	<-requestStarted

	// Callers 2..5: launched while caller 1 is in flight. They must be merged by singleflight.
	for i := 2; i <= numCallers; i++ {
		go func(idx int) {
			defer wg.Done()
			auth := &coreauth.Auth{
				ID:       "test-ag-dedup-subsequent",
				Provider: "antigravity",
				Attributes: map[string]string{
					"base_url": server.URL,
				},
				Metadata: map[string]any{
					"access_token": "ya29.test-token",
				},
			}
			hints := svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth)
			if _, ok := hints.WebSearchModelIDs["gemini-3.1-flash-lite"]; !ok {
				t.Errorf("caller %d: expected gemini-3.1-flash-lite in hints", idx)
			}
		}(i)
	}

	// Deterministically verify that all callers are in-flight in singleflight before server responds
	deadline := time.Now().Add(2 * time.Second)
	for antigravityProbeInFlight.Load() < numCallers && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := antigravityProbeInFlight.Load(); got != numCallers {
		t.Fatalf("expected all %d callers in flight, got %d", numCallers, got)
	}

	// Release the in-flight server response
	release()
	wg.Wait()

	if got := requestCount.Load(); got != 1 {
		t.Fatalf("concurrent probes resulted in %d upstream requests, want exactly 1 deduplicated request", got)
	}
}

// TestAntigravityCapabilityProbe_CachedWithinTTL verifies that capability probe results
// are cached and repeated probes within the 5-minute TTL do not hit upstream network.
func TestAntigravityCapabilityProbe_CachedWithinTTL(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"webSearchModelIds":["gemini-3.1-flash-lite"]}`))
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})

	svc := &Service{
		cfg: &config.Config{},
	}

	auth1 := &coreauth.Auth{
		ID:       "test-ag-cache-1",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "ya29.test-token-1",
		},
	}

	// First probe hits upstream
	hints1 := svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth1)
	if _, ok := hints1.WebSearchModelIDs["gemini-3.1-flash-lite"]; !ok {
		t.Fatalf("first probe: expected gemini-3.1-flash-lite in hints")
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("first probe resulted in %d upstream requests, want 1", got)
	}

	// Second probe for another account with same base URL within TTL must be served from cache
	auth2 := &coreauth.Auth{
		ID:       "test-ag-cache-2",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "ya29.test-token-2",
		},
	}

	hints2 := svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth2)
	if _, ok := hints2.WebSearchModelIDs["gemini-3.1-flash-lite"]; !ok {
		t.Fatalf("second probe: expected gemini-3.1-flash-lite in hints")
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("second probe hit upstream; total requests = %d, want 1 (served from cache)", got)
	}
}

// TestAntigravityCapabilityProbe_CacheExpiresAfterTTL verifies that cached capability probe
// results expire after the 5-minute TTL, triggering a new upstream fetch.
func TestAntigravityCapabilityProbe_CacheExpiresAfterTTL(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	simulatedTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	antigravityNowFunc = func() time.Time { return simulatedTime }
	t.Cleanup(func() { antigravityNowFunc = time.Now })

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"webSearchModelIds":["gemini-3.1-flash-lite"]}`))
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})

	svc := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "test-ag-ttl",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "ya29.test",
		},
	}

	// 1. Initial probe hits upstream
	svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth)
	if requestCount.Load() != 1 {
		t.Fatalf("first probe requests = %d, want 1", requestCount.Load())
	}

	// 2. Advance clock by 4 minutes (still within 5m TTL) -> served from cache
	simulatedTime = simulatedTime.Add(4 * time.Minute)
	svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth)
	if requestCount.Load() != 1 {
		t.Fatalf("probe at 4m requests = %d, want 1 (cached)", requestCount.Load())
	}

	// 3. Advance clock past 5-minute TTL -> fetches from upstream again
	simulatedTime = simulatedTime.Add(1*time.Minute + time.Second)
	svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth)
	if requestCount.Load() != 2 {
		t.Fatalf("probe after TTL expiry requests = %d, want 2", requestCount.Load())
	}
}

// TestAntigravityCapabilityProbe_CachesEmptyWebSearchModelIDs verifies that valid upstream
// responses with empty WebSearchModelIDs are cached for the full 5-minute TTL.
func TestAntigravityCapabilityProbe_CachesEmptyWebSearchModelIDs(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	simulatedTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	antigravityNowFunc = func() time.Time { return simulatedTime }
	t.Cleanup(func() { antigravityNowFunc = time.Now })

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"webSearchModelIds":[]}`))
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})

	svc := &Service{cfg: &config.Config{}}
	auth1 := &coreauth.Auth{
		ID:         "test-ag-empty-1",
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "ya29.test-1"},
	}

	// First probe: hits upstream, returns empty hints
	hints1 := svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth1)
	if len(hints1.WebSearchModelIDs) != 0 {
		t.Fatalf("expected empty hints, got %v", hints1)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("first probe requests = %d, want 1", got)
	}

	// Second probe for another account 3 minutes later: must be served from cache
	simulatedTime = simulatedTime.Add(3 * time.Minute)
	auth2 := &coreauth.Auth{
		ID:         "test-ag-empty-2",
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "ya29.test-2"},
	}
	hints2 := svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth2)
	if len(hints2.WebSearchModelIDs) != 0 {
		t.Fatalf("expected empty hints, got %v", hints2)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("second probe hit upstream; total requests = %d, want 1 (served from cache)", got)
	}
}

// TestAntigravityCapabilityProbe_CachesFailureWithBackoff verifies that failed upstream probes
// (e.g. 500 error) are cached with failure backoff TTL (1 minute) to avoid spamming upstream.
func TestAntigravityCapabilityProbe_CachesFailureWithBackoff(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	simulatedTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	antigravityNowFunc = func() time.Time { return simulatedTime }
	t.Cleanup(func() { antigravityNowFunc = time.Now })

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})

	svc := &Service{cfg: &config.Config{}}
	auth1 := &coreauth.Auth{
		ID:         "test-ag-fail-1",
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "ya29.test-1"},
	}

	// First probe: fails with 500
	hints1 := svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth1)
	if len(hints1.WebSearchModelIDs) != 0 {
		t.Fatalf("expected empty hints on error, got %v", hints1)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("first probe requests = %d, want 1", got)
	}

	// Second probe for another account 30 seconds later (within 1m failure TTL): served from failure cache
	simulatedTime = simulatedTime.Add(30 * time.Second)
	auth2 := &coreauth.Auth{
		ID:         "test-ag-fail-2",
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "ya29.test-2"},
	}
	hints2 := svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth2)
	if len(hints2.WebSearchModelIDs) != 0 {
		t.Fatalf("expected empty hints on error, got %v", hints2)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("second probe during failure backoff hit upstream; total requests = %d, want 1", got)
	}

	// Third probe after failure TTL (61 seconds): retries upstream
	simulatedTime = simulatedTime.Add(31 * time.Second)
	hints3 := svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth2)
	if len(hints3.WebSearchModelIDs) != 0 {
		t.Fatalf("expected empty hints on error, got %v", hints3)
	}
	if got := requestCount.Load(); got != 2 {
		t.Fatalf("probe after failure TTL expired requests = %d, want 2", got)
	}
}

// TestAntigravityCapabilityProbe_InvalidJSONHandledAsFailure verifies that malformed JSON
// responses are treated as failures (with failure TTL) rather than successful 5-minute caches.
func TestAntigravityCapabilityProbe_InvalidJSONHandledAsFailure(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	simulatedTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	antigravityNowFunc = func() time.Time { return simulatedTime }
	t.Cleanup(func() { antigravityNowFunc = time.Now })

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not-valid-json`))
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})

	svc := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:         "test-ag-badjson",
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "ya29.test"},
	}

	// First probe: malformed JSON
	hints := svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth)
	if len(hints.WebSearchModelIDs) != 0 {
		t.Fatalf("expected empty hints on bad JSON, got %v", hints)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("requestCount = %d, want 1", requestCount.Load())
	}

	// Advance past 1 minute failure TTL (e.g. 70s) -> should retry upstream
	simulatedTime = simulatedTime.Add(70 * time.Second)
	svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth)
	if requestCount.Load() != 2 {
		t.Fatalf("requestCount after failure TTL = %d, want 2 (retried)", requestCount.Load())
	}
}

// TestAntigravityCapabilityProbe_AuthErrorDoesNotPoisonCache verifies that 401 Unauthorized
// from an expired account token does not poison the shared endpoint cache, allowing another
// account with valid credentials to probe successfully.
func TestAntigravityCapabilityProbe_AuthErrorDoesNotPoisonCache(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		authHeader := r.Header.Get("Authorization")
		if authHeader == "Bearer ya29.bad-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"webSearchModelIds":["gemini-3.1-flash-lite"]}`))
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})

	svc := &Service{cfg: &config.Config{}}

	badAuth := &coreauth.Auth{
		ID:         "test-ag-bad",
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "ya29.bad-token"},
	}

	// 1. Account with bad token fails with 401
	hintsBad := svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), badAuth)
	if len(hintsBad.WebSearchModelIDs) != 0 {
		t.Fatalf("expected empty hints on 401, got %v", hintsBad)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("requestCount after 401 = %d, want 1", requestCount.Load())
	}

	// 2. Another account with good token immediately probes the same endpoint
	// It must NOT be blocked by any failure cache and should probe successfully.
	goodAuth := &coreauth.Auth{
		ID:         "test-ag-good",
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "ya29.good-token"},
	}

	hintsGood := svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), goodAuth)
	if _, ok := hintsGood.WebSearchModelIDs["gemini-3.1-flash-lite"]; !ok {
		t.Fatalf("good account failed to get hints: %v", hintsGood)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("good account requestCount = %d, want 2", requestCount.Load())
	}

	// 3. Bad account tries again within failure TTL -> throttled by auth failure cache
	hintsBadAgain := svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), badAuth)
	if len(hintsBadAgain.WebSearchModelIDs) != 0 {
		t.Fatalf("expected empty hints for repeated bad auth, got %v", hintsBadAgain)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("repeated bad auth probe hit upstream; total requests = %d, want 2 (throttled)", requestCount.Load())
	}
}

// TestAntigravityCapabilityProbe_ConcurrentAuthErrorDoesNotBlockHealthyAccount verifies that
// if caller 1 has an invalid token (401) and caller 2 has a valid token, caller 2 detects that
// the shared probe failed due to caller 1's token and retries with its own token, succeeding.
func TestAntigravityCapabilityProbe_ConcurrentAuthErrorDoesNotBlockHealthyAccount(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	var requestCount atomic.Int32
	request1Started := make(chan struct{})
	releaseRequest1 := make(chan struct{})
	var release1Once sync.Once
	release1 := func() { release1Once.Do(func() { close(releaseRequest1) }) }
	defer release1()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := requestCount.Add(1)
		authHeader := r.Header.Get("Authorization")
		if idx == 1 {
			close(request1Started)
			<-releaseRequest1
			if authHeader != "Bearer ya29.bad-token" {
				t.Errorf("request 1 auth = %q, want ya29.bad-token", authHeader)
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		if authHeader != "Bearer ya29.good-token" {
			t.Errorf("request 2 auth = %q, want ya29.good-token", authHeader)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"webSearchModelIds":["gemini-3.1-flash-lite"]}`))
	}))
	t.Cleanup(func() {
		release1()
		server.CloseClientConnections()
		server.Close()
	})

	svc := &Service{cfg: &config.Config{}}

	badAuth := &coreauth.Auth{
		ID:         "test-ag-bad-concurrent",
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "ya29.bad-token"},
	}
	goodAuth := &coreauth.Auth{
		ID:         "test-ag-good-concurrent",
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "ya29.good-token"},
	}

	var hintsBad, hintsGood antigravityModelCapabilityHints
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		hintsBad = svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), badAuth)
	}()

	<-request1Started

	go func() {
		defer wg.Done()
		hintsGood = svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), goodAuth)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for antigravityProbeInFlight.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := antigravityProbeInFlight.Load(); got != 2 {
		t.Fatalf("expected both callers in flight, got %d", got)
	}

	release1()
	wg.Wait()

	if len(hintsBad.WebSearchModelIDs) != 0 {
		t.Fatalf("expected bad account to have empty hints, got %v", hintsBad)
	}
	if _, ok := hintsGood.WebSearchModelIDs["gemini-3.1-flash-lite"]; !ok {
		t.Fatalf("expected good account to recover and obtain capabilities, got %v", hintsGood)
	}
	if got := requestCount.Load(); got != 2 {
		t.Fatalf("requestCount = %d, want 2", got)
	}
}
