package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestAntigravityHttpRequest_EnforcesTOSPause(t *testing.T) {
	t.Run("short-circuits direct HttpRequest without dialing upstream or refreshing token", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		cfg := &config.Config{}
		cfg.Antigravity.TOSPauseWindow = "1h"
		exec := NewAntigravityExecutor(cfg)

		offendingAuth := &cliproxyauth.Auth{
			ID:       "ag-http-req-tos-offender",
			Provider: "antigravity",
			Status:   cliproxyauth.StatusActive,
		}
		siblingAuth := &cliproxyauth.Auth{
			ID:       "ag-http-req-tos-sibling",
			Provider: "antigravity",
			Status:   cliproxyauth.StatusActive,
			Attributes: map[string]string{
				"header:X-Custom-Header": "val-1",
			},
			Metadata: map[string]any{
				"access_token":  "expired-token",
				"refresh_token": "refresh-token-1",
				"project_id":    "project-1",
				"expired":       time.Now().Add(-time.Hour).Format(time.RFC3339),
			},
		}
		markAntigravityTOSPause(cfg, offendingAuth)

		var hits atomic.Int32
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
			hits.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       http.NoBody,
			}, nil
		}))

		req, errReq := http.NewRequest(http.MethodPost, antigravityBaseURLDaily+"/v1internal:fetchUserInfo", nil)
		if errReq != nil {
			t.Fatalf("http.NewRequest() error = %v", errReq)
		}
		req.Header.Set("X-Caller-Header", "keep-me")

		resp, errDo := exec.HttpRequest(ctx, siblingAuth, req)
		if resp != nil && resp.Body != nil {
			if errClose := resp.Body.Close(); errClose != nil {
				t.Errorf("close response body error: %v", errClose)
			}
		}
		if errDo == nil {
			t.Fatalf("HttpRequest() error = nil, want HTTP 503 TOS pause error (hits=%d)", hits.Load())
		}
		if hits.Load() != 0 {
			t.Fatalf("upstream hits = %d, want 0 during active TOS pause", hits.Load())
		}
		var statusErrVal cliproxyexecutor.StatusError
		if !errors.As(errDo, &statusErrVal) || statusErrVal.StatusCode() != http.StatusServiceUnavailable {
			t.Fatalf("HttpRequest() status = %v, want %d", errDo, http.StatusServiceUnavailable)
		}
		if !strings.Contains(errDo.Error(), "antigravity pool paused after TOS_VIOLATION") {
			t.Fatalf("HttpRequest() error = %q, want TOS pause message", errDo.Error())
		}
		var requestErr cliproxyexecutor.RequestScopedError
		if !errors.As(errDo, &requestErr) || !requestErr.IsRequestScoped() {
			t.Fatalf("HttpRequest() error %T is not marked RequestScopedError: %v", errDo, errDo)
		}
		if got := req.Header.Get("X-Caller-Header"); got != "keep-me" {
			t.Fatalf("caller req.Header mutated during TOS pause: X-Caller-Header = %q, want %q", got, "keep-me")
		}
	})

	t.Run("short-circuits Manager.HttpRequest during active TOS pause", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		cfg := &config.Config{}
		cfg.Antigravity.TOSPauseWindow = "1h"
		exec := NewAntigravityExecutor(cfg)
		manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
		manager.RegisterExecutor(exec)

		auth := &cliproxyauth.Auth{
			ID:       "ag-manager-http-req-tos",
			Provider: "antigravity",
			Status:   cliproxyauth.StatusActive,
			Metadata: map[string]any{
				"access_token": "token-1",
				"project_id":   "project-1",
				"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}
		markAntigravityTOSPause(cfg, auth)

		var hits atomic.Int32
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
			hits.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       http.NoBody,
			}, nil
		}))

		req, errReq := http.NewRequest(http.MethodPost, antigravityBaseURLDaily+"/v1internal:fetchUserInfo", nil)
		if errReq != nil {
			t.Fatalf("http.NewRequest() error = %v", errReq)
		}

		resp, errDo := manager.HttpRequest(ctx, auth, req)
		if resp != nil && resp.Body != nil {
			if errClose := resp.Body.Close(); errClose != nil {
				t.Errorf("close response body error: %v", errClose)
			}
		}
		if errDo == nil {
			t.Fatalf("Manager.HttpRequest() error = nil, want HTTP 503 TOS pause error (hits=%d)", hits.Load())
		}
		if hits.Load() != 0 {
			t.Fatalf("upstream hits = %d, want 0 during active TOS pause", hits.Load())
		}
		var statusErrVal cliproxyexecutor.StatusError
		if !errors.As(errDo, &statusErrVal) || statusErrVal.StatusCode() != http.StatusServiceUnavailable {
			t.Fatalf("Manager.HttpRequest() status = %v, want %d", errDo, http.StatusServiceUnavailable)
		}
	})
}

