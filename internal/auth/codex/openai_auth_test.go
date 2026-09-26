package codex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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
}

func TestIsNonRetryableRefreshErr(t *testing.T) {
	// Terminal cases mirror how RefreshTokens formats a non-200 token endpoint
	// response: "token refresh failed with status %d: %s".
	refreshStatusErr := func(statusCode int, body string) error {
		return fmt.Errorf("token refresh failed with status %d: %s", statusCode, body)
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "transport failure",
			err:  errors.New(`Post "https://auth.openai.com/oauth/token": dial tcp: i/o timeout`),
			want: false,
		},
		{
			name: "server error",
			err:  refreshStatusErr(http.StatusServiceUnavailable, `{"error":"server_error","error_description":"upstream unavailable"}`),
			want: false,
		},
		{
			name: "refresh token reused",
			err:  refreshStatusErr(http.StatusBadRequest, `{"error":"invalid_request","code":"refresh_token_reused"}`),
			want: true,
		},
		{
			name: "refresh token invalidated",
			err:  refreshStatusErr(http.StatusBadRequest, `{"error":"invalid_request","code":"refresh_token_invalidated"}`),
			want: true,
		},
		{
			name: "token invalidated",
			err:  refreshStatusErr(http.StatusUnauthorized, `{"error":"unauthorized","code":"token_invalidated"}`),
			want: true,
		},
		{
			name: "invalid grant",
			err:  refreshStatusErr(http.StatusUnauthorized, `{"error":"invalid_grant","error_description":"refresh token revoked"}`),
			want: true,
		},
		{
			name: "invalid grant uppercase",
			err:  refreshStatusErr(http.StatusBadRequest, `{"error":"INVALID_GRANT","error_description":"Refresh token revoked"}`),
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNonRetryableRefreshErr(tc.err); got != tc.want {
				t.Fatalf("isNonRetryableRefreshErr(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

func TestRefreshTokensWithRetry_NonRetryableOnlyAttemptsOnce(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		body       string
		wantErr    string
	}{
		{
			name:       "refresh_token_reused",
			statusCode: http.StatusBadRequest,
			body:       `{"error":"invalid_grant","code":"refresh_token_reused"}`,
			wantErr:    "refresh_token_reused",
		},
		{
			name:       "refresh_token_invalidated",
			statusCode: http.StatusBadRequest,
			body:       `{"error":"invalid_request","code":"refresh_token_invalidated"}`,
			wantErr:    "refresh_token_invalidated",
		},
		{
			name:       "token_invalidated",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":"unauthorized","code":"token_invalidated"}`,
			wantErr:    "token_invalidated",
		},
		{
			name:       "invalid_grant",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":"invalid_grant","error_description":"refresh token revoked"}`,
			wantErr:    "invalid_grant",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			auth := &CodexAuth{
				httpClient: &http.Client{
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						atomic.AddInt32(&calls, 1)
						return &http.Response{
							StatusCode: tc.statusCode,
							Body:       io.NopCloser(strings.NewReader(tc.body)),
							Header:     make(http.Header),
							Request:    req,
						}, nil
					}),
				},
			}

			_, err := auth.RefreshTokensWithRetry(context.Background(), "dummy_refresh_token_"+tc.name, 3)
			if err == nil {
				t.Fatalf("expected error for non-retryable refresh failure")
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.wantErr) {
				t.Fatalf("expected %s in error, got: %v", tc.wantErr, err)
			}
			if got := atomic.LoadInt32(&calls); got != 1 {
				t.Fatalf("expected 1 refresh attempt, got %d", got)
			}
		})
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
