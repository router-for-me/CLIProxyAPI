package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexErrorHeadersPreserveLatestCoolingSemantics(t *testing.T) {
	for _, modelLevel := range []bool{false, true} {
		headers := http.Header{"Retry-After": {"38"}, "X-Request-Id": {"original"}}
		err := newCodexStatusErrWithHeaders(http.StatusBadRequest,
			[]byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":120}}`), headers, modelLevel)
		if err.StatusCode() != http.StatusTooManyRequests || err.IsCredentialScoped() != !modelLevel {
			t.Fatalf("new upstream cooling semantics lost: %+v", err)
		}
		if err.RetryAfter() == nil || *err.RetryAfter() != 120*time.Second {
			t.Fatal("valid body reset must win over header")
		}
		headers.Set("X-Request-Id", "changed source")
		returned := err.Headers()
		returned.Set("X-Request-Id", "changed return")
		if err.Headers().Get("X-Request-Id") != "original" {
			t.Fatal("error headers must be isolated from source and returned map mutations")
		}
	}
	for _, status := range []int{429, 408, 500, 502, 503, 504} {
		err := newCodexStatusErrWithHeaders(status, []byte(`{"detail":"Rate limit exceeded"}`), http.Header{"Retry-After": {"38"}}, false)
		if err.StatusCode() != status || err.RetryAfter() == nil || *err.RetryAfter() != 38*time.Second {
			t.Fatalf("status=%d lost retry metadata: %+v", status, err)
		}
	}
	err := newCodexStatusErrWithHeaders(429, []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":9999999999}}`), http.Header{"Retry-After": {"38"}}, false)
	if err.RetryAfter() == nil || *err.RetryAfter() != 38*time.Second {
		t.Fatal("overflowing body delay must not suppress a valid header")
	}
	if err := newCodexStatusErrWithHeaders(503, nil, nil, false); err.RetryAfter() != nil || err.Headers() != nil {
		t.Fatal("missing metadata must retain existing fallback behavior")
	}
}

func TestCodexErrorHeadersRealHTTPPaths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
		alt    string
	}{
		{"execute", false, ""}, {"compact", false, "responses/compact"}, {"stream", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "38")
				w.Header().Set("X-Request-Id", "real-http")
				w.WriteHeader(429)
				_, _ = fmt.Fprint(w, `{"detail":"Rate limit exceeded"}`)
			}))
			t.Cleanup(server.Close)
			executor := NewCodexExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "synthetic"}}
			req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"test"}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: tc.stream, Alt: tc.alt}
			var err error
			if tc.stream {
				_, err = executor.ExecuteStream(context.Background(), auth, req, opts)
			} else {
				_, err = executor.Execute(context.Background(), auth, req, opts)
			}
			headerError, ok := err.(statusErrWithHeaders)
			if !ok || headerError.RetryAfter() == nil || *headerError.RetryAfter() != 38*time.Second || headerError.Headers().Get("X-Request-Id") != "real-http" {
				t.Fatalf("headers lost through %s: %T %v", tc.name, err, err)
			}
		})
	}
}

func TestCodexWebsocketRetryHeadersPreserveConnectionLimitSignal(t *testing.T) {
	for _, tc := range []struct {
		body string
		want time.Duration
	}{
		{`{"type":"error","status":429,"headers":{"Retry-After":"38"},"error":{"message":"rate limited"}}`, 38 * time.Second},
		{`{"type":"error","status":429,"headers":{"Retry-After":"38"},"error":{"code":"websocket_connection_limit_reached"}}`, 0},
		{`{"type":"error","status":429,"headers":{"Retry-After":"38"},"error":{"type":"usage_limit_reached","resets_in_seconds":120}}`, 120 * time.Second},
	} {
		err, ok := parseCodexWebsocketError([]byte(tc.body))
		if !ok {
			t.Fatal("missing error")
		}
		timed := err.(statusErrWithHeaders)
		if timed.RetryAfter() == nil || *timed.RetryAfter() != tc.want {
			t.Fatalf("got %v want %v", timed.RetryAfter(), tc.want)
		}
	}
}

func TestCodexImageErrorHeadersRealHTTP(t *testing.T) {
	for _, model := range []string{"gpt-image-1.5", "gpt-image-2"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", model, stream), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Retry-After", "38")
					w.WriteHeader(429)
					_, _ = fmt.Fprint(w, `{"detail":"Rate limit exceeded"}`)
				}))
				t.Cleanup(server.Close)
				executor := NewCodexExecutor(&config.Config{})
				req := cliproxyexecutor.Request{Model: model, Payload: []byte(fmt.Sprintf(`{"model":%q,"prompt":"synthetic"}`, model))}
				opts := codexOpenAIImageTestOptions(codexImagesGenerationsPath, stream)
				var err error
				if stream {
					_, err = executor.ExecuteStream(context.Background(), newCodexOpenAIImageTestAuth(server.URL), req, opts)
				} else {
					_, err = executor.Execute(context.Background(), newCodexOpenAIImageTestAuth(server.URL), req, opts)
				}
				headerError, ok := err.(statusErrWithHeaders)
				if !ok || headerError.RetryAfter() == nil || *headerError.RetryAfter() != 38*time.Second {
					t.Fatalf("image error metadata lost: %T %v", err, err)
				}
			})
		}
	}
}
