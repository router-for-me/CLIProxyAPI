package codex

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRefreshTokensWithRetry_NonRetryableOnlyAttemptsOnce(t *testing.T) {
	var calls int32
	auth := &CodexAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				atomic.AddInt32(&calls, 1)
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant","code":"refresh_token_reused"}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	_, err := auth.RefreshTokensWithRetry(context.Background(), "dummy_refresh_token", 3)
	if err == nil {
		t.Fatalf("expected error for non-retryable refresh failure")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "refresh_token_reused") {
		t.Fatalf("expected refresh_token_reused in error, got: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 refresh attempt, got %d", got)
	}
}

// TestRefreshTokens_ConcurrentSingleFlight verifies that concurrent refreshes for the
// same refresh token are collapsed into a single upstream call. OpenAI rotates the
// refresh token on each success, so without de-duplication concurrent refreshes race
// and the loser fails with refresh_token_reused.
func TestRefreshTokens_ConcurrentSingleFlight(t *testing.T) {
	const concurrency = 8
	var calls int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	auth := &CodexAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				atomic.AddInt32(&calls, 1)
				select {
				case entered <- struct{}{}:
				default:
				}
				// Block so concurrent callers coalesce into the in-flight refresh.
				<-release
				body := `{"access_token":"new_access","refresh_token":"new_refresh","id_token":"","expires_in":3600}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	var (
		wg      sync.WaitGroup
		started sync.WaitGroup
		results = make([]error, concurrency)
	)
	started.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			started.Done()
			_, err := auth.RefreshTokens(context.Background(), "shared_refresh_token")
			results[idx] = err
		}(i)
	}

	started.Wait()
	<-entered                         // the single upstream call has reached the transport
	time.Sleep(50 * time.Millisecond) // let the remaining callers coalesce via singleflight
	close(release)                    // allow the upstream call to complete
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 upstream refresh call, got %d", got)
	}
	for i, err := range results {
		if err != nil {
			t.Fatalf("concurrent refresh %d failed: %v", i, err)
		}
	}
}

func TestNewCodexAuthWithProxyURL_OverrideDirectDisablesProxy(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://proxy.example.com:8080"}}
	auth := NewCodexAuthWithProxyURL(cfg, "direct")

	transport, ok := auth.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected http.Transport, got %T", auth.httpClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestNewCodexAuthWithProxyURL_OverrideProxyTakesPrecedence(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://global.example.com:8080"}}
	auth := NewCodexAuthWithProxyURL(cfg, "http://override.example.com:8081")

	transport, ok := auth.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected http.Transport, got %T", auth.httpClient.Transport)
	}
	req, errReq := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errReq != nil {
		t.Fatalf("new request: %v", errReq)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("proxy func: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://override.example.com:8081" {
		t.Fatalf("proxy URL = %v, want http://override.example.com:8081", proxyURL)
	}
}
