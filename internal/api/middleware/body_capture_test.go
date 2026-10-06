package middleware

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

type recordingSink struct{ got []BodyCaptureRequest }

func (r *recordingSink) Capture(_ context.Context, req BodyCaptureRequest) {
	r.got = append(r.got, req)
}

func TestBodyCaptureSinkSkipsGETEvenWithSink(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &recordingSink{}
	logger := logging.NewFileRequestLogger(true, t.TempDir(), "", 0)
	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(logger, sink))
	engine.GET("/skip", func(c *gin.Context) {
		c.Set(logging.StoreRequestBodiesContextKey, true)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/skip", nil))
	if len(sink.got) != 0 {
		t.Fatalf("GET must stay skipped with a sink present, got %d captures", len(sink.got))
	}
}

func TestBodyCaptureSinkCalledWhenGateSet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &recordingSink{}
	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(nil, sink))
	engine.POST("/x", func(c *gin.Context) {
		c.Set(logging.StoreRequestBodiesContextKey, true)
		c.Set(logging.StoreRequestBodiesProviderContextKey, "claude")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	if len(sink.got) != 1 {
		t.Fatalf("want 1 capture, got %d", len(sink.got))
	}
	if sink.got[0].Provider != "claude" {
		t.Fatalf("provider = %q", sink.got[0].Provider)
	}
}

func TestBodyCaptureSinkNotCalledWhenGateAbsent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &recordingSink{}
	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(nil, sink))
	engine.POST("/y", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/y", nil))
	if len(sink.got) != 0 {
		t.Fatalf("expected no capture, got %d", len(sink.got))
	}
}

func TestBodyCaptureSinkCapturesBodiesAndAttributes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &recordingSink{}
	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(nil, sink))
	engine.POST("/capture", func(c *gin.Context) {
		c.Set(logging.StoreRequestBodiesContextKey, true)
		c.Set(logging.StoreRequestBodiesProviderContextKey, " openai ")
		c.Set(logging.StoreRequestBodiesUpstreamIDContextKey, int64(42))
		c.Set("API_REQUEST", []byte("upstream-req"))
		c.Set("API_RESPONSE", []byte("upstream-resp"))
		c.JSON(http.StatusOK, gin.H{"hello": "world"})
	})
	request := httptest.NewRequest(http.MethodPost, "/capture", strings.NewReader(`{"input":"hi"}`))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(httptest.NewRecorder(), request)

	if len(sink.got) != 1 {
		t.Fatalf("want 1 capture, got %d", len(sink.got))
	}
	got := sink.got[0]
	if got.Provider != "openai" {
		t.Fatalf("provider = %q, want %q", got.Provider, "openai")
	}
	if got.UpstreamProviderID != 42 {
		t.Fatalf("upstream id = %d, want 42", got.UpstreamProviderID)
	}
	if !strings.Contains(string(got.ClientRequestBody), `"input":"hi"`) {
		t.Fatalf("client request body = %q", got.ClientRequestBody)
	}
	if !strings.Contains(string(got.ClientResponseBody), "world") {
		t.Fatalf("client response body = %q", got.ClientResponseBody)
	}
	if string(got.UpstreamRequest) != "upstream-req" {
		t.Fatalf("upstream request = %q", got.UpstreamRequest)
	}
	if string(got.UpstreamResponse) != "upstream-resp" {
		t.Fatalf("upstream response = %q", got.UpstreamResponse)
	}
	if got.Truncated {
		t.Fatal("unexpected truncation for small payload")
	}
}

func TestBodyCaptureSinkTeesStreamingResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &recordingSink{}
	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(nil, sink))
	engine.POST("/stream", func(c *gin.Context) {
		c.Set(logging.StoreRequestBodiesContextKey, true)
		c.Set(logging.StoreRequestBodiesProviderContextKey, "claude")
		c.Header("Content-Type", "text/event-stream")
		c.Writer.WriteHeader(http.StatusOK)
		if _, errWrite := c.Writer.Write([]byte("data: one\n\n")); errWrite != nil {
			t.Fatalf("write chunk 1: %v", errWrite)
		}
		if _, errWrite := c.Writer.Write([]byte("data: two\n\n")); errWrite != nil {
			t.Fatalf("write chunk 2: %v", errWrite)
		}
	})
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/stream", nil))

	if len(sink.got) != 1 {
		t.Fatalf("want 1 capture, got %d", len(sink.got))
	}
	body := string(sink.got[0].ClientResponseBody)
	if !strings.Contains(body, "data: one") || !strings.Contains(body, "data: two") {
		t.Fatalf("streamed capture = %q", body)
	}
}

