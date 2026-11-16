package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const issue7565RetryInfoBody = `{
  "error": {
    "code": 429,
    "status": "RESOURCE_EXHAUSTED",
    "message": "Resource exhausted. Please retry in 36m41.110635822s.",
    "details": [
      {
        "@type": "type.googleapis.com/google.rpc.RetryInfo",
        "retryDelay": "2190s"
      }
    ]
  }
}`

func TestGeminiInteractions429HonorsRetryAfterCooldown_Issue7565(t *testing.T) {
	cliproxyauth.SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { cliproxyauth.SetQuotaCooldownDisabled(false) })

	const model = "gemini-4-hydrogen"

	variants := []struct {
		name       string
		retryHdr   string
		body       string
		wantMinDur time.Duration
	}{
		{
			name:       "both_header_and_retry_info",
			retryHdr:   "2190",
			body:       issue7565RetryInfoBody,
			wantMinDur: 2180 * time.Second,
		},
		{
			name:       "retry_after_header_only",
			retryHdr:   "2190",
			body:       `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded."}}`,
			wantMinDur: 2180 * time.Second,
		},
		{
			name:       "retry_info_body_only",
			body:       issue7565RetryInfoBody,
			wantMinDur: 2180 * time.Second,
		},
		{
			name:       "message_please_retry_in_only",
			body:       `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded. Please retry in 36m41.110635822s."}}`,
			wantMinDur: 2190 * time.Second,
		},
	}

	modes := []struct {
		name   string
		stream bool
	}{
		{name: "non_stream", stream: false},
		{name: "stream", stream: true},
	}

	for _, variant := range variants {
		for _, mode := range modes {
			t.Run(variant.name+"_"+mode.name, func(t *testing.T) {
				var upstreamCalls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamCalls.Add(1)
					if variant.retryHdr != "" {
						w.Header().Set("Retry-After", variant.retryHdr)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(variant.body))
				}))
				defer server.Close()

				cfg := &config.Config{DisableCooling: false}
				manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(NewGeminiInteractionsExecutor(cfg))

				authID := fmt.Sprintf("issue7565-%s-%s", variant.name, mode.name)
				registry.GetGlobalRegistry().RegisterClient(authID, "gemini-interactions", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

				_, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
					ID:       authID,
					Provider: "gemini-interactions",
					Status:   cliproxyauth.StatusActive,
					Attributes: map[string]string{
						"api_key":  "synthetic-interactions-key",
						"base_url": server.URL,
					},
				})
				if errRegister != nil {
					t.Fatalf("Register() error = %v", errRegister)
				}

				req := cliproxyexecutor.Request{
					Model:   model,
					Payload: []byte(`{"model":"gemini-4-hydrogen","input":"hello"}`),
				}
				opts := cliproxyexecutor.Options{
					SourceFormat:   sdktranslator.FormatInteractions,
					ResponseFormat: sdktranslator.FormatInteractions,
					Stream:         mode.stream,
				}

				beforeCall := time.Now()
				var firstErr error
				if mode.stream {
					res, errStream := manager.ExecuteStream(context.Background(), []string{"gemini-interactions"}, req, opts)
					firstErr = errStream
					if res != nil {
						for chunk := range res.Chunks {
							if chunk.Err != nil {
								firstErr = chunk.Err
							}
						}
					}
				} else {
					_, firstErr = manager.Execute(context.Background(), []string{"gemini-interactions"}, req, opts)
				}
				if firstErr == nil {
					t.Fatal("expected HTTP 429 error on first call, got nil")
				}
				if gotCalls := upstreamCalls.Load(); gotCalls != 1 {
					t.Fatalf("upstream calls after first request = %d, want 1", gotCalls)
				}

				gotAuth, ok := manager.GetByID(authID)
				if !ok || gotAuth == nil {
					t.Fatalf("GetByID(%q) missing", authID)
				}
				state := gotAuth.ModelStates[model]
				if state == nil || !state.Unavailable || !state.Quota.Exceeded {
					t.Fatalf("model state after 429 = %+v, want unavailable quota cooldown", state)
				}
				if gotDelay := state.NextRetryAfter.Sub(beforeCall); gotDelay < variant.wantMinDur {
					t.Fatalf("NextRetryAfter delay = %v, want >= %v", gotDelay, variant.wantMinDur)
				}
				if gotRecover := state.Quota.NextRecoverAt.Sub(beforeCall); gotRecover < variant.wantMinDur {
					t.Fatalf("Quota.NextRecoverAt delay = %v, want >= %v", gotRecover, variant.wantMinDur)
				}
				if cliproxyauth.IsAuthAvailableForModel(gotAuth, model, beforeCall.Add(20*time.Minute)) {
					t.Fatal("auth unexpectedly available 20m into a 2190s cooldown")
				}
				if !cliproxyauth.IsAuthAvailableForModel(gotAuth, model, beforeCall.Add(38*time.Minute)) {
					t.Fatal("auth still blocked after 38m, want recovered")
				}

				safeRetryHdr := cliproxyauth.SafeResponseHeaders(firstErr).Get("Retry-After")
				secs, errParseHdr := strconv.Atoi(safeRetryHdr)
				if errParseHdr != nil || time.Duration(secs)*time.Second < variant.wantMinDur {
					t.Fatalf("SafeResponseHeaders Retry-After = %q, want integer seconds >= %v", safeRetryHdr, variant.wantMinDur)
				}

				// Subsequent dispatch before cooldown expiry must not hit the upstream server again.
				if mode.stream {
					_, errSecond := manager.ExecuteStream(context.Background(), []string{"gemini-interactions"}, req, opts)
					if errSecond == nil {
						t.Fatal("expected cooldown error on second stream call, got nil")
					}
				} else {
					_, errSecond := manager.Execute(context.Background(), []string{"gemini-interactions"}, req, opts)
					if errSecond == nil {
						t.Fatal("expected cooldown error on second call, got nil")
					}
				}
				if gotCalls := upstreamCalls.Load(); gotCalls != 1 {
					t.Fatalf("upstream calls after second request during cooldown = %d, want 1", gotCalls)
				}
			})
		}
	}
}

