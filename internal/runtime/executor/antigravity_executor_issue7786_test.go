package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func newIssue7786TestAuth(id string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       id,
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token":  "token-" + id,
			"refresh_token": "refresh-" + id,
			"project_id":    "project-1",
			"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
}

type trackingLimitBody struct {
	prefix    []byte
	totalSize int
	pos       int
	bytesRead atomic.Int64
	closed    atomic.Bool
}

func (b *trackingLimitBody) Read(p []byte) (int, error) {
	if b.pos >= b.totalSize {
		return 0, io.EOF
	}
	remaining := b.totalSize - b.pos
	n := len(p)
	if n > remaining {
		n = remaining
	}
	for i := 0; i < n; i++ {
		idx := b.pos + i
		if idx < len(b.prefix) {
			p[i] = b.prefix[idx]
		} else {
			p[i] = 'a'
		}
	}
	b.pos += n
	b.bytesRead.Add(int64(n))
	return n, nil
}

func (b *trackingLimitBody) Close() error {
	b.closed.Store(true)
	return nil
}

type hookedKVClient struct {
	*fakeAntigravityKVClient
	onSet func(key string)
}

func (c *hookedKVClient) KVSet(ctx context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error) {
	if c.onSet != nil {
		c.onSet(key)
	}
	return c.fakeAntigravityKVClient.KVSet(ctx, key, value, opts)
}

func TestAntigravityIssue7786_CountTokensEnforcesShortCooldownAtEntry(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	reqPayload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)

	t.Run("blocks active short cooldown without dialing upstream", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		exec := NewAntigravityExecutor(&config.Config{})
		auth := newIssue7786TestAuth("issue7786-cooldown-block")
		if errMark := markAntigravityShortCooldownRequired(context.Background(), auth, "gemini-3-flash", time.Now(), 30*time.Second); errMark != nil {
			t.Fatalf("markAntigravityShortCooldownRequired() error = %v", errMark)
		}

		var hits atomic.Int32
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			hits.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"totalTokens":42}`)),
			}, nil
		}))

		_, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash(high)",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		if hits.Load() != 0 {
			t.Fatalf("upstream hits = %d, want 0 while auth is in short cooldown", hits.Load())
		}
		var sErr statusErr
		if !errors.As(errCount, &sErr) || sErr.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("CountTokens() err = %#v, want 429 statusErr", errCount)
		}
		if sErr.RetryAfter() == nil || *sErr.RetryAfter() <= 0 || *sErr.RetryAfter() > 30*time.Second {
			t.Fatalf("CountTokens() RetryAfter() = %v, want (0, 30s]", sErr.RetryAfter())
		}
	})

	t.Run("bypasses short cooldown when cooling is disabled", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		auth := newIssue7786TestAuth("issue7786-cooldown-disabled")
		if errMark := markAntigravityShortCooldownRequired(context.Background(), auth, "gemini-3-flash", time.Now(), 30*time.Second); errMark != nil {
			t.Fatalf("markAntigravityShortCooldownRequired() error = %v", errMark)
		}
		exec := NewAntigravityExecutor(&config.Config{DisableCooling: true})

		var hits atomic.Int32
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			hits.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"totalTokens":42}`)),
			}, nil
		}))

		if _, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		}); errCount != nil {
			t.Fatalf("CountTokens() error = %v, want nil when cooling disabled", errCount)
		}
		if hits.Load() != 1 {
			t.Fatalf("upstream hits = %d, want 1", hits.Load())
		}
	})

	t.Run("does not bypass short cooldown even when credits flag is present in context", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		auth := newIssue7786TestAuth("issue7786-cooldown-no-credits-bypass")
		cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{
			Known:           true,
			Available:       true,
			CreditAmount:    100,
			MinCreditAmount: 50,
			UpdatedAt:       time.Now(),
		})
		t.Cleanup(func() {
			cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
		})
		if errMark := markAntigravityShortCooldownRequired(context.Background(), auth, "claude-sonnet-4-6", time.Now(), 30*time.Second); errMark != nil {
			t.Fatalf("markAntigravityShortCooldownRequired() error = %v", errMark)
		}
		exec := NewAntigravityExecutor(&config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		})

		var hits atomic.Int32
		ctx := cliproxyauth.WithAntigravityCredits(context.Background())
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			hits.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"totalTokens":42}`)),
			}, nil
		}))

		_, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "claude-sonnet-4-6",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		if hits.Load() != 0 {
			t.Fatalf("upstream hits = %d, want 0 because countTokens does not enable credits", hits.Load())
		}
		var sErr statusErr
		if !errors.As(errCount, &sErr) || sErr.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("CountTokens() err = %#v, want 429 statusErr", errCount)
		}
		if sErr.RetryAfter() == nil || *sErr.RetryAfter() <= 0 || *sErr.RetryAfter() > 30*time.Second {
			t.Fatalf("CountTokens() RetryAfter() = %v, want (0, 30s]", sErr.RetryAfter())
		}
	})

	t.Run("fails closed with 503 when Home KV cooldown read fails", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		client := newFakeAntigravityKVClient()
		client.getErr = errors.New("kv read unavailable")
		useFakeAntigravityKVClient(t, client, true, nil)

		exec := NewAntigravityExecutor(&config.Config{})
		auth := newIssue7786TestAuth("issue7786-home-kv-read-err")

		var hits atomic.Int32
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			hits.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"totalTokens":42}`)),
			}, nil
		}))

		_, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		if hits.Load() != 0 {
			t.Fatalf("upstream hits = %d, want 0 when Home KV cooldown read fails", hits.Load())
		}
		var sErr statusErr
		if !errors.As(errCount, &sErr) || sErr.StatusCode() != http.StatusServiceUnavailable {
			t.Fatalf("CountTokens() err = %#v, want 503 homeKVUnavailableStatusErr", errCount)
		}
	})
}

