package helps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
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

func TestDeferredAPIResponseCapturedAndAlignedAcrossAttempts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{}

	// Attempt 1: request recorded, then aborted before any upstream response arrives.
	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{
		URL:    "https://api.example.com/v1/responses",
		Method: http.MethodPost,
		Body:   []byte(`{"attempt":1}`),
	})
	capture := deferredResponseCaptureFrom(ginCtx)
	if capture.AttemptCount() != 1 || !capture.AttemptCapturedAt(0).IsZero() {
		t.Fatalf("attempt 1 capturedAt = %v, want zero before response arrives", capture.AttemptCapturedAt(0))
	}
	if capture.HasPayload() {
		t.Fatal("HasPayload = true before any response details were recorded")
	}

	// Attempt 2: response metadata, SSE chunks, and read error recorded.
	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{
		URL:    "https://api.example.com/v1/responses",
		Method: http.MethodPost,
		Body:   []byte(`{"attempt":2}`),
	})
	if !capture.AttemptCapturedAt(1).IsZero() {
		t.Fatalf("attempt 2 capturedAt = %v, want zero before response event", capture.AttemptCapturedAt(1))
	}
	headers := http.Header{
		"Content-Type":  []string{"text/event-stream"},
		"Authorization": []string{"Bearer super-secret-upstream-token"},
		"Set-Cookie":    []string{"__cf_bm=super-secret-cf-cookie"},
	}
	RecordAPIResponseMetadata(ctx, cfg, http.StatusBadGateway, headers)
	headers.Set("Content-Type", "text/mutated")
	if capture.AttemptCapturedAt(1).IsZero() {
		t.Fatal("attempt 2 capturedAt was not stamped on first response event")
	}
	AppendAPIResponseChunk(ctx, cfg, []byte("event: response.created"))
	AppendAPIResponseChunk(ctx, cfg, []byte(`data: {"type":"response.created"}`))
	RecordAPIResponseError(ctx, cfg, errors.New("stream disconnected"))

	// Attempt 3: transport dial error with no HTTP response metadata.
	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{
		URL:    "https://api.example.com/v1/responses",
		Method: http.MethodPost,
		Body:   []byte(`{"attempt":3}`),
	})
	RecordAPIResponseError(ctx, cfg, errors.New("dial tcp timeout"))

	value, exists := ginCtx.Get(logging.DeferredAPIResponseContextKey)
	if !exists {
		t.Fatal("deferred API response was not stored in Gin context")
	}
	deferred, ok := value.(logging.DeferredAPIResponse)
	if !ok || deferred == nil || !deferred.HasPayload() {
		t.Fatalf("deferred API response = %#v, want populated DeferredAPIResponse", value)
	}

	rendered := string(deferred.RenderAPIResponse())
	if strings.Contains(rendered, "=== API RESPONSE 1 ===") {
		t.Fatalf("attempt 1 had no response content and should not render:\n%s", rendered)
	}
	for _, want := range []string{
		"=== API RESPONSE 2 ===",
		"Status: 502",
		"Authorization: Bearer supe...oken",
		"Content-Type: text/event-stream",
		"event: response.created\ndata: {\"type\":\"response.created\"}",
		"Error: stream disconnected\n\n=== API RESPONSE 3 ===",
		"Error: dial tcp timeout",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered deferred response missing %q:\n%s", want, rendered)
		}
	}
	for _, secret := range []string{"super-secret-upstream-token", "super-secret-cf-cookie"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("rendered deferred response leaked secret %q:\n%s", secret, rendered)
		}
	}
	attempt3Section := rendered[strings.Index(rendered, "=== API RESPONSE 3 ==="):]
	if strings.Contains(attempt3Section, "Headers:") {
		t.Fatalf("dial error without HTTP response should not fabricate Headers section:\n%s", attempt3Section)
	}
}