func TestAntigravityHttpRequest_SetsHostAndCustomAuthHeaders(t *testing.T) {
	parsedURL, errParse := url.Parse(antigravityBaseURLDaily + "/v1internal:fetchUserInfo")
	if errParse != nil {
		t.Fatalf("url.Parse() error = %v", errParse)
	}

	newAuth := func(attrs map[string]string) *cliproxyauth.Auth {
		return &cliproxyauth.Auth{
			ID:         "ag-http-req-headers-auth",
			Provider:   "antigravity",
			Status:     cliproxyauth.StatusActive,
			Attributes: attrs,
			Metadata: map[string]any{
				"access_token": "token-1",
				"project_id":   "project-1",
				"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
	}

	t.Run("normalizes mismatched Host from URL and applies static custom headers without mutating caller headers", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		exec := NewAntigravityExecutor(&config.Config{})
		auth := newAuth(map[string]string{
			"header:X-Custom-Header": "val-1",
		})

		var gotHost string
		var gotHeaders http.Header
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			gotHost = r.Host
			gotHeaders = r.Header.Clone()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       http.NoBody,
			}, nil
		}))

		req := &http.Request{
			Method: http.MethodPost,
			URL:    parsedURL,
			Host:   "proxy.local:8317",
			Header: http.Header{
				"Content-Type":     {"application/json"},
				"X-Leaked-Inbound": {"secret"},
			},
		}

		resp, errDo := exec.HttpRequest(ctx, auth, req)
		if errDo != nil {
			t.Fatalf("HttpRequest() error = %v", errDo)
		}
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatalf("close response body error: %v", errClose)
		}

		if gotHost != parsedURL.Host {
			t.Fatalf("outgoing req.Host = %q, want %q", gotHost, parsedURL.Host)
		}
		if got := gotHeaders.Get("X-Custom-Header"); got != "val-1" {
			t.Fatalf("outgoing X-Custom-Header = %q, want %q", got, "val-1")
		}
		if got := gotHeaders.Get("Content-Type"); got != "application/json" {
			t.Fatalf("outgoing Content-Type = %q, want %q", got, "application/json")
		}
		if got := gotHeaders.Get("Authorization"); got != "Bearer token-1" {
			t.Fatalf("outgoing Authorization = %q, want %q", got, "Bearer token-1")
		}
		if got := gotHeaders.Get("User-Agent"); got == "" {
			t.Fatal("outgoing User-Agent is empty, want Antigravity user agent")
		}
		if got := gotHeaders.Get("X-Leaked-Inbound"); got != "" {
			t.Fatalf("outgoing X-Leaked-Inbound = %q, want stripped", got)
		}
		if got := req.Header.Get("X-Leaked-Inbound"); got != "secret" {
			t.Fatalf("caller req.Header was mutated: X-Leaked-Inbound = %q, want %q", got, "secret")
		}
	})

	t.Run("sets empty Host from URL and handles nil req.Header without panic", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		exec := NewAntigravityExecutor(&config.Config{})
		auth := newAuth(nil)

		var gotHost string
		var gotAuth string
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			gotHost = r.Host
			gotAuth = r.Header.Get("Authorization")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       http.NoBody,
			}, nil
		}))

		req := &http.Request{
			Method: http.MethodPost,
			URL:    parsedURL,
		}

		resp, errDo := exec.HttpRequest(ctx, auth, req)
		if errDo != nil {
			t.Fatalf("HttpRequest() error = %v", errDo)
		}
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatalf("close response body error: %v", errClose)
		}

		if gotHost != parsedURL.Host {
			t.Fatalf("outgoing req.Host = %q, want %q", gotHost, parsedURL.Host)
		}
		if gotAuth != "Bearer token-1" {
			t.Fatalf("outgoing Authorization = %q, want %q", gotAuth, "Bearer token-1")
		}
	})

	t.Run("allows custom header:Host, header:User-Agent, and dynamic headers to override defaults", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		exec := NewAntigravityExecutor(&config.Config{})
		auth := newAuth(map[string]string{
			"header:Host":              "override.example.com",
			"header:User-Agent":        "custom-ag-ua/1.0",
			"header:X-Custom-Header":   "val-2",
			"header:X-Dynamic-Trace":   "$X-Client-Trace",
			"header:X-Dynamic-Session": "$CPA-SESSION-ID",
		})

		ginReq := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		ginReq.Header.Set("X-Client-Trace", "trace-7824")
		ginCtx := &gin.Context{Request: ginReq}

		var gotHost string
		var gotHeaders http.Header
		ctx := context.WithValue(context.Background(), "gin", ginCtx)
		ctx = util.WithSessionID(ctx, "sess-7824")
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			gotHost = r.Host
			gotHeaders = r.Header.Clone()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       http.NoBody,
			}, nil
		}))

		req, errReq := http.NewRequest(http.MethodPost, parsedURL.String(), nil)
		if errReq != nil {
			t.Fatalf("http.NewRequest() error = %v", errReq)
		}

		resp, errDo := exec.HttpRequest(ctx, auth, req)
		if errDo != nil {
			t.Fatalf("HttpRequest() error = %v", errDo)
		}
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatalf("close response body error: %v", errClose)
		}

		if gotHost != "override.example.com" {
			t.Fatalf("outgoing req.Host = %q, want %q", gotHost, "override.example.com")
		}
		if got := gotHeaders.Get("Host"); got != "override.example.com" {
			t.Fatalf("outgoing Host header = %q, want %q", got, "override.example.com")
		}
		if got := gotHeaders.Get("User-Agent"); got != "custom-ag-ua/1.0" {
			t.Fatalf("outgoing User-Agent = %q, want %q", got, "custom-ag-ua/1.0")
		}
		if got := gotHeaders.Get("X-Custom-Header"); got != "val-2" {
			t.Fatalf("outgoing X-Custom-Header = %q, want %q", got, "val-2")
		}
		if got := gotHeaders.Get("X-Dynamic-Trace"); got != "trace-7824" {
			t.Fatalf("outgoing X-Dynamic-Trace = %q, want %q", got, "trace-7824")
		}
		if got := gotHeaders.Get("X-Dynamic-Session"); got != "sess-7824" {
			t.Fatalf("outgoing X-Dynamic-Session = %q, want %q", got, "sess-7824")
		}
	})

	t.Run("uses updated auth snapshot attributes after mid-request token refresh", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		exec := NewAntigravityExecutor(&config.Config{})
		manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
		manager.RegisterExecutor(exec)

		latestAuth := &cliproxyauth.Auth{
			ID:       "ag-http-req-updated-snapshot",
			Provider: "antigravity",
			Status:   cliproxyauth.StatusActive,
			Attributes: map[string]string{
				"header:X-Custom-Header": "fresh-val",
				"header:X-Fresh-Only":    "fresh-only",
				"user_agent":             "antigravity/9.9.9 fresh/custom",
			},
			Metadata: map[string]any{
				"access_token": "fresh-token",
				"project_id":   "project-1",
				"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		registered, errRegister := manager.Register(context.Background(), latestAuth)
		if errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}

		staleAuth := registered.Clone()
		staleAuth.Attributes = map[string]string{
			"header:X-Custom-Header": "stale-val",
			"header:X-Stale-Only":    "stale-only",
			"user_agent":             "antigravity/1.0.0 stale/custom",
		}
		staleAuth.Metadata = map[string]any{
			"access_token":  "stale-token",
			"refresh_token": "refresh-token-1",
			"project_id":    "project-1",
			"expired":       time.Now().Add(-time.Hour).Format(time.RFC3339),
		}

		var gotHeaders http.Header
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			gotHeaders = r.Header.Clone()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       http.NoBody,
			}, nil
		}))

		req, errReq := http.NewRequest(http.MethodPost, parsedURL.String(), nil)
		if errReq != nil {
			t.Fatalf("http.NewRequest() error = %v", errReq)
		}

		resp, errDo := manager.HttpRequest(ctx, staleAuth, req)
		if errDo != nil {
			t.Fatalf("Manager.HttpRequest() error = %v", errDo)
		}
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatalf("close response body error: %v", errClose)
		}

		if got := gotHeaders.Get("Authorization"); got != "Bearer fresh-token" {
			t.Fatalf("outgoing Authorization = %q, want %q", got, "Bearer fresh-token")
		}
		if got := gotHeaders.Get("X-Custom-Header"); got != "fresh-val" {
			t.Fatalf("outgoing X-Custom-Header = %q, want %q", got, "fresh-val")
		}
		if got := gotHeaders.Get("X-Fresh-Only"); got != "fresh-only" {
			t.Fatalf("outgoing X-Fresh-Only = %q, want %q", got, "fresh-only")
		}
		if got := gotHeaders.Get("X-Stale-Only"); got != "" {
			t.Fatalf("outgoing X-Stale-Only = %q, want empty (stale snapshot header must not be sent)", got)
		}
		if got := gotHeaders.Get("User-Agent"); !strings.Contains(got, "9.9.9") {
			t.Fatalf("outgoing User-Agent = %q, want updated user agent containing 9.9.9", got)
		}
	})

	t.Run("strips credential-configured headers on cross-origin redirect across distinct origins", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		var origin1Host, origin2Host string
		var origin1Headers, origin2Headers http.Header
		var origin2Hits atomic.Int32

		origin2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin2Hits.Add(1)
			origin2Host = r.Host
			origin2Headers = r.Header.Clone()
			w.WriteHeader(http.StatusOK)
		}))
		defer origin2.Close()

		origin1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin1Host = r.Host
			origin1Headers = r.Header.Clone()
			http.Redirect(w, r, origin2.URL+"/redirected", http.StatusFound)
		}))
		defer origin1.Close()

		exec := NewAntigravityExecutor(&config.Config{})
		auth := newAuth(map[string]string{
			"header:X-Upstream-Token": "secret-upstream-token",
			"header:X-Custom-Header":  "val-secret",
			"header:Host":             "custom.origin1.example",
		})

		req, errReq := http.NewRequest(http.MethodGet, origin1.URL+"/start", nil)
		if errReq != nil {
			t.Fatalf("http.NewRequest() error = %v", errReq)
		}

		resp, errDo := exec.HttpRequest(context.Background(), auth, req)
		if errDo != nil {
			t.Fatalf("HttpRequest() error = %v", errDo)
		}
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatalf("close response body error: %v", errClose)
		}

		if origin1Host != "custom.origin1.example" {
			t.Fatalf("origin1 Host = %q, want %q", origin1Host, "custom.origin1.example")
		}
		if got := origin1Headers.Get("X-Upstream-Token"); got != "secret-upstream-token" {
			t.Fatalf("origin1 X-Upstream-Token = %q, want %q", got, "secret-upstream-token")
		}
		if got := origin1Headers.Get("Authorization"); got != "Bearer token-1" {
			t.Fatalf("origin1 Authorization = %q, want %q", got, "Bearer token-1")
		}

		if origin2Hits.Load() != 1 {
			t.Fatalf("origin2 hits = %d, want 1", origin2Hits.Load())
		}
		if got := origin2Headers.Get("X-Upstream-Token"); got != "" {
			t.Fatalf("origin2 leaked X-Upstream-Token = %q, want empty", got)
		}
		if got := origin2Headers.Get("X-Custom-Header"); got != "" {
			t.Fatalf("origin2 leaked X-Custom-Header = %q, want empty", got)
		}
		if got := origin2Headers.Get("Authorization"); got != "" {
			t.Fatalf("origin2 leaked Authorization = %q, want empty", got)
		}
		if origin2Host == "custom.origin1.example" {
			t.Fatalf("origin2 leaked custom Host = %q", origin2Host)
		}
	})

	t.Run("strips credential headers on cross-host bounce redirect while keeping same-origin redirects", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		exec := NewAntigravityExecutor(&config.Config{})
		auth := newAuth(map[string]string{
			"header:X-Upstream-Token": "secret-upstream-token",
		})

		type hopCapture struct {
			url           string
			upstreamToken string
			authorization string
		}
		var hops []hopCapture
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			hops = append(hops, hopCapture{
				url:           r.URL.String(),
				upstreamToken: r.Header.Get("X-Upstream-Token"),
				authorization: r.Header.Get("Authorization"),
			})
			switch r.URL.String() {
			case "https://origin-a.example/start":
				return &http.Response{
					StatusCode: http.StatusFound,
					Header:     http.Header{"Location": {"https://origin-a.example/same"}},
					Body:       http.NoBody,
				}, nil
			case "https://origin-a.example/same":
				return &http.Response{
					StatusCode: http.StatusFound,
					Header:     http.Header{"Location": {"https://origin-b.example/other"}},
					Body:       http.NoBody,
				}, nil
			case "https://origin-b.example/other":
				return &http.Response{
					StatusCode: http.StatusFound,
					Header:     http.Header{"Location": {"https://origin-a.example/bounce"}},
					Body:       http.NoBody,
				}, nil
			default:
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       http.NoBody,
				}, nil
			}
		}))

		req, errReq := http.NewRequest(http.MethodGet, "https://origin-a.example/start", nil)
		if errReq != nil {
			t.Fatalf("http.NewRequest() error = %v", errReq)
		}
		resp, errDo := exec.HttpRequest(ctx, auth, req)
		if errDo != nil {
			t.Fatalf("HttpRequest() error = %v", errDo)
		}
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatalf("close response body error: %v", errClose)
		}

		if len(hops) != 4 {
			t.Fatalf("redirect hops = %d, want 4 (%+v)", len(hops), hops)
		}
		if hops[1].upstreamToken != "secret-upstream-token" || hops[1].authorization != "Bearer token-1" {
			t.Fatalf("same-origin hop lost credential headers: %+v", hops[1])
		}
		if hops[2].upstreamToken != "" || hops[2].authorization != "" {
			t.Fatalf("cross-origin hop leaked credential headers: %+v", hops[2])
		}
		if hops[3].upstreamToken != "" || hops[3].authorization != "" {
			t.Fatalf("post-cross-origin bounce hop leaked credential headers: %+v", hops[3])
		}
	})
}