func TestAntigravityIssue7786_CountTokens429Bookkeeping(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	reqPayload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)

	t.Run("records short cooldown on 429 RATE_LIMIT_EXCEEDED retryDelay and blocks next CountTokens", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		body429 := `{"error":{"code":429,"message":"Rate limited","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"45s"}]}}`
		var hits atomic.Int32
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			hits.Add(1)
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body429)),
			}, nil
		}))

		exec := NewAntigravityExecutor(&config.Config{})
		auth := newIssue7786TestAuth("issue7786-429-short-cooldown")

		_, errFirst := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		var sErrFirst statusErr
		if !errors.As(errFirst, &sErrFirst) || sErrFirst.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("first CountTokens() err = %#v, want 429 statusErr", errFirst)
		}
		if sErrFirst.RetryAfter() == nil || *sErrFirst.RetryAfter() != 45*time.Second {
			t.Fatalf("first CountTokens() RetryAfter() = %v, want 45s", sErrFirst.RetryAfter())
		}

		inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, "gemini-3-flash", time.Now())
		if errCooldown != nil {
			t.Fatalf("antigravityIsInShortCooldownRequired() error = %v", errCooldown)
		}
		if !inCooldown || remaining <= 0 || remaining > 45*time.Second {
			t.Fatalf("antigravityIsInShortCooldownRequired() = (%v, %v), want (true, (0, 45s])", inCooldown, remaining)
		}

		_, errSecond := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		if hits.Load() != 1 {
			t.Fatalf("upstream hits = %d, want 1 (second CountTokens must be blocked by recorded short cooldown)", hits.Load())
		}
		var sErrSecond statusErr
		if !errors.As(errSecond, &sErrSecond) || sErrSecond.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("second CountTokens() err = %#v, want 429 statusErr", errSecond)
		}
	})

	t.Run("records short cooldown on 429 RATE_LIMIT_EXCEEDED via Retry-After header", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"Retry-After":  []string{"45"},
				},
				Body: io.NopCloser(strings.NewReader(antigravityRateLimitNoRetryInfoBody)),
			}, nil
		}))

		exec := NewAntigravityExecutor(&config.Config{})
		auth := newIssue7786TestAuth("issue7786-429-header-cooldown")

		_, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		var sErr statusErr
		if !errors.As(errCount, &sErr) || sErr.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("CountTokens() err = %#v, want 429 statusErr", errCount)
		}

		inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, "gemini-3-flash", time.Now())
		if errCooldown != nil {
			t.Fatalf("antigravityIsInShortCooldownRequired() error = %v", errCooldown)
		}
		if !inCooldown || remaining <= 0 || remaining > 45*time.Second {
			t.Fatalf("antigravityIsInShortCooldownRequired() = (%v, %v), want (true, (0, 45s])", inCooldown, remaining)
		}
	})

	t.Run("does not send enabledCreditTypes or disable credits on 429 INSUFFICIENT_G1_CREDITS_BALANCE", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		var capturedReq []byte
		body429 := `{"error":{"code":429,"message":"Insufficient credits","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"INSUFFICIENT_G1_CREDITS_BALANCE"}]}}`
		ctx := cliproxyauth.WithAntigravityCredits(context.Background())
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			var errRead error
			capturedReq, errRead = io.ReadAll(r.Body)
			if errRead != nil {
				return nil, errRead
			}
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body429)),
			}, nil
		}))

		cfg := &config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		}
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7786TestAuth("issue7786-429-credits-consistent")
		cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{
			Known:           true,
			Available:       true,
			CreditAmount:    100,
			MinCreditAmount: 50,
			UpdatedAt:       time.Now(),
		})
		t.Cleanup(func() {
			cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
			closeAntigravityAuthIdleTransports(auth)
		})

		// Populate transport cache to confirm eviction on full-quota-exhausted 429.
		_ = newAntigravityHTTPClient(context.Background(), cfg, auth, 0)
		if !antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected transport to be cached before CountTokens")
		}

		_, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "claude-sonnet-4-6",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		var sErr statusErr
		if !errors.As(errCount, &sErr) || sErr.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("CountTokens() err = %#v, want 429 statusErr", errCount)
		}
		if ojson.GetBytesNoCopy(capturedReq, "enabledCreditTypes").Exists() {
			t.Fatalf("expected countTokens payload to omit enabledCreditTypes, got: %s", string(capturedReq))
		}
		if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports for auth to be evicted on 429")
		}
		if _, failed := antigravityCreditsFailureByAuth.Load(auth.ID); failed {
			t.Fatal("expected credits failure state not to be marked when countTokens did not request credits")
		}
		hint, okHint := cliproxyauth.GetAntigravityCreditsHint(auth.ID)
		if !okHint || !hint.Known || !hint.Available {
			t.Fatalf("credits hint = %#v (ok=%v), want Known=true Available=true unchanged", hint, okHint)
		}
	})

	t.Run("evicts idle transports only once on short-cooldown 429 during Home KV write", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		cfg := &config.Config{}
		auth := newIssue7786TestAuth("issue7786-single-evict-429")
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		transportKey := issue7753TransportKey(cfg, auth)
		_ = newAntigravityHTTPClient(context.Background(), cfg, auth, 0)
		if !antigravityTransports.Contains(transportKey) {
			t.Fatal("expected initial transport to be cached before CountTokens")
		}

		var evictedBeforeKVSet bool
		baseKV := newFakeAntigravityKVClient()
		hookedKV := &hookedKVClient{
			fakeAntigravityKVClient: baseKV,
			onSet: func(_ string) {
				evictedBeforeKVSet = !antigravityTransports.Contains(transportKey)
				// Simulate another concurrent model request creating a fresh pooled transport while KVSet is in flight.
				_ = newAntigravityHTTPClient(context.Background(), cfg, auth, 0)
			},
		}
		origKVClient := currentAntigravityKVClient
		currentAntigravityKVClient = func() (antigravityKVClient, bool, error) {
			return hookedKV, true, nil
		}
		t.Cleanup(func() { currentAntigravityKVClient = origKVClient })

		body429 := `{"error":{"code":429,"message":"Rate limited","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"45s"}]}}`
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body429)),
			}, nil
		}))

		exec := NewAntigravityExecutor(cfg)
		_, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		var sErr statusErr
		if !errors.As(errCount, &sErr) || sErr.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("CountTokens() err = %#v, want 429 statusErr", errCount)
		}
		if !evictedBeforeKVSet {
			t.Fatal("expected idle transports to be evicted before Home KVSet")
		}
		if !antigravityTransports.Contains(transportKey) {
			t.Fatal("expected freshly created transport during KVSet to survive (must not evict transports twice on one 429)")
		}
	})

	t.Run("fails closed with 503 when Home KV short-cooldown write fails on 429", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		client := newFakeAntigravityKVClient()
		client.setErr = errors.New("kv write unavailable")
		useFakeAntigravityKVClient(t, client, true, nil)

		body429 := `{"error":{"code":429,"message":"Rate limited","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"45s"}]}}`
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body429)),
			}, nil
		}))

		exec := NewAntigravityExecutor(&config.Config{})
		auth := newIssue7786TestAuth("issue7786-home-kv-write-err")

		_, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		var sErr statusErr
		if !errors.As(errCount, &sErr) || sErr.StatusCode() != http.StatusServiceUnavailable {
			t.Fatalf("CountTokens() err = %#v, want 503 homeKVUnavailableStatusErr", errCount)
		}
	})
}