func TestBodyCaptureSinkBoundsStreamingCapture(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &recordingSink{}
	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(nil, sink))
	chunk := bytes.Repeat([]byte("s"), 1024)
	chunks := (bodyCaptureSectionMaxBytes / len(chunk)) + 8
	engine.POST("/stream-big", func(c *gin.Context) {
		c.Set(logging.StoreRequestBodiesContextKey, true)
		c.Set(logging.StoreRequestBodiesProviderContextKey, "claude")
		c.Header("Content-Type", "text/event-stream")
		c.Writer.WriteHeader(http.StatusOK)
		for i := 0; i < chunks; i++ {
			if _, errWrite := c.Writer.Write(chunk); errWrite != nil {
				t.Fatalf("write chunk %d: %v", i, errWrite)
			}
		}
	})
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/stream-big", nil))

	if rec.Body.Len() != len(chunk)*chunks {
		t.Fatalf("client bytes = %d, want %d", rec.Body.Len(), len(chunk)*chunks)
	}
	if len(sink.got) != 1 {
		t.Fatalf("want 1 capture, got %d", len(sink.got))
	}
	got := sink.got[0]
	if len(got.ClientResponseBody) > bodyCaptureSectionMaxBytes {
		t.Fatalf("captured %d stream bytes, want <= %d", len(got.ClientResponseBody), bodyCaptureSectionMaxBytes)
	}
	if !got.Truncated {
		t.Fatal("expected truncation for oversized stream")
	}
}

func TestBodyCaptureStreamingDoesNotBufferBody(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w := NewResponseWriterWrapper(c.Writer, nil, &RequestInfo{})
	w.captureEnabled = func() bool { return true }
	w.isStreaming = true
	w.capturedStreamBuf = &bytes.Buffer{}

	if _, errWrite := w.Write([]byte("chunk")); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	if w.body.Len() != 0 {
		t.Fatalf("streaming wrote %d bytes into w.body, want 0", w.body.Len())
	}
	if w.capturedStreamBuf.Len() != len("chunk") {
		t.Fatalf("tee captured %d bytes, want %d", w.capturedStreamBuf.Len(), len("chunk"))
	}
}

func TestBodyCaptureBoundsNonStreamingResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	sink := &recordingSink{}
	w := NewResponseWriterWrapper(c.Writer, nil, &RequestInfo{RequestID: "r1"})
	w.bodySink = sink
	w.captureEnabled = func() bool { return true }
	// Mirrors RequestLoggingMiddleware with the file logger disabled: without
	// the bounded tee this path would accumulate the full response in w.body.
	w.logOnErrorOnly = true

	payload := bytes.Repeat([]byte("x"), bodyCaptureSectionMaxBytes+4096)
	if _, errWrite := w.Write(payload); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	if w.body.Len() != 0 {
		t.Fatalf("non-streaming capture buffered %d bytes into w.body; want 0", w.body.Len())
	}
	if w.capturedResponseBuf == nil {
		t.Fatal("capturedResponseBuf was not populated")
	}
	if w.capturedResponseBuf.buf.Len() != bodyCaptureSectionMaxBytes {
		t.Fatalf("bounded tee holds %d bytes, want %d", w.capturedResponseBuf.buf.Len(), bodyCaptureSectionMaxBytes)
	}

	if errFinalize := w.Finalize(c); errFinalize != nil {
		t.Fatalf("finalize: %v", errFinalize)
	}
	if len(sink.got) != 1 {
		t.Fatalf("want 1 capture, got %d", len(sink.got))
	}
	got := sink.got[0]
	if len(got.ClientResponseBody) != bodyCaptureSectionMaxBytes {
		t.Fatalf("captured %d response bytes, want %d", len(got.ClientResponseBody), bodyCaptureSectionMaxBytes)
	}
	if !got.Truncated {
		t.Fatal("expected Truncated for oversized non-streaming response")
	}
}