func assertRetryAfterDuration(t *testing.T, err error, want time.Duration) {
	t.Helper()
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want status 429", err)
	}
	var retryProvider interface{ RetryAfter() *time.Duration }
	if !errors.As(err, &retryProvider) || retryProvider.RetryAfter() == nil {
		t.Fatalf("error %T (%v) has nil RetryAfter(), want %v", err, err, want)
	}
	if got := *retryProvider.RetryAfter(); got != want {
		t.Fatalf("RetryAfter() = %v, want %v", got, want)
	}
}

func TestGeminiAndVertexExecutors429PropagateRetryAfter_Issue7565(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-vertex-sa-token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		w.Header().Set("Retry-After", "2190")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(issue7565RetryInfoBody))
	}))
	defer server.Close()

	targetURL, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)
	redirectCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", issue6258RedirectTransport{
		target: targetURL,
		base:   transport,
	})

	saRaw := testVertexServiceAccountJSON(t, server.URL+"/token")
	saMap := map[string]any{
		"type":           ojson.GetBytes(saRaw, "type").String(),
		"project_id":     ojson.GetBytes(saRaw, "project_id").String(),
		"private_key_id": ojson.GetBytes(saRaw, "private_key_id").String(),
		"private_key":    ojson.GetBytes(saRaw, "private_key").String(),
		"client_email":   ojson.GetBytes(saRaw, "client_email").String(),
		"token_uri":      ojson.GetBytes(saRaw, "token_uri").String(),
	}

	gemExec := NewGeminiExecutor(&config.Config{})
	gemAuth := &cliproxyauth.Auth{
		ID:         "gem-apikey",
		Provider:   "gemini",
		Attributes: map[string]string{"api_key": "k", "base_url": server.URL},
	}
	gemReq := cliproxyexecutor.Request{
		Model:   "gemini-2.5-pro",
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	}
	gemOpts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
	}

	want := 2190 * time.Second

	t.Run("gemini_execute", func(t *testing.T) {
		_, errExec := gemExec.Execute(context.Background(), gemAuth, gemReq, gemOpts)
		assertRetryAfterDuration(t, errExec, want)
	})
	t.Run("gemini_execute_stream", func(t *testing.T) {
		_, errStream := gemExec.ExecuteStream(context.Background(), gemAuth, gemReq, gemOpts)
		assertRetryAfterDuration(t, errStream, want)
	})
	t.Run("gemini_count_tokens", func(t *testing.T) {
		_, errCount := gemExec.CountTokens(context.Background(), gemAuth, gemReq, gemOpts)
		assertRetryAfterDuration(t, errCount, want)
	})

	vtxExec := NewGeminiVertexExecutor(&config.Config{})
	vtxKeyAuth := &cliproxyauth.Auth{
		ID:         "vtx-apikey",
		Provider:   "vertex",
		Attributes: map[string]string{"api_key": "vk", "base_url": server.URL, "interactions": "true"},
	}
	vtxSAAuth := &cliproxyauth.Auth{
		ID:       "vtx-sa",
		Provider: "vertex",
		Metadata: map[string]any{
			"project_id":      "proxy-test",
			"location":        "global",
			"interactions":    true,
			"service_account": saMap,
		},
	}
	vtxInterReq := cliproxyexecutor.Request{
		Model:   "gemini-4-hydrogen",
		Payload: []byte(`{"model":"gemini-4-hydrogen","input":"hi"}`),
	}
	vtxInterOpts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
	}

	t.Run("vertex_apikey_execute", func(t *testing.T) {
		_, errExec := vtxExec.Execute(context.Background(), vtxKeyAuth, gemReq, gemOpts)
		assertRetryAfterDuration(t, errExec, want)
	})
	t.Run("vertex_apikey_stream", func(t *testing.T) {
		_, errStream := vtxExec.ExecuteStream(context.Background(), vtxKeyAuth, gemReq, gemOpts)
		assertRetryAfterDuration(t, errStream, want)
	})
	t.Run("vertex_apikey_count_tokens", func(t *testing.T) {
		_, errCount := vtxExec.CountTokens(context.Background(), vtxKeyAuth, gemReq, gemOpts)
		assertRetryAfterDuration(t, errCount, want)
	})
	t.Run("vertex_sa_execute", func(t *testing.T) {
		_, errExec := vtxExec.Execute(redirectCtx, vtxSAAuth, gemReq, gemOpts)
		assertRetryAfterDuration(t, errExec, want)
	})
	t.Run("vertex_sa_stream", func(t *testing.T) {
		_, errStream := vtxExec.ExecuteStream(redirectCtx, vtxSAAuth, gemReq, gemOpts)
		assertRetryAfterDuration(t, errStream, want)
	})
	t.Run("vertex_sa_count_tokens", func(t *testing.T) {
		_, errCount := vtxExec.CountTokens(redirectCtx, vtxSAAuth, gemReq, gemOpts)
		assertRetryAfterDuration(t, errCount, want)
	})
	t.Run("vertex_interactions_execute", func(t *testing.T) {
		_, errExec := vtxExec.Execute(redirectCtx, vtxSAAuth, vtxInterReq, vtxInterOpts)
		assertRetryAfterDuration(t, errExec, want)
	})
	t.Run("vertex_interactions_stream", func(t *testing.T) {
		_, errStream := vtxExec.ExecuteStream(redirectCtx, vtxSAAuth, vtxInterReq, vtxInterOpts)
		assertRetryAfterDuration(t, errStream, want)
	})
}

func TestAntigravityZeroRetryDelayPreservesSemantics_Issue7565(t *testing.T) {
	body := []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0s"}]}}`)

	decision := decideAntigravity429(body)
	if decision.kind != antigravity429DecisionInstantRetrySameAuth {
		t.Fatalf("decideAntigravity429 kind = %v, want %v", decision.kind, antigravity429DecisionInstantRetrySameAuth)
	}
	if decision.retryAfter == nil || *decision.retryAfter != 0 {
		t.Fatalf("decideAntigravity429 retryAfter = %v, want 0s", decision.retryAfter)
	}

	sErr := newAntigravityStatusErr(http.StatusTooManyRequests, body)
	assertRetryAfterDuration(t, sErr, 0)
}
