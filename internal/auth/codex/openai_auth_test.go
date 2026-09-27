package codex

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"golang.org/x/sync/singleflight"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewCodexAuthDoesNotSetRequestTimeout(t *testing.T) {
	if got := NewCodexAuth(nil).httpClient.Timeout; got != 0 {
		t.Fatalf("HTTP client timeout = %s, want zero", got)
	}
}

func TestRefreshTokens_UsesIndependentTimeout(t *testing.T) {
	resetCodexRefreshGroupForTest()
	defer resetCodexRefreshGroupForTest()

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	cancelCaller()
	var requestDeadline time.Time
	auth := &CodexAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				var ok bool
				requestDeadline, ok = req.Context().Deadline()
				if !ok {
					t.Fatal("refresh request has no deadline")
				}
				if errContext := req.Context().Err(); errContext != nil {
					t.Fatalf("refresh request context is already done: %v", errContext)
				}
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader(`{"error":"probe"}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	_, err := auth.RefreshTokens(callerCtx, "independent-timeout-token")
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if requestDeadline.IsZero() || !requestDeadline.After(time.Now()) {
		t.Fatalf("refresh deadline = %v, want a future deadline", requestDeadline)
	}
}

func resetCodexRefreshGroupForTest() {
	codexRefreshGroup = singleflight.Group{}
	codexRefreshResults.Range(func(key, _ any) bool {
		codexRefreshResults.Delete(key)
		return true
	})
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

func TestRefreshTokens_DeduplicatesConcurrentRefreshAcrossInstances(t *testing.T) {
	resetCodexRefreshGroupForTest()
	t.Cleanup(resetCodexRefreshGroupForTest)

	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		once.Do(func() { close(started) })
		<-release
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"access_token":"new-access",
				"refresh_token":"new-refresh",
				"token_type":"Bearer",
				"expires_in":3600
			}`)),
			Header:  make(http.Header),
			Request: req,
		}, nil
	})
	authA := &CodexAuth{httpClient: &http.Client{Transport: transport}}
	authB := &CodexAuth{httpClient: &http.Client{Transport: transport}}

	results := make(chan *CodexTokenData, 2)
	errs := make(chan error, 2)
	runRefresh := func(auth *CodexAuth, launched chan<- struct{}) {
		if launched != nil {
			close(launched)
		}
		tokenData, errRefresh := auth.RefreshTokens(context.Background(), "shared-refresh-token")
		results <- tokenData
		errs <- errRefresh
	}

	go runRefresh(authA, nil)
	<-started

	secondLaunched := make(chan struct{})
	go runRefresh(authB, secondLaunched)
	<-secondLaunched
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected concurrent refresh to share a single upstream call, got %d", got)
	}
	close(release)

	for i := 0; i < 2; i++ {
		if errRefresh := <-errs; errRefresh != nil {
			t.Fatalf("expected refresh to succeed, got %v", errRefresh)
		}
		tokenData := <-results
		if tokenData == nil || tokenData.AccessToken != "new-access" || tokenData.RefreshToken != "new-refresh" {
			t.Fatalf("unexpected token data: %#v", tokenData)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected both refresh callers to share a single upstream call, got %d", got)
	}
}

func TestRefreshTokens_DeduplicatesStragglersAfterRefreshCompletes(t *testing.T) {
	resetCodexRefreshGroupForTest()
	t.Cleanup(resetCodexRefreshGroupForTest)

	const callers = 50
	var calls int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		call := atomic.AddInt32(&calls, 1)
		if call == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(`{
					"access_token":"new-access",
					"refresh_token":"new-refresh",
					"token_type":"Bearer",
					"expires_in":3600
				}`)),
				Header:  make(http.Header),
				Request: req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant","code":"refresh_token_invalidated"}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})

	leader := &CodexAuth{httpClient: &http.Client{Transport: transport}}
	first, errFirst := leader.RefreshTokens(context.Background(), "rotating-refresh-token")
	if errFirst != nil {
		t.Fatalf("leader refresh failed: %v", errFirst)
	}
	if first == nil || first.RefreshToken != "new-refresh" {
		t.Fatalf("unexpected leader token data: %#v", first)
	}

	type refreshOutcome struct {
		tokenData *CodexTokenData
		err       error
	}
	start := make(chan struct{})
	outcomes := make(chan refreshOutcome, callers-1)
	var wg sync.WaitGroup
	for i := 0; i < callers-1; i++ {
		auth := &CodexAuth{httpClient: &http.Client{Transport: transport}}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tokenData, errRefresh := auth.RefreshTokens(context.Background(), "rotating-refresh-token")
			outcomes <- refreshOutcome{tokenData: tokenData, err: errRefresh}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)

	for outcome := range outcomes {
		if outcome.err != nil {
			t.Errorf("straggler refresh failed: %v", outcome.err)
			continue
		}
		if outcome.tokenData == nil || outcome.tokenData.AccessToken != "new-access" || outcome.tokenData.RefreshToken != "new-refresh" {
			t.Errorf("unexpected straggler token data: %#v", outcome.tokenData)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected all %d callers to reuse one rotated refresh result, got %d upstream calls", callers, got)
	}
}

func TestRefreshTokens_DeduplicatesFiftyCallersPerAccount(t *testing.T) {
	resetCodexRefreshGroupForTest()
	t.Cleanup(resetCodexRefreshGroupForTest)

	const callersPerAccount = 25
	tokens := []string{"account-a-refresh", "account-b-refresh"}
	var mu sync.Mutex
	calls := make(map[string]int)
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		token := req.PostFormValue("refresh_token")
		mu.Lock()
		calls[token]++
		call := calls[token]
		mu.Unlock()
		if call == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(`{
					"access_token":"new-access",
					"refresh_token":"new-refresh",
					"token_type":"Bearer",
					"expires_in":3600
				}`)),
				Header:  make(http.Header),
				Request: req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant","code":"refresh_token_invalidated"}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})

	for _, token := range tokens {
		auth := &CodexAuth{httpClient: &http.Client{Transport: transport}}
		if _, errRefresh := auth.RefreshTokens(context.Background(), token); errRefresh != nil {
			t.Fatalf("leader refresh for %s failed: %v", token, errRefresh)
		}
	}

	start := make(chan struct{})
	errs := make(chan error, len(tokens)*(callersPerAccount-1))
	var wg sync.WaitGroup
	for _, token := range tokens {
		for i := 1; i < callersPerAccount; i++ {
			auth := &CodexAuth{httpClient: &http.Client{Transport: transport}}
			wg.Add(1)
			go func(refreshToken string) {
				defer wg.Done()
				<-start
				tokenData, errRefresh := auth.RefreshTokens(context.Background(), refreshToken)
				if errRefresh == nil && (tokenData == nil || tokenData.RefreshToken != "new-refresh") {
					errRefresh = fmt.Errorf("unexpected token data: %#v", tokenData)
				}
				errs <- errRefresh
			}(token)
		}
	}
	close(start)
	wg.Wait()
	close(errs)
	for errRefresh := range errs {
		if errRefresh != nil {
			t.Errorf("straggler refresh failed: %v", errRefresh)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for _, token := range tokens {
		if got := calls[token]; got != 1 {
			t.Fatalf("account %s made %d upstream refresh calls, want 1", token, got)
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
