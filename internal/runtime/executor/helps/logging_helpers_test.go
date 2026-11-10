package helps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestRequestLoggingDoesNotMarkUpstreamAttempt(t *testing.T) {
	tests := []struct {
		name   string
		record func(context.Context)
	}{
		{
			name: "HTTP",
			record: func(ctx context.Context) {
				RecordAPIRequest(ctx, &config.Config{}, UpstreamRequestLog{URL: "https://api.example.com", Method: http.MethodPost})
			},
		},
		{
			name: "websocket",
			record: func(ctx context.Context) {
				RecordAPIWebsocketRequest(ctx, &config.Config{}, UpstreamRequestLog{URL: "wss://api.example.com", Method: "WEBSOCKET"})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := cliproxyexecutor.WithUpstreamAttemptTracker(context.Background())
			test.record(ctx)
			if cliproxyexecutor.UpstreamAttempted(ctx) {
				t.Fatal("request logging marked an upstream attempt before transport")
			}
		})
	}
}

func TestRecordAPIRequestClonesDeferredBodyWhenRequestLogDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	body := []byte(`{"model":"original"}`)

	RecordAPIRequest(ctx, &config.Config{}, UpstreamRequestLog{
		URL:    "https://api.example.com/v1/responses",
		Method: http.MethodPost,
		Body:   body,
	})
	body[10] = 'X'

	value, exists := ginCtx.Get(logging.DeferredAPIRequestContextKey)
	if !exists {
		t.Fatal("deferred API request was not captured")
	}
	requests, ok := value.([]logging.DeferredAPIRequest)
	if !ok || len(requests) != 1 {
		t.Fatalf("deferred API requests = %#v, want one request", value)
	}
	captured := string(requests[0]())
	if !strings.Contains(captured, `{"model":"original"}`) {
		t.Fatalf("captured API request = %q, want original body", captured)
	}
}

func TestRecordAPIRequestDeferredCapturePolicy(t *testing.T) {
	for _, tt := range []struct {
		name      string
		disabled  bool
		fullLog   bool
		wantDefer bool
	}{
		{name: "HTTP with upgrade headers", wantDefer: true},
		{name: "established WebSocket", disabled: true},
		{name: "WebSocket with full logging", disabled: true, fullLog: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			ginCtx.Request.Header.Set("Connection", "Upgrade")
			ginCtx.Request.Header.Set("Upgrade", "websocket")
			ctx := context.WithValue(context.Background(), "gin", ginCtx)
			if tt.disabled {
				ctx = logging.WithDeferredAPIRequestCaptureDisabled(ctx)
			}
			cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: tt.fullLog}}
			RecordAPIRequest(ctx, cfg, UpstreamRequestLog{Method: http.MethodPost, Body: []byte("request-payload")})
			value, exists := ginCtx.Get(logging.DeferredAPIRequestContextKey)
			if exists != tt.wantDefer {
				t.Fatalf("deferred capture present = %t, want %t", exists, tt.wantDefer)
			}
			if tt.wantDefer {
				requests, _ := value.([]logging.DeferredAPIRequest)
				if len(requests) != 1 || !strings.Contains(string(requests[0]()), "request-payload") {
					t.Fatal("ordinary HTTP lost its error-only request body")
				}
			}
			if tt.fullLog {
				logged, _ := ginCtx.Get(apiRequestKey)
				if body, _ := logged.([]byte); !strings.Contains(string(body), "request-payload") {
					t.Fatal("full logging lost the upstream request body")
				}
			}
		})
	}
}

func TestRecordAPIRequestDisabledCaptureDoesNotRetainLargeTurns(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := logging.WithDeferredAPIRequestCaptureDisabled(context.WithValue(context.Background(), "gin", ginCtx))
	body := []byte(strings.Repeat("x", 1<<20))
	for range 40 {
		RecordAPIRequest(ctx, &config.Config{}, UpstreamRequestLog{Body: body})
	}
	for _, key := range []string{logging.DeferredAPIRequestContextKey, deferredAPIRequestBytesKey} {
		if _, exists := ginCtx.Get(key); exists {
			t.Fatalf("disabled capture retained %s after 40 MiB of upstream requests", key)
		}
	}
}

func TestRecordAPIRequestAsyncCapturePolicySurvivesGinContextReuse(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	parent := context.WithValue(context.Background(), "gin", ginCtx)
	socketCtx, cancel := context.WithCancel(logging.WithDeferredAPIRequestCaptureDisabled(parent))
	cancel()
	callbackCtx := context.WithoutCancel(socketCtx)

	// A completed socket can leave asynchronous plugin callbacks holding the
	// same Gin pointer that is now serving an unrelated HTTP request.
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	RecordAPIRequest(parent, &config.Config{}, UpstreamRequestLog{Body: []byte("new-http-request")})
	var callbacks sync.WaitGroup
	for range 8 {
		callbacks.Go(func() {
			RecordAPIRequest(callbackCtx, &config.Config{}, UpstreamRequestLog{Body: []byte("old-websocket-request")})
		})
	}
	callbacks.Wait()
	value, _ := ginCtx.Get(logging.DeferredAPIRequestContextKey)
	requests, _ := value.([]logging.DeferredAPIRequest)
	if len(requests) != 1 || !strings.Contains(string(requests[0]()), "new-http-request") {
		t.Fatal("old WebSocket callbacks changed the new HTTP request's deferred log")
	}
	used, _ := ginCtx.Get(deferredAPIRequestBytesKey)
	if used != len("new-http-request") {
		t.Fatalf("deferred bytes = %v, want only the new HTTP body", used)
	}
}