func TestDeferredAPIResponseBodyTruncatedAndBoundedAcrossAttempts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{}

	// Pass a 2 MiB slice to verify truncation copies into a bounded buffer rather
	// than retaining the multi-megabyte backing array.
	largePayload := bytes.Repeat([]byte("a"), 2<<20)
	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{Method: http.MethodPost})
	RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, nil)
	AppendAPIResponseChunk(ctx, cfg, largePayload)

	capture := deferredResponseCaptureFrom(ginCtx)
	attempt1Len, attempt1Cap, attempt1Truncated := capture.AttemptBodyStats(0)
	if attempt1Len != maxDeferredAPIResponseBodyBytes {
		t.Fatalf("attempt 1 body len = %d, want %d", attempt1Len, maxDeferredAPIResponseBodyBytes)
	}
	if attempt1Cap > maxDeferredAPIResponseBodyBytes*2 {
		t.Fatalf("attempt 1 body cap = %d, retained oversized backing array", attempt1Cap)
	}
	if !attempt1Truncated {
		t.Fatal("attempt 1 bodyTruncated = false, want true")
	}

	maxAttempts := maxTotalDeferredAPIResponseBodyBytes/maxDeferredAPIResponseBodyBytes + 2
	for i := 1; i < maxAttempts; i++ {
		RecordAPIRequest(ctx, cfg, UpstreamRequestLog{Method: http.MethodPost})
		RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, nil)
		AppendAPIResponseChunk(ctx, cfg, largePayload)
	}
	if capture.TotalResponseBodyBytes() > maxTotalDeferredAPIResponseBodyBytes {
		t.Fatalf("total deferred response bytes = %d, want <= %d", capture.TotalResponseBodyBytes(), maxTotalDeferredAPIResponseBodyBytes)
	}
	lastLen, _, lastTruncated := capture.AttemptBodyStats(capture.AttemptCount() - 1)
	if lastLen != 0 || !lastTruncated {
		t.Fatalf("exhausted budget attempt len=%d truncated=%t, want len=0 truncated=true", lastLen, lastTruncated)
	}

	rendered := string(capture.RenderAPIResponse())
	wantMarker := fmt.Sprintf("[API RESPONSE BODY TRUNCATED: captured first %d bytes]", maxDeferredAPIResponseBodyBytes)
	if !strings.Contains(rendered, wantMarker) {
		t.Fatalf("rendered response missing truncation marker %q", wantMarker)
	}
}

func TestDeferredAPIResponseBoundsLargeErrorsHeadersAndRetries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{}

	giantErr := errors.New(strings.Repeat("e", 2<<20))
	giantHeaders := http.Header{
		"X-Giant-Header": []string{strings.Repeat("h", 256<<10)},
	}

	for range 24 {
		RecordAPIRequest(ctx, cfg, UpstreamRequestLog{Method: http.MethodPost})
		RecordAPIResponseMetadata(ctx, cfg, http.StatusBadGateway, giantHeaders)
		RecordAPIResponseError(ctx, cfg, giantErr)
	}

	capture := deferredResponseCaptureFrom(ginCtx)
	rendered := string(capture.RenderAPIResponse())
	if len(rendered) > maxTotalDeferredAPIResponseBodyBytes+(128<<10) {
		t.Fatalf("rendered deferred response len = %d, exceeded bounded budget", len(rendered))
	}
	if !strings.Contains(rendered, "[API RESPONSE ERROR TRUNCATED:") {
		t.Fatalf("rendered deferred response missing error truncation marker:\n%.512s", rendered)
	}

	t.Run("hundred thousand short headers do not allocate unbounded header map", func(t *testing.T) {
		manyHeaders := make(http.Header, 100_000)
		for i := range 100_000 {
			manyHeaders[fmt.Sprintf("X-Short-%06d", i)] = []string{"1"}
		}
		hdrGinCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		hdrCtx := context.WithValue(context.Background(), "gin", hdrGinCtx)
		RecordAPIRequest(hdrCtx, cfg, UpstreamRequestLog{Method: http.MethodPost})

		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		RecordAPIResponseMetadata(hdrCtx, cfg, http.StatusBadGateway, manyHeaders)
		runtime.ReadMemStats(&after)

		allocated := after.TotalAlloc - before.TotalAlloc
		if allocated > 512<<10 {
			t.Fatalf("RecordAPIResponseMetadata allocated %d bytes for 100k short headers, want <= %d", allocated, 512<<10)
		}
		hdrRendered := string(deferredResponseCaptureFrom(hdrGinCtx).RenderAPIResponse())
		if !strings.Contains(hdrRendered, "[API RESPONSE HEADERS TRUNCATED]") {
			t.Fatalf("expected header truncation marker:\n%s", hdrRendered)
		}
	})

	t.Run("sixty-five short headers retain first sixty-four sorted entries", func(t *testing.T) {
		sixtyFive := make(http.Header, 65)
		for i := range 65 {
			sixtyFive[fmt.Sprintf("X-Cap-%02d", i)] = []string{fmt.Sprintf("val-%02d", i)}
		}
		capGinCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		capCtx := context.WithValue(context.Background(), "gin", capGinCtx)
		RecordAPIRequest(capCtx, cfg, UpstreamRequestLog{Method: http.MethodPost})
		RecordAPIResponseMetadata(capCtx, cfg, http.StatusBadGateway, sixtyFive)

		capRendered := string(deferredResponseCaptureFrom(capGinCtx).RenderAPIResponse())
		for i := range 64 {
			wantLine := fmt.Sprintf("X-Cap-%02d: val-%02d", i, i)
			if !strings.Contains(capRendered, wantLine) {
				t.Fatalf("rendered headers dropped retained entry %d (%q):\n%s", i, wantLine, capRendered)
			}
		}
		if strings.Contains(capRendered, "X-Cap-64:") {
			t.Fatalf("rendered headers unexpectedly retained 65th entry:\n%s", capRendered)
		}
		if !strings.Contains(capRendered, "[API RESPONSE HEADERS TRUNCATED]") {
			t.Fatalf("rendered headers missing truncation marker:\n%s", capRendered)
		}
	})
}

