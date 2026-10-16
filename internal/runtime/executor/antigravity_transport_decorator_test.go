package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type auditTestTransport struct {
	http.RoundTripper
}

func TestAntigravityDecoratorRunsAfterCredentialHTTP11PoolSelection(t *testing.T) {
	for _, proxy := range []string{"", "http://127.0.0.1:12345"} {
		t.Run("proxy_"+proxy, func(t *testing.T) {
			calls := 0
			var selected http.RoundTripper
			ctx := httptransport.WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
				calls++
				selected = next
				tr, ok := next.(*http.Transport)
				if !ok || tr.ForceAttemptHTTP2 || len(tr.TLSNextProto) != 0 || tr.TLSClientConfig == nil || len(tr.TLSClientConfig.NextProtos) != 0 {
					t.Fatal("decorator preceded HTTP/1.1 selection")
				}
				return &auditTestTransport{RoundTripper: next}
			})
			auth := &coreauth.Auth{ID: "decorator-fixture", ProxyURL: proxy}
			c := newAntigravityHTTPClient(ctx, &config.Config{}, auth, 0)
			plain := newAntigravityHTTPClient(context.Background(), &config.Config{}, auth, 0)
			if calls != 1 {
				t.Fatalf("decorator calls = %d, want 1", calls)
			}
			wrapped, ok := c.Transport.(*auditTestTransport)
			if !ok || wrapped.RoundTripper != selected || plain.Transport != selected {
				t.Fatal("pool replaced or helper decorated twice")
			}
		})
	}
}

func TestAntigravityDecoratorConcurrentIsolation(t *testing.T) {
	auth := &coreauth.Auth{ID: "decorator-race-auth"}
	cfg := &config.Config{}
	baseClient := newAntigravityHTTPClient(context.Background(), cfg, auth, 0)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ctx := httptransport.WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
				return &auditTestTransport{RoundTripper: next}
			})
			c := newAntigravityHTTPClient(ctx, cfg, auth, 0)
			wrapped, ok := c.Transport.(*auditTestTransport)
			if !ok || wrapped.RoundTripper != baseClient.Transport {
				t.Errorf("expected wrapped transport around shared pool %T, got %T", baseClient.Transport, c.Transport)
			}
		}()
		go func() {
			defer wg.Done()
			c := newAntigravityHTTPClient(context.Background(), cfg, auth, 0)
			if c.Transport != baseClient.Transport {
				t.Errorf("undecorated client transport = %p, want %p", c.Transport, baseClient.Transport)
			}
		}()
	}
	wg.Wait()
}

func TestExecutorAttemptDecoratorObservesTranslatedPayloadAndStatusWithoutRequestLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "gpt-4o-mini"}},
					Params: map[string]any{"temperature": 0.25},
				},
			},
		},
	}
	exec := NewOpenAICompatExecutor("openai-compat", cfg)
	auth := &coreauth.Auth{
		ID:       "compat-decorator-test",
		Provider: "openai-compat",
		Attributes: map[string]string{
			"base_url": srv.URL,
			"api_key":  "secret-test-key",
		},
	}

	var observedModel string
	var observedTemp float64
	var observedStatus int
	var attempts int
	ctx := httptransport.WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			if req.Body != nil {
				bodyBytes, errRead := io.ReadAll(req.Body)
				if errRead != nil {
					return nil, errRead
				}
				observedModel = gjson.GetBytes(bodyBytes, "model").String()
				observedTemp = gjson.GetBytes(bodyBytes, "temperature").Float()
				req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
			resp, errRT := next.RoundTrip(req)
			if resp != nil {
				observedStatus = resp.StatusCode
			}
			return resp, errRT
		})
	})

	resp, errExec := exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-4o-mini",
		Payload: []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	})
	if errExec != nil {
		t.Fatalf("Execute: %v", errExec)
	}
	if !strings.Contains(string(resp.Payload), `"content":"ok"`) {
		t.Fatalf("unexpected response payload: %s", string(resp.Payload))
	}
	if cfg.RequestLog {
		t.Fatal("RequestLog must remain disabled")
	}
	if attempts != 1 || observedModel != "gpt-4o-mini" || observedTemp != 0.25 || observedStatus != http.StatusOK {
		t.Fatalf("observed attempts=%d model=%q temp=%v status=%d", attempts, observedModel, observedTemp, observedStatus)
	}
}