func TestRecordAPIResponseMetadataStoresHeadersWhenRequestLogDisabled(t *testing.T) {
	ctx := logging.WithResponseHeadersHolder(context.Background())
	headers := http.Header{}
	headers.Add("X-Upstream-Request-Id", "upstream-req-1")

	RecordAPIResponseMetadata(ctx, &config.Config{}, http.StatusOK, headers)
	headers.Set("X-Upstream-Request-Id", "mutated")

	got := logging.GetResponseHeaders(ctx)
	if got.Get("X-Upstream-Request-Id") != "upstream-req-1" {
		t.Fatalf("response header = %q, want %q", got.Get("X-Upstream-Request-Id"), "upstream-req-1")
	}
}

func TestDeferredAPIResponseCapturedWhenRequestLogDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{}

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{
		URL:    "https://api.example.com/v1/chat/completions",
		Method: http.MethodPost,
		Body:   []byte(`{"model":"grok-4.7"}`),
	})
	headers := http.Header{}
	headers.Set("Content-Type", "text/event-stream")
	headers.Set("Authorization", "Bearer super-secret-upstream-token")
	RecordAPIResponseMetadata(ctx, cfg, http.StatusBadGateway, headers)
	headers.Set("Content-Type", "text/mutated")
	AppendAPIResponseChunk(ctx, cfg, []byte(`data: {"type":"message.start"}`))
	AppendAPIResponseChunk(ctx, cfg, []byte(`data: {"type":"message.delta"}`))
	RecordAPIResponseError(ctx, cfg, errors.New("stream disconnected"))

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{
		URL:    "https://api.example.com/v1/chat/completions",
		Method: http.MethodPost,
		Body:   []byte(`{"model":"grok-4.7"}`),
	})
	RecordAPIResponseError(ctx, cfg, errors.New("retry failed"))

	value, exists := ginCtx.Get(logging.DeferredAPIResponseContextKey)
	if !exists {
		t.Fatal("deferred API response was not captured")
	}
	deferred, ok := value.(logging.DeferredAPIResponse)
	if !ok || deferred == nil {
		t.Fatalf("deferred API response = %#v, want logging.DeferredAPIResponse", value)
	}
	rendered := string(deferred.RenderAPIResponse())
	for _, want := range []string{
		"=== API RESPONSE 1 ===",
		"Status: 502",
		"Content-Type: text/event-stream",
		`data: {"type":"message.start"}` + "\n" + `data: {"type":"message.delta"}`,
		"Error: stream disconnected",
		"Error: stream disconnected\n\n=== API RESPONSE 2 ===",
		"Error: retry failed",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("deferred API response missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "super-secret-upstream-token") {
		t.Fatalf("deferred API response leaked the sensitive header value:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Authorization: Bearer supe...oken") {
		t.Fatalf("deferred API response lost the masked authorization header:\n%s", rendered)
	}

	requestValue, _ := ginCtx.Get(logging.DeferredAPIRequestContextKey)
	requests, _ := requestValue.([]logging.DeferredAPIRequest)
	if len(requests) != 2 {
		t.Fatalf("deferred API requests = %d, want 2", len(requests))
	}
	for i, want := range []string{"=== API REQUEST 1 ===", "=== API REQUEST 2 ==="} {
		if !strings.Contains(string(requests[i]()), want) {
			t.Fatalf("deferred API request %d missing %q", i+1, want)
		}
	}
}

func TestDeferredAPIResponseBodyTruncatedWhenRequestLogDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{}

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{Method: http.MethodPost})
	RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, nil)
	chunk := []byte(strings.Repeat("a", 1024))
	for range maxDeferredAPIResponseBodyBytes/len(chunk) + 2 {
		AppendAPIResponseChunk(ctx, cfg, chunk)
	}

	capture := deferredResponseCaptureFrom(ginCtx)
	if capture == nil {
		t.Fatal("deferred API response capture is missing")
	}
	if len(capture.attempts) != 1 {
		t.Fatalf("deferred response attempts = %d, want 1", len(capture.attempts))
	}
	attempt := capture.attempts[0]
	if len(attempt.body) > maxDeferredAPIResponseBodyBytes {
		t.Fatalf("deferred response body = %d bytes, want at most %d", len(attempt.body), maxDeferredAPIResponseBodyBytes)
	}
	if !attempt.bodyTruncated {
		t.Fatal("deferred response body was not marked as truncated")
	}
	rendered := string(capture.RenderAPIResponse())
	if !strings.Contains(rendered, fmt.Sprintf("[API RESPONSE BODY TRUNCATED: captured first %d bytes]", len(attempt.body))) {
		t.Fatalf("deferred API response missing truncation marker:\n%s", rendered)
	}
	if !bytes.HasPrefix(attempt.body, chunk) {
		t.Fatal("deferred response body does not start with the first upstream chunk")
	}
}

