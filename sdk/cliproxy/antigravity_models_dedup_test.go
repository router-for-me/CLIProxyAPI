package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func resetAntigravityCapabilityCache() {
	antigravityCapabilityMu.Lock()
	antigravityCapabilityCache = make(map[string]antigravityCapabilityCacheEntry)
	antigravityAuthFailureCache = make(map[string]antigravityFailureState)
	antigravityCapabilityMu.Unlock()
}

func antigravityTestAuth(id, endpoint string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: "antigravity", Attributes: map[string]string{"base_url": endpoint}, Metadata: map[string]any{"access_token": "token-" + id, "project_id": "project-" + id}}
}

func TestAntigravityModelBaseURLs_DefaultDaily(t *testing.T) {
	urls := antigravityModelBaseURLs(&coreauth.Auth{})
	if len(urls) != 1 || urls[0] != antigravityModelBaseURLDaily {
		t.Fatalf("unexpected default: %v", urls)
	}
	if antigravityCapabilityCacheTTL != registry.ModelsRefreshInterval {
		t.Fatal("catalog and entitlement cache must share cadence")
	}
}

func TestAntigravityCapabilityProbe_DeduplicatesConcurrentRequests(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		_, _ = w.Write([]byte(`{"models":{"allowed":{}},"webSearchModelIds":["allowed"]}`))
	}))
	t.Cleanup(func() { unblock(); server.Close() })
	svc := &Service{cfg: &config.Config{}}
	auth := antigravityTestAuth("shared", server.URL)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
			if _, ok := hints.ModelIDs["allowed"]; !ok {
				t.Error("missing model")
			}
		}()
	}
	<-started
	unblock()
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestAntigravityCapabilityProbe_AccountAndProjectIsolation(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != antigravityModelsPath {
			t.Errorf("path=%s", r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		id := body["project"]
		_ = json.NewEncoder(w).Encode(map[string]any{"models": map[string]any{id: map[string]any{}}})
	}))
	defer server.Close()
	svc := &Service{cfg: &config.Config{}}
	a, b := antigravityTestAuth("a", server.URL), antigravityTestAuth("b", server.URL)
	for _, auth := range []*coreauth.Auth{a, b, a, b} {
		hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
		if len(hints.ModelIDs) != 1 {
			t.Fatalf("hints=%v", hints)
		}
		if _, ok := hints.ModelIDs[auth.Metadata["project_id"].(string)]; !ok {
			t.Fatal("cross-account catalog")
		}
		delete(hints.ModelIDs, auth.Metadata["project_id"].(string))
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
	a.Metadata["access_token"] = "rotated"
	svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), a)
	if calls.Load() != 2 {
		t.Fatal("token rotation invalidated successful catalog")
	}
	a.Metadata["project_id"] = "changed"
	svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), a)
	if calls.Load() != 3 {
		t.Fatal("project change reused catalog")
	}
}

func TestAntigravityCapabilityProbe_TTLAndStaleOnError(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	antigravityNowFunc = func() time.Time { return now }
	t.Cleanup(func() { antigravityNowFunc = time.Now })
	var calls atomic.Int32
	var status atomic.Int32
	status.Store(200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"models":{"allowed":{}}}`))
	}))
	defer server.Close()
	svc := &Service{cfg: &config.Config{}}
	auth := antigravityTestAuth("ttl", server.URL)
	svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	now = now.Add(antigravityCapabilityCacheTTL - time.Second)
	svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	if calls.Load() != 1 {
		t.Fatal("expired too early")
	}
	now = now.Add(time.Second)
	status.Store(500)
	hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	if _, ok := hints.ModelIDs["allowed"]; !ok {
		t.Fatal("lost last successful catalog")
	}
	svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	if calls.Load() != 2 {
		t.Fatal("failure not throttled")
	}
	now = now.Add(2 * antigravityCapabilityRetryBase)
	status.Store(200)
	svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	if calls.Load() != 3 {
		t.Fatal("did not retry")
	}
}

func TestAntigravityCapabilityProbe_FailuresAreAccountScoped(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") == "Bearer token-bad" {
					w.WriteHeader(status)
					return
				}
				_, _ = w.Write([]byte(`{"models":{"allowed":{}}}`))
			}))
			defer server.Close()
			svc := &Service{cfg: &config.Config{}}
			bad := antigravityTestAuth("bad", server.URL)
			good := antigravityTestAuth("good", server.URL)
			if hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), bad); hints.ModelIDs != nil {
				t.Fatal("error treated as empty catalog")
			}
			svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), bad)
			if hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), good); len(hints.ModelIDs) != 1 {
				t.Fatal("good account blocked")
			}
			if calls.Load() != 2 {
				t.Fatalf("calls=%d", calls.Load())
			}
			bad.Metadata["access_token"] = "refreshed"
			if hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), bad); len(hints.ModelIDs) != 1 {
				t.Fatal("new token blocked")
			}
		})
	}
}

func TestAntigravityCapabilityParsing(t *testing.T) {
	for _, tc := range []struct {
		body         string
		valid, known bool
		count        int
	}{
		{`{"models":{"known":{"quotaInfo":{"remainingFraction":0}}}}`, true, true, 1},
		{`{"models":{}}`, true, true, 0},
		{`{"webSearchModelIds":[]}`, true, false, 0},
		{`{"models":null}`, true, false, 0},
		{`{"models":[]}`, false, false, 0},
		{`{"models":`, false, false, 0},
	} {
		hints, ok := parseAntigravityModelCapabilityHints([]byte(tc.body))
		if ok != tc.valid || (hints.ModelIDs != nil) != tc.known || len(hints.ModelIDs) != tc.count {
			t.Fatalf("body=%s hints=%v valid=%v", tc.body, hints, ok)
		}
	}
}

func TestAntigravityCapabilityProbe_CancelledDoesNotCacheFailure(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	svc := &Service{cfg: &config.Config{}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	svc.fetchAntigravityModelCapabilityHintsForAuth(ctx, antigravityTestAuth("cancelled", "http://127.0.0.1:1"))
	if len(antigravityCapabilityCache) != 0 || len(antigravityAuthFailureCache) != 0 {
		t.Fatal("cancellation cached")
	}
}
