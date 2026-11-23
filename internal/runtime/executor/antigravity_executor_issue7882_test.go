package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	internalcache "github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func newIssue7882PooledConfig(tosPauseWindow string, enableCredits bool) *config.Config {
	enabled := true
	cfg := &config.Config{
		QuotaExceeded: config.QuotaExceeded{
			AntigravityCredits: enableCredits,
		},
		Antigravity: config.AntigravityConfig{
			TOSPauseWindow: tosPauseWindow,
			ConnectionPool: config.AntigravityConnectionPoolConfig{
				Enabled: &enabled,
			},
		},
	}
	return cfg
}

func newIssue7882Auth(id, baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:         id,
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": baseURL},
		Metadata: map[string]any{
			"access_token":  "token-" + id,
			"refresh_token": "refresh-" + id,
			"project_id":    "project-1",
			"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
}

func TestAntigravityIssue7882_ExecuteInBandErrorPayloads(t *testing.T) {
	reqPayload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	openaiPayload := []byte(`{"model":"gemini-4-argon","messages":[{"role":"user","content":"hi"}]}`)

	t.Run("HTTP 200 in-band 403 TOS_VIOLATION surfaces 403, arms TOS pause, preserves credits failure state, and evicts idle transports", func(t *testing.T) {
		formats := []struct {
			name    string
			format  sdktranslator.Format
			payload []byte
		}{
			{name: "antigravity", format: sdktranslator.FormatAntigravity, payload: reqPayload},
			{name: "openai", format: sdktranslator.FormatOpenAI, payload: openaiPayload},
		}
		for _, tc := range formats {
			t.Run(tc.name, func(t *testing.T) {
				resetAntigravityCreditsRetryState()
				t.Cleanup(resetAntigravityCreditsRetryState)

				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.ReadAll(r.Body)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(antigravityTOSBody))
				}))
				defer server.Close()

				cfg := newIssue7882PooledConfig("6h", true)
				exec := NewAntigravityExecutor(cfg)
				auth := newIssue7882Auth("issue7882-exec-200-tos-"+tc.name, server.URL)
				t.Cleanup(func() {
					closeAntigravityAuthIdleTransports(auth)
					cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
				})

				markAntigravityCreditsPermanentlyDisabled(auth)
				ctx := cliproxyauth.WithAntigravityCredits(context.Background())

				_, errExec := exec.Execute(ctx, auth, cliproxyexecutor.Request{
					Model:   "gemini-4-argon",
					Payload: tc.payload,
				}, cliproxyexecutor.Options{
					SourceFormat:   tc.format,
					ResponseFormat: tc.format,
				})

				var sErr statusErr
				if !errors.As(errExec, &sErr) {
					t.Fatalf("Execute() err = %#v, want statusErr", errExec)
				}
				if sErr.StatusCode() != http.StatusForbidden {
					t.Fatalf("StatusCode() = %d, want %d (err = %v)", sErr.StatusCode(), http.StatusForbidden, errExec)
				}
				if !sErr.IsTerminalTOSViolation() || !sErr.IsCredentialScoped() {
					t.Fatalf("TOS statusErr flags = (tos=%v, credScoped=%v), want (true, true)", sErr.IsTerminalTOSViolation(), sErr.IsCredentialScoped())
				}
				if !antigravityInTOSPause(cfg, auth) {
					t.Fatal("expected pool-wide TOS pause to be armed on HTTP 200 in-band 403 TOS_VIOLATION")
				}
				if _, loaded := antigravityCreditsFailureByAuth.Load(auth.ID); !loaded {
					t.Fatal("HTTP 200 in-band 403 TOS_VIOLATION must not clear prior credits failure state")
				}
				if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
					t.Fatal("expected idle transports for auth to be evicted on HTTP 200 in-band 403 TOS_VIOLATION")
				}
			})
		}
	})

	t.Run("HTTP 200 in-band 403 VALIDATION_REQUIRED surfaces credential-scoped 403 and evicts idle transports", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(antigravityValidationRequiredBody))
		}))
		defer server.Close()

		cfg := newIssue7882PooledConfig("", false)
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7882Auth("issue7882-exec-200-validation", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-4-carbon",
			Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})

		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.StatusCode() != http.StatusForbidden || !sErr.IsCredentialScoped() || sErr.IsTerminalTOSViolation() {
			t.Fatalf("Execute() err = %#v, want credential-scoped non-TOS 403 statusErr", errExec)
		}
		if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports for auth to be evicted on HTTP 200 in-band 403 VALIDATION_REQUIRED")
		}
	})

	t.Run("HTTP 200 in-band 429 RATE_LIMIT_EXCEEDED records short cooldown, preserves credits failure state, honors Retry-After, and evicts idle transports", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(antigravityRateLimitNoRetryInfoBody))
		}))
		defer server.Close()

		cfg := newIssue7882PooledConfig("", true)
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7882Auth("issue7882-exec-200-429-short", server.URL)
		t.Cleanup(func() {
			closeAntigravityAuthIdleTransports(auth)
			cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
		})

		markAntigravityCreditsPermanentlyDisabled(auth)
		ctx := cliproxyauth.WithAntigravityCredits(context.Background())

		model := "gemini-4-hydrogen"
		_, errExec := exec.Execute(ctx, auth, cliproxyexecutor.Request{
			Model:   model,
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})

		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("Execute() err = %#v, want 429 statusErr", errExec)
		}
		if sErr.RetryAfter() == nil || *sErr.RetryAfter() != 45*time.Second {
			t.Fatalf("RetryAfter() = %v, want 45s", sErr.RetryAfter())
		}
		inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, model, time.Now())
		if errCooldown != nil || !inCooldown || remaining <= 0 || remaining > 45*time.Second {
			t.Fatalf("antigravityIsInShortCooldownRequired() = (%v, %v, %v), want (true, (0, 45s], nil)", inCooldown, remaining, errCooldown)
		}
		if _, loaded := antigravityCreditsFailureByAuth.Load(auth.ID); !loaded {
			t.Fatal("HTTP 200 in-band 429 must not clear prior credits failure state")
		}
		if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports for auth to be evicted on HTTP 200 in-band 429")
		}
	})

	t.Run("HTTP 200 in-band 429 INSUFFICIENT_G1_CREDITS_BALANCE permanently disables credits and evicts idle transports", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		creditsExhaustedBody := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"INSUFFICIENT_G1_CREDITS_BALANCE"}]}}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(creditsExhaustedBody))
		}))
		defer server.Close()

		cfg := newIssue7882PooledConfig("", true)
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7882Auth("issue7882-exec-200-429-credits", server.URL)
		t.Cleanup(func() {
			closeAntigravityAuthIdleTransports(auth)
			cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
		})

		ctx := cliproxyauth.WithAntigravityCredits(context.Background())
		_, errExec := exec.Execute(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-4-neon",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})

		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("Execute() err = %#v, want 429 statusErr", errExec)
		}
		rawState, loaded := antigravityCreditsFailureByAuth.Load(auth.ID)
		if !loaded {
			t.Fatal("expected auth to be marked permanently disabled in antigravityCreditsFailureByAuth")
		}
		state, okState := rawState.(antigravityCreditsFailureState)
		if !okState || !state.PermanentlyDisabled || !state.ExplicitBalanceExhausted {
			t.Fatalf("credits failure state = %+v, want PermanentlyDisabled=true ExplicitBalanceExhausted=true", state)
		}
		if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports for auth to be evicted on HTTP 200 in-band 429 credits exhaustion")
		}
	})

	t.Run("HTTP 200 in-band 401 UNAUTHENTICATED surfaces credential-scoped 401 and evicts idle transports", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		body401 := `{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body401))
		}))
		defer server.Close()

		cfg := newIssue7882PooledConfig("", false)
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7882Auth("issue7882-exec-200-401", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-4-argon",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})

		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.StatusCode() != http.StatusUnauthorized || !sErr.IsCredentialScoped() {
			t.Fatalf("Execute() err = %#v, want credential-scoped 401 statusErr", errExec)
		}
		if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports for auth to be evicted on HTTP 200 in-band 401")
		}
	})

	t.Run("HTTP 200 in-band 400 invalid thought signature clears cached reasoning replay", func(t *testing.T) {
		internalcache.ClearAntigravityReasoningReplayCache()
		t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

		model := "gemini-3-flash-agent"
		sessionKey := "session:issue7882-invalid-sig"
		bad := []byte(`{"type":"thought_signature","thoughtSignature":"INVALID_REPLAY_SIGNATURE_ISSUE7882_XXXX","contentIndex":1,"partIndex":0}`)
		if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, [][]byte{bad}) {
			t.Fatal("failed to seed replay cache")
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid thoughtSignature in model content","code":400}}`))
		}))
		defer server.Close()

		cfg := newIssue7882PooledConfig("", false)
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7882Auth("issue7882-exec-200-invalid-sig", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		payload := []byte(`{"sessionId":"issue7882-invalid-sig","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"functionCall":{"id":"id1","name":"Bash","args":{}}}]},{"role":"model","parts":[{"functionResponse":{"id":"id1","name":"Bash","response":{"result":"ok"}}}]}]}}`)
		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   model,
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})

		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.StatusCode() != http.StatusBadRequest {
			t.Fatalf("Execute() err = %#v, want 400 statusErr", errExec)
		}
		if _, ok, errGet := internalcache.GetAntigravityReasoningReplayItemsRequired(context.Background(), model, sessionKey); errGet != nil {
			t.Fatalf("get after clear: %v", errGet)
		} else if ok {
			t.Fatal("HTTP 200 in-band 400 invalid signature should clear cached replay item")
		}
	})

	t.Run("HTTP 200 in-band out-of-range error code normalizes to 502 and preserves credits failure state", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"error":{"code":0,"message":"internal gateway error"}}`))
		}))
		defer server.Close()

		cfg := newIssue7882PooledConfig("", true)
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7882Auth("issue7882-exec-200-badcode", server.URL)
		t.Cleanup(func() {
			closeAntigravityAuthIdleTransports(auth)
			cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
		})

		markAntigravityCreditsPermanentlyDisabled(auth)
		ctx := cliproxyauth.WithAntigravityCredits(context.Background())

		_, errExec := exec.Execute(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-4-argon",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})

		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.StatusCode() != http.StatusBadGateway {
			t.Fatalf("Execute() err = %#v, want 502 statusErr", errExec)
		}
		if _, loaded := antigravityCreditsFailureByAuth.Load(auth.ID); !loaded {
			t.Fatal("HTTP 200 in-band error must not clear prior credits failure state")
		}
	})

	t.Run("HTTP 200 with null error succeeds and clears prior credits failure state", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"error":null,"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"null-err-ok"}]},"finishReason":"STOP"}]}}`))
		}))
		defer server.Close()

		cfg := newIssue7882PooledConfig("", true)
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7882Auth("issue7882-exec-200-null-err", server.URL)
		t.Cleanup(func() {
			closeAntigravityAuthIdleTransports(auth)
			cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
		})

		markAntigravityCreditsPermanentlyDisabled(auth)
		ctx := cliproxyauth.WithAntigravityCredits(context.Background())

		resp, errExec := exec.Execute(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-4-argon",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		if errExec != nil {
			t.Fatalf("Execute() error = %v, want nil", errExec)
		}
		if len(resp.Payload) == 0 {
			t.Fatal("Execute() returned empty payload")
		}
		if _, loaded := antigravityCreditsFailureByAuth.Load(auth.ID); loaded {
			t.Fatal("confirmed successful credits request should clear prior credits failure state")
		}
	})

	t.Run("HTTP 200 zero-output does not prematurely clear prior credits failure state", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`))
		}))
		defer server.Close()

		cfg := newIssue7882PooledConfig("", true)
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7882Auth("issue7882-exec-200-zero-output", server.URL)
		t.Cleanup(func() {
			closeAntigravityAuthIdleTransports(auth)
			cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
		})

		markAntigravityCreditsPermanentlyDisabled(auth)
		ctx := cliproxyauth.WithAntigravityCredits(context.Background())

		_, errExec := exec.Execute(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-4-argon",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.StatusCode() != http.StatusBadGateway {
			t.Fatalf("Execute() err = %#v, want 502 zero-output statusErr", errExec)
		}
		if _, loaded := antigravityCreditsFailureByAuth.Load(auth.ID); !loaded {
			t.Fatal("zero-output failure must not clear prior credits failure state")
		}
	})
}

func TestAntigravityIssue7882_EvictsIdleConnectionsOnMidStream403And401(t *testing.T) {
	reqPayload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	okSSE := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}}\n\n"
	plain403SSE := `{"error":{"code":403,"message":"Forbidden","status":"PERMISSION_DENIED"}}`
	tos403SSE := `{"error":{"code":403,"message":"TOS suspended","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION"}]}}`
	unauth401SSE := `{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`

	partialSSE := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]}}]}}` + "\n\n"
	cases := []struct {
		name             string
		errFrame         string
		wantCode         int
		wantCred         bool
		emitPartialFirst bool
	}{
		{name: "mid-stream_401", errFrame: unauth401SSE, wantCode: http.StatusUnauthorized, wantCred: true},
		{name: "mid-stream_401_after_partial", errFrame: unauth401SSE, wantCode: http.StatusUnauthorized, wantCred: true, emitPartialFirst: true},
		{name: "mid-stream_403_plain", errFrame: plain403SSE, wantCode: http.StatusForbidden, wantCred: false},
		{name: "mid-stream_403_tos", errFrame: tos403SSE, wantCode: http.StatusForbidden, wantCred: true},
		{name: "mid-stream_403_tos_after_partial", errFrame: tos403SSE, wantCode: http.StatusForbidden, wantCred: true, emitPartialFirst: true},
	}

	for _, mode := range []string{"stream", "claude_nonstream"} {
		for _, tc := range cases {
			t.Run(mode+"_"+tc.name, func(t *testing.T) {
				resetAntigravityCreditsRetryState()
				t.Cleanup(resetAntigravityCreditsRetryState)

				var mu sync.Mutex
				var callNum int
				var addrs []string

				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.ReadAll(r.Body)
					mu.Lock()
					callNum++
					n := callNum
					addrs = append(addrs, r.RemoteAddr)
					mu.Unlock()

					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					if n == 2 {
						if tc.emitPartialFirst {
							_, _ = w.Write([]byte(partialSSE))
							if flusher, okFlush := w.(http.Flusher); okFlush {
								flusher.Flush()
							}
						}
						_, _ = w.Write([]byte("data: " + tc.errFrame + "\n\n"))
						return
					}
					_, _ = w.Write([]byte(okSSE))
				}))
				defer server.Close()

				cfg := newIssue7882PooledConfig("", false)
				exec := NewAntigravityExecutor(cfg)
				auth := newIssue7882Auth("issue7882-"+mode+"-"+tc.name, server.URL)
				t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

				runOnce := func() error {
					req := cliproxyexecutor.Request{
						Model:   "claude-sonnet-4-6",
						Payload: reqPayload,
					}
					opts := cliproxyexecutor.Options{
						SourceFormat:   sdktranslator.FormatAntigravity,
						ResponseFormat: sdktranslator.FormatAntigravity,
					}
					if mode == "stream" {
						res, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
						if errStream != nil {
							return errStream
						}
						var streamErr error
						for chunk := range res.Chunks {
							if chunk.Err != nil {
								streamErr = chunk.Err
							}
						}
						return streamErr
					}
					_, errExec := exec.Execute(context.Background(), auth, req, opts)
					return errExec
				}

				// 1. Warm up the per-auth HTTP/1.1 keep-alive connection.
				if errWarm := runOnce(); errWarm != nil {
					t.Fatalf("warm-up call failed: %v", errWarm)
				}
				if !antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
					t.Fatal("expected transport to be cached after warm-up")
				}

				// 2. Trigger mid-stream 403/401 error on the pooled connection.
				errMid := runOnce()
				var sErr statusErr
				if !errors.As(errMid, &sErr) || sErr.StatusCode() != tc.wantCode {
					t.Fatalf("mid-stream err = %#v, want %d statusErr", errMid, tc.wantCode)
				}
				if sErr.IsCredentialScoped() != tc.wantCred {
					t.Fatalf("IsCredentialScoped() = %v, want %v", sErr.IsCredentialScoped(), tc.wantCred)
				}
				if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
					t.Fatalf("expected idle transports for auth to be evicted after %s %s", mode, tc.name)
				}

				// 3. Follow-up call must dial a fresh TCP connection rather than reusing the evicted one.
				if errProbe := runOnce(); errProbe != nil {
					t.Fatalf("probe call failed: %v", errProbe)
				}

				mu.Lock()
				gotAddrs := append([]string(nil), addrs...)
				mu.Unlock()
				if len(gotAddrs) != 3 {
					t.Fatalf("recorded %d requests, want 3", len(gotAddrs))
				}
				if gotAddrs[0] != gotAddrs[1] {
					t.Fatalf("expected call 2 to reuse warm keep-alive connection (%s != %s)", gotAddrs[0], gotAddrs[1])
				}
				if gotAddrs[2] == gotAddrs[1] {
					t.Fatalf("expected call 3 after %s to dial a fresh connection, but reused %s", tc.name, gotAddrs[1])
				}
			})
		}
	}
}