func TestAntigravityIssue7786_CountTokensBoundsResponseBodyRead(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	reqPayload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	const oversizedTotal = 2 << 20
	const maxExpectedRead = (1 << 20) + 1

	t.Run("200 OK oversized response is bounded and rejected with 502", func(t *testing.T) {
		body := &trackingLimitBody{
			prefix:    []byte(`{"totalTokens":42,"pad":"`),
			totalSize: oversizedTotal,
		}
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       body,
			}, nil
		}))

		exec := NewAntigravityExecutor(&config.Config{})
		auth := newIssue7786TestAuth("issue7786-oversize-200")

		_, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		var sErr statusErr
		if !errors.As(errCount, &sErr) || sErr.StatusCode() != http.StatusBadGateway {
			t.Fatalf("CountTokens() err = %#v, want 502 statusErr on oversized 200 response", errCount)
		}
		if gotRead := body.bytesRead.Load(); gotRead > maxExpectedRead {
			t.Fatalf("body bytes read = %d, want <= %d", gotRead, maxExpectedRead)
		}
		if !body.closed.Load() {
			t.Fatal("expected response body to be closed")
		}
	})

	t.Run("429 oversized error response is bounded and preserves 429 Retry-After", func(t *testing.T) {
		body := &trackingLimitBody{
			prefix:    []byte(`{"error":{"code":429,"message":"`),
			totalSize: oversizedTotal,
		}
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"Retry-After":  []string{"30"},
				},
				Body: body,
			}, nil
		}))

		exec := NewAntigravityExecutor(&config.Config{})
		auth := newIssue7786TestAuth("issue7786-oversize-429")

		_, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		var sErr statusErr
		if !errors.As(errCount, &sErr) || sErr.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("CountTokens() err = %#v, want 429 statusErr", errCount)
		}
		if sErr.RetryAfter() == nil || *sErr.RetryAfter() != 30*time.Second {
			t.Fatalf("CountTokens() RetryAfter() = %v, want 30s", sErr.RetryAfter())
		}
		if gotRead := body.bytesRead.Load(); gotRead > maxExpectedRead {
			t.Fatalf("body bytes read = %d, want <= %d", gotRead, maxExpectedRead)
		}
		if !body.closed.Load() {
			t.Fatal("expected response body to be closed")
		}
	})
}