func TestDeferredAPIResponseSkippedForFullLoggingAndDisabledPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("full request logging uses the regular capture", func(t *testing.T) {
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx := context.WithValue(context.Background(), "gin", ginCtx)
		cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}

		RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, http.Header{"Content-Type": []string{"text/event-stream"}})
		AppendAPIResponseChunk(ctx, cfg, []byte(`data: {"type":"message.start"}`))
		RecordAPIResponseError(ctx, cfg, errors.New("EOF"))

		if _, exists := ginCtx.Get(logging.DeferredAPIResponseContextKey); exists {
			t.Fatal("full logging must not retain a deferred API response")
		}
		value, exists := ginCtx.Get(apiResponseKey)
		if !exists {
			t.Fatal("API_RESPONSE was not captured")
		}
		response, _ := value.([]byte)
		for _, want := range []string{"Status: 200", `data: {"type":"message.start"}`, "Error: EOF"} {
			if !strings.Contains(string(response), want) {
				t.Fatalf("API response missing %q:\n%s", want, string(response))
			}
		}
	})

	t.Run("disabled error-only capture retains nothing", func(t *testing.T) {
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx := logging.WithDeferredAPIRequestCaptureDisabled(context.WithValue(context.Background(), "gin", ginCtx))
		cfg := &config.Config{}

		RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, http.Header{"Content-Type": []string{"text/event-stream"}})
		AppendAPIResponseChunk(ctx, cfg, []byte(`data: {"type":"message.start"}`))
		RecordAPIResponseError(ctx, cfg, errors.New("EOF"))

		for _, key := range []string{logging.DeferredAPIResponseContextKey, apiResponseKey} {
			if _, exists := ginCtx.Get(key); exists {
				t.Fatalf("disabled capture retained %s", key)
			}
		}
	})
}

func TestAPIResponseAttemptsAreSeparated(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		fileBacked        bool
		firstResponseBody []byte
	}{
		{name: "memory backed error"},
		{name: "file backed error", fileBacked: true},
		{name: "memory backed partial body", firstResponseBody: []byte("partial")},
		{name: "file backed partial body", fileBacked: true, firstResponseBody: []byte("partial")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(recorder)
			var responseSource *logging.FileBodySource
			if tt.fileBacked {
				var errSource error
				responseSource, errSource = logging.NewFileBodySourceInDir(t.TempDir(), "api-response")
				if errSource != nil {
					t.Fatalf("NewFileBodySourceInDir: %v", errSource)
				}
				t.Cleanup(func() {
					if errCleanup := responseSource.Cleanup(); errCleanup != nil {
						t.Errorf("Cleanup: %v", errCleanup)
					}
				})
				ginCtx.Set(logging.APIResponseSourceContextKey, responseSource)
			}

			ctx := context.WithValue(context.Background(), "gin", ginCtx)
			cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}
			RecordAPIRequest(ctx, cfg, UpstreamRequestLog{URL: "https://api.example.com/first", Method: http.MethodPost})
			if len(tt.firstResponseBody) > 0 {
				AppendAPIResponseChunk(ctx, cfg, tt.firstResponseBody)
			} else {
				RecordAPIResponseError(ctx, cfg, errors.New("EOF"))
			}
			RecordAPIRequest(ctx, cfg, UpstreamRequestLog{URL: "https://api.example.com/second", Method: http.MethodPost})
			RecordAPIResponseError(ctx, cfg, errors.New("retry failed"))

			var response []byte
			if responseSource != nil {
				var errBytes error
				response, errBytes = responseSource.Bytes()
				if errBytes != nil {
					t.Fatalf("responseSource.Bytes: %v", errBytes)
				}
			} else {
				value, exists := ginCtx.Get(apiResponseKey)
				if !exists {
					t.Fatal("API_RESPONSE was not captured")
				}
				response, _ = value.([]byte)
			}

			previousEnd := "Error: EOF"
			if len(tt.firstResponseBody) > 0 {
				previousEnd = string(tt.firstResponseBody)
			}
			wantBoundary := previousEnd + "\n\n=== API RESPONSE 2 ==="
			if !strings.Contains(string(response), wantBoundary) {
				t.Fatalf("API response attempts are not separated by one blank line:\n%q\nwant boundary %q", response, wantBoundary)
			}
		})
	}
}