func TestBodyCaptureSinkTruncatesOversizedClientBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &recordingSink{}
	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(nil, sink))
	engine.POST("/big", func(c *gin.Context) {
		c.Set(logging.StoreRequestBodiesContextKey, true)
		c.Set(logging.StoreRequestBodiesProviderContextKey, "openai")
		if _, errCopy := io.Copy(io.Discard, c.Request.Body); errCopy != nil {
			t.Fatalf("drain body: %v", errCopy)
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	payload := bytes.Repeat([]byte("a"), bodyCaptureSectionMaxBytes+4096)
	request := httptest.NewRequest(http.MethodPost, "/big", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(httptest.NewRecorder(), request)

	if len(sink.got) != 1 {
		t.Fatalf("want 1 capture, got %d", len(sink.got))
	}
	got := sink.got[0]
	if len(got.ClientRequestBody) != bodyCaptureSectionMaxBytes {
		t.Fatalf("captured %d request bytes, want %d", len(got.ClientRequestBody), bodyCaptureSectionMaxBytes)
	}
	if !got.Truncated {
		t.Fatal("expected Truncated for oversized client body")
	}
}

func TestBodyCaptureSinkAutoCapturesFailedRequestWithoutGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &recordingSink{}
	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(nil, sink))
	// No STORE_REQUEST_BODIES gate is set: capture is OFF for this provider.
	engine.POST("/fail", func(c *gin.Context) {
		c.Set(logging.StoreRequestBodiesProviderContextKey, "openai")
		c.Set(logging.StoreRequestBodiesUpstreamIDContextKey, int64(7))
		c.Set("API_REQUEST", []byte("upstream-req"))
		c.Set("API_RESPONSE", []byte("upstream-err"))
		c.JSON(http.StatusBadGateway, gin.H{"error": "boom"})
	})
	request := httptest.NewRequest(http.MethodPost, "/fail", strings.NewReader(`{"input":"hi"}`))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(httptest.NewRecorder(), request)

	if len(sink.got) != 1 {
		t.Fatalf("failed request must auto-capture, got %d captures", len(sink.got))
	}
	got := sink.got[0]
	if got.Provider != "openai" {
		t.Fatalf("provider = %q, want %q", got.Provider, "openai")
	}
	if got.UpstreamProviderID != 7 {
		t.Fatalf("upstream id = %d, want 7", got.UpstreamProviderID)
	}
	if string(got.UpstreamResponse) != "upstream-err" {
		t.Fatalf("upstream response = %q", got.UpstreamResponse)
	}
	if !strings.Contains(string(got.ClientRequestBody), `"input":"hi"`) {
		t.Fatalf("client request body = %q", got.ClientRequestBody)
	}
}

func TestBodyCaptureSinkAutoCapturesAPIResponseErrorOn2xx(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &recordingSink{}
	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(nil, sink))
	// A stream can terminate with an API_RESPONSE_ERROR while the HTTP status
	// is still 200 (e.g. an SSE error frame); that must auto-capture too.
	engine.POST("/stream-fail", func(c *gin.Context) {
		c.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{{StatusCode: http.StatusBadGateway, Error: errors.New("upstream error")}})
		c.Set(logging.StoreRequestBodiesProviderContextKey, "claude")
		c.JSON(http.StatusOK, gin.H{"ok": false})
	})
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/stream-fail", nil))

	if len(sink.got) != 1 {
		t.Fatalf("API_RESPONSE_ERROR request must auto-capture, got %d captures", len(sink.got))
	}
}

func TestBodyCaptureSinkSkipsSuccessfulRequestWithoutGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &recordingSink{}
	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(nil, sink))
	engine.POST("/ok", func(c *gin.Context) {
		c.Set(logging.StoreRequestBodiesProviderContextKey, "openai")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/ok", nil))

	if len(sink.got) != 0 {
		t.Fatalf("successful request without the opt-in gate must not capture, got %d", len(sink.got))
	}
}