func TestAntigravityIssue7786_CountTokensSanitizesRequestSchemas(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	t.Run("Gemini request renames parametersJsonSchema and strips Draft-07 keywords", func(t *testing.T) {
		var capturedBody []byte
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			var errRead error
			capturedBody, errRead = io.ReadAll(r.Body)
			if errRead != nil {
				return nil, errRead
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"totalTokens":123}`)),
			}, nil
		}))

		geminiPayload := []byte(`{
			"contents": [{"role":"user","parts":[{"text":"check weather"}]}],
			"tools": [{
				"functionDeclarations": [{
					"name": "get_weather",
					"description": "Lookup weather",
					"parametersJsonSchema": {
						"$schema": "http://json-schema.org/draft-07/schema#",
						"type": "object",
						"additionalProperties": false,
						"properties": {
							"city": {"type": "string"}
						},
						"required": ["city"]
					}
				}]
			}],
			"generationConfig": {
				"responseMimeType": "application/json",
				"responseSchema": {
					"$schema": "http://json-schema.org/draft-07/schema#",
					"type": "object",
					"additionalProperties": false,
					"properties": {
						"summary": {"type": "string"}
					}
				}
			}
		}`)

		exec := NewAntigravityExecutor(&config.Config{})
		auth := newIssue7786TestAuth("issue7786-schema-gemini")

		if _, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: geminiPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		}); errCount != nil {
			t.Fatalf("CountTokens() error = %v", errCount)
		}

		decl := ojson.GetBytesNoCopy(capturedBody, "request.tools.0.functionDeclarations.0")
		if !decl.Exists() || decl.Get("name").String() != "get_weather" {
			t.Fatalf("expected get_weather declaration to remain present, got: %s", string(capturedBody))
		}
		if decl.Get("parametersJsonSchema").Exists() {
			t.Fatalf("expected parametersJsonSchema to be renamed to parameters, got: %s", decl.Raw)
		}
		if !decl.Get("parameters").Exists() || !strings.EqualFold(decl.Get("parameters.properties.city.type").String(), "string") {
			t.Fatalf("expected sanitized parameters with city string property, got: %s", decl.Raw)
		}
		if decl.Get("parameters.$schema").Exists() || decl.Get("parameters.additionalProperties").Exists() {
			t.Fatalf("expected Draft-07 keywords stripped from tool parameters, got: %s", decl.Get("parameters").Raw)
		}

		respSchema := ojson.GetBytesNoCopy(capturedBody, "request.generationConfig.responseSchema")
		if !respSchema.Exists() || !strings.EqualFold(respSchema.Get("properties.summary.type").String(), "string") {
			t.Fatalf("expected request.generationConfig.responseSchema with summary property to remain present, got: %s", string(capturedBody))
		}
		if respSchema.Get("$schema").Exists() {
			t.Fatalf("expected $schema stripped from responseSchema, got: %s", respSchema.Raw)
		}
	})

	t.Run("Claude request flattens multi-branch tool unions and sanitizes schemas", func(t *testing.T) {
		var capturedBody []byte
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			var errRead error
			capturedBody, errRead = io.ReadAll(r.Body)
			if errRead != nil {
				return nil, errRead
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"totalTokens":99}`)),
			}, nil
		}))

		claudePayload := []byte(`{
			"model": "claude-sonnet-4-6",
			"max_tokens": 128,
			"messages": [{"role":"user","content":"run tool"}],
			"tools": [{
				"name": "multi_union",
				"description": "Accepts string or number",
				"input_schema": {
					"$schema": "http://json-schema.org/draft-07/schema#",
					"type": "object",
					"additionalProperties": false,
					"properties": {
						"value": {
							"anyOf": [
								{"type": "string"},
								{"type": "integer"}
							]
						}
					}
				}
			}]
		}`)

		exec := NewAntigravityExecutor(&config.Config{})
		auth := newIssue7786TestAuth("issue7786-schema-claude")

		if _, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "claude-sonnet-4-6",
			Payload: claudePayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatClaude,
			ResponseFormat: sdktranslator.FormatClaude,
		}); errCount != nil {
			t.Fatalf("CountTokens() error = %v", errCount)
		}

		decl := ojson.GetBytesNoCopy(capturedBody, "request.tools.0.functionDeclarations.0")
		if !decl.Exists() || decl.Get("name").String() != "multi_union" {
			t.Fatalf("expected multi_union declaration to remain present for Claude, got: %s", string(capturedBody))
		}
		if decl.Get("parametersJsonSchema").Exists() {
			t.Fatalf("expected parametersJsonSchema to be renamed to parameters for Claude, got: %s", decl.Raw)
		}
		if !decl.Get("parameters").Exists() {
			t.Fatalf("expected sanitized parameters in Claude tool declaration, got: %s", decl.Raw)
		}
		valProp := decl.Get("parameters.properties.value")
		if !valProp.Exists() {
			t.Fatalf("expected flattened value property for Claude, got: %s", decl.Get("parameters").Raw)
		}
		if gotType := valProp.Get("type").String(); gotType != "string" && gotType != "integer" {
			t.Fatalf("expected flattened value type to be string or integer for Claude, got %q in %s", gotType, valProp.Raw)
		}
		if desc := valProp.Get("description").String(); !strings.Contains(desc, "Accepts:") {
			t.Fatalf("expected flattened value description to include Accepts branch hint, got: %s", valProp.Raw)
		}
		if decl.Get("parameters.$schema").Exists() || decl.Get("parameters.additionalProperties").Exists() {
			t.Fatalf("expected Draft-07 keywords stripped for Claude, got: %s", decl.Get("parameters").Raw)
		}
		if valProp.Get("anyOf").Exists() || valProp.Get("oneOf").Exists() {
			t.Fatalf("expected Claude multi-branch anyOf union to be flattened, got: %s", valProp.Raw)
		}
	})

	t.Run("payload config remains final semantic barrier after CountTokens schema sanitization", func(t *testing.T) {
		var capturedBody []byte
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			var errRead error
			capturedBody, errRead = io.ReadAll(r.Body)
			if errRead != nil {
				return nil, errRead
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"totalTokens":55}`)),
			}, nil
		}))

		cfg := &config.Config{
			Payload: config.PayloadConfig{
				Override: []config.PayloadRule{
					{
						Models: []config.PayloadModelRule{{Name: "gemini-3-flash", Protocol: "antigravity"}},
						Params: map[string]any{
							"tools.0.functionDeclarations.0.parameters.additionalProperties": false,
						},
					},
				},
			},
		}
		geminiPayload := []byte(`{
			"contents": [{"role":"user","parts":[{"text":"hello"}]}],
			"tools": [{
				"functionDeclarations": [{
					"name": "ping",
					"parametersJsonSchema": {
						"$schema": "http://json-schema.org/draft-07/schema#",
						"type": "object",
						"additionalProperties": true,
						"properties": {"q": {"type": "string"}}
					}
				}]
			}]
		}`)

		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7786TestAuth("issue7786-payload-barrier")

		if _, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: geminiPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		}); errCount != nil {
			t.Fatalf("CountTokens() error = %v", errCount)
		}

		decl := ojson.GetBytesNoCopy(capturedBody, "request.tools.0.functionDeclarations.0")
		if !decl.Exists() || decl.Get("name").String() != "ping" {
			t.Fatalf("expected ping declaration to remain present, got: %s", string(capturedBody))
		}
		if decl.Get("parametersJsonSchema").Exists() {
			t.Fatalf("expected parametersJsonSchema renamed before payload override, got: %s", decl.Raw)
		}
		if !decl.Get("parameters").Exists() || !strings.EqualFold(decl.Get("parameters.properties.q.type").String(), "string") {
			t.Fatalf("expected sanitized parameters with q string property, got: %s", decl.Raw)
		}
		if decl.Get("parameters.$schema").Exists() {
			t.Fatalf("expected $schema stripped before payload override, got: %s", decl.Get("parameters").Raw)
		}
		ap := decl.Get("parameters.additionalProperties")
		if !ap.Exists() || ap.Bool() {
			t.Fatalf("expected Payload.Override additionalProperties=false to survive as final semantic barrier, got: %s", decl.Get("parameters").Raw)
		}
	})
}