func TestDeferredAPIResponseSkippedWhenFullLogCommercialOrDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("full request logging uses regular capture only", func(t *testing.T) {
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx := context.WithValue(context.Background(), "gin", ginCtx)
		cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}

		RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, http.Header{"Content-Type": []string{"text/event-stream"}})
		AppendAPIResponseChunk(ctx, cfg, []byte(`data: {"type":"response.created"}`))
		RecordAPIResponseError(ctx, cfg, errors.New("EOF"))

		if _, exists := ginCtx.Get(logging.DeferredAPIResponseContextKey); exists {
			t.Fatal("full logging must not populate DeferredAPIResponseContextKey")
		}
		if _, exists := ginCtx.Get(apiResponseKey); !exists {
			t.Fatal("expected regular API_RESPONSE capture")
		}
	})

	t.Run("commercial mode retains nothing", func(t *testing.T) {
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx := context.WithValue(context.Background(), "gin", ginCtx)
		cfg := &config.Config{CommercialMode: true}

		RecordAPIResponseMetadata(ctx, cfg, http.StatusBadGateway, http.Header{"X-Test": []string{"1"}})
		AppendAPIResponseChunk(ctx, cfg, []byte("upstream error"))
		RecordAPIResponseError(ctx, cfg, errors.New("boom"))
		RecordAPIWebsocketUpgradeRejection(ctx, cfg, UpstreamRequestLog{URL: "https://example.com"}, http.StatusForbidden, nil, []byte("denied"))

		for _, key := range []string{logging.DeferredAPIRequestContextKey, logging.DeferredAPIResponseContextKey, apiResponseKey} {
			if _, exists := ginCtx.Get(key); exists {
				t.Fatalf("commercial mode retained %s", key)
			}
		}
	})

	t.Run("disabled policy survives Gin context reuse", func(t *testing.T) {
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		parent := context.WithValue(context.Background(), "gin", ginCtx)
		socketCtx, cancel := context.WithCancel(logging.WithDeferredAPIRequestCaptureDisabled(parent))
		cancel()
		callbackCtx := context.WithoutCancel(socketCtx)

		RecordAPIRequest(parent, &config.Config{}, UpstreamRequestLog{Body: []byte("new-http-request")})
		RecordAPIResponseMetadata(parent, &config.Config{}, http.StatusBadGateway, nil)

		var callbacks sync.WaitGroup
		for range 8 {
			callbacks.Go(func() {
				RecordAPIResponseMetadata(callbackCtx, &config.Config{}, http.StatusInternalServerError, nil)
				AppendAPIResponseChunk(callbackCtx, &config.Config{}, []byte("stale-websocket-chunk"))
				RecordAPIResponseError(callbackCtx, &config.Config{}, errors.New("stale-websocket-error"))
				RecordAPIWebsocketUpgradeRejection(callbackCtx, &config.Config{}, UpstreamRequestLog{URL: "https://stale.invalid"}, http.StatusForbidden, nil, []byte("stale"))
			})
		}
		callbacks.Wait()

		rendered := string(deferredResponseCaptureFrom(ginCtx).RenderAPIResponse())
		if strings.Contains(rendered, "stale") {
			t.Fatalf("disabled async callbacks polluted reused Gin context:\n%s", rendered)
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
