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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
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

func TestCodexAuth_TokenRequestsUserAgent(t *testing.T) {
	configured := func(disabled bool, ua string) *config.Config {
		return &config.Config{
			Codex:               config.CodexConfig{DisableCodexCloaking: disabled},
			CodexHeaderDefaults: config.CodexHeaderDefaults{UserAgent: ua},
		}
	}
	cases := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{"nil_config", nil, constant.DefaultCodexUserAgent},
		{"empty_config", &config.Config{}, constant.DefaultCodexUserAgent},
		{"cloaking_overrides_config", configured(false, "custom-ua"), constant.DefaultCodexUserAgent},
		{"configured", configured(true, "custom-ua"), "custom-ua"},
		{"trimmed", configured(true, "  custom-ua  "), "custom-ua"},
		{"empty", configured(true, ""), constant.DefaultCodexUserAgent},
		{"whitespace", configured(true, " \t "), constant.DefaultCodexUserAgent},
	}
	for _, flow := range []string{"authorization_code", "refresh_token"} {
		for _, tc := range cases {
			t.Run(flow+"/"+tc.name, func(t *testing.T) {
				resetCodexRefreshGroupForTest()
				t.Cleanup(resetCodexRefreshGroupForTest)
				auth := NewCodexAuth(tc.cfg)
				calls := 0
				auth.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != http.MethodPost || req.URL.String() != TokenURL {
						t.Errorf("request = %s %s, want POST %s", req.Method, req.URL, TokenURL)
					}
					if got := req.Header.Get("User-Agent"); got != tc.want {
						t.Errorf("User-Agent = %q, want %q", got, tc.want)
					}
					if err := req.ParseForm(); err != nil {
						t.Errorf("parse token form: %v", err)
					}
					if got := req.PostForm.Get("grant_type"); got != flow {
						t.Errorf("grant_type = %q, want %q", got, flow)
					}
					if flow == "authorization_code" {
						if req.PostForm.Get("code") != "test-code" || req.PostForm.Get("code_verifier") != "test-verifier" || req.PostForm.Get("redirect_uri") != RedirectURI {
							t.Error("exchange form did not preserve code, verifier, or redirect URI")
						}
					} else if req.PostForm.Get("refresh_token") != "test-refresh" {
						t.Error("refresh form did not preserve refresh token")
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)),
						Header:     make(http.Header),
						Request:    req,
					}, nil
				})
				if flow == "authorization_code" {
					bundle, err := auth.ExchangeCodeForTokensWithRedirect(context.Background(), "test-code", RedirectURI, &PKCECodes{CodeVerifier: "test-verifier"})
					if err != nil {
						t.Fatalf("exchange: %v", err)
					}
					if bundle.TokenData.AccessToken != "new-access" {
						t.Error("unexpected exchange access token")
					}
				} else {
					tokens, err := auth.RefreshTokens(context.Background(), "test-refresh")
					if err != nil {
						t.Fatalf("refresh: %v", err)
					}
					if tokens.AccessToken != "new-access" {
						t.Error("unexpected refresh access token")
					}
				}
				if calls != 1 {
					t.Fatalf("transport calls = %d, want 1", calls)
				}
			})
		}
	}
}

func TestNewCodexAuthWithProxyURL_FallsBackToGlobalProxy(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://global.example.com:8080"}}
	auth := NewCodexAuthWithProxyURL(cfg, "")
	transport, ok := auth.httpClient.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatalf("expected transport with global proxy, got %T", auth.httpClient.Transport)
	}
	req, err := http.NewRequest(http.MethodPost, TokenURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil || proxyURL == nil || proxyURL.String() != cfg.ProxyURL {
		t.Fatalf("proxy = %v, error = %v, want %s", proxyURL, errProxy, cfg.ProxyURL)
	}
}