func TestAntigravityIssue7882_EvictsIdleTransportsOnHTTP401AcrossPaths(t *testing.T) {
	reqPayload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	body401 := `{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`

	paths := []string{"execute", "claude_nonstream", "stream", "count_tokens"}
	for _, pathName := range paths {
		t.Run(pathName, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(body401))
			}))
			defer server.Close()

			cfg := newIssue7882PooledConfig("", false)
			exec := NewAntigravityExecutor(cfg)
			auth := newIssue7882Auth("issue7882-http401-"+pathName, server.URL)
			t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

			var gotErr error
			switch pathName {
			case "execute":
				_, gotErr = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "gemini-4-argon",
					Payload: reqPayload,
				}, cliproxyexecutor.Options{
					SourceFormat:   sdktranslator.FormatAntigravity,
					ResponseFormat: sdktranslator.FormatAntigravity,
				})
			case "claude_nonstream":
				_, gotErr = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "claude-sonnet-4-6",
					Payload: reqPayload,
				}, cliproxyexecutor.Options{
					SourceFormat:   sdktranslator.FormatAntigravity,
					ResponseFormat: sdktranslator.FormatAntigravity,
				})
			case "stream":
				res, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "gemini-4-argon",
					Payload: reqPayload,
				}, cliproxyexecutor.Options{
					SourceFormat:   sdktranslator.FormatAntigravity,
					ResponseFormat: sdktranslator.FormatAntigravity,
				})
				if errStream != nil {
					gotErr = errStream
				} else if res != nil {
					for chunk := range res.Chunks {
						if chunk.Err != nil {
							gotErr = chunk.Err
						}
					}
				}
			case "count_tokens":
				_, gotErr = exec.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "gemini-4-argon",
					Payload: reqPayload,
				}, cliproxyexecutor.Options{
					SourceFormat:   sdktranslator.FormatAntigravity,
					ResponseFormat: sdktranslator.FormatAntigravity,
				})
			}

			var sErr statusErr
			if !errors.As(gotErr, &sErr) || sErr.StatusCode() != http.StatusUnauthorized || !sErr.IsCredentialScoped() {
				t.Fatalf("%s HTTP 401 err = %#v, want credential-scoped 401 statusErr", pathName, gotErr)
			}
			if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
				t.Fatalf("expected idle transports for auth to be evicted on %s HTTP 401", pathName)
			}
		})
	}
}
