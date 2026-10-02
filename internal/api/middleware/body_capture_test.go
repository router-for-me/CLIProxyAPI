package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

type recordingSink struct{ got []BodyCaptureRequest }

func (r *recordingSink) Capture(_ context.Context, req BodyCaptureRequest) {
	r.got = append(r.got, req)
}

func TestBodyCaptureSinkCalledWhenGateSet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &recordingSink{}
	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(nil, sink))
	engine.GET("/x", func(c *gin.Context) {
		c.Set(logging.StoreRequestBodiesContextKey, true)
		c.Set(logging.StoreRequestBodiesProviderContextKey, "claude")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
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
	engine.GET("/y", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/y", nil))
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
	engine.GET("/stream", func(c *gin.Context) {
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
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream", nil))

	if len(sink.got) != 1 {
		t.Fatalf("want 1 capture, got %d", len(sink.got))
	}
	body := string(sink.got[0].ClientResponseBody)
	if !strings.Contains(body, "data: one") || !strings.Contains(body, "data: two") {
		t.Fatalf("streamed capture = %q", body)
	}
}
