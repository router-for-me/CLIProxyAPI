package logging

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func TestGinLogrusLogger_PreservesRequestIDWhenOutputDiscarded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(GinLogrusLogger())

	var ctxRequestID string
	var ginRequestID string
	engine.GET("/v1/responses", func(c *gin.Context) {
		ctxRequestID = GetRequestID(c.Request.Context())
		ginRequestID = GetGinRequestID(c)
		c.Status(http.StatusOK)
	})

	prevOutput := log.StandardLogger().Out
	prevLevel := log.StandardLogger().Level
	log.SetOutput(io.Discard)
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() {
		log.SetOutput(prevOutput)
		log.SetLevel(prevLevel)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/responses?q=test", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ctxRequestID == "" {
		t.Fatal("expected request context to keep request ID when logger output is discarded")
	}
	if ginRequestID == "" {
		t.Fatal("expected gin context to keep request ID when logger output is discarded")
	}
}

func BenchmarkGinLogrusLoggerDiscardedOutput(b *testing.B) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(GinLogrusLogger())
	engine.POST("/v1/responses", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	prevOutput := log.StandardLogger().Out
	prevLevel := log.StandardLogger().Level
	log.SetOutput(io.Discard)
	log.SetLevel(log.InfoLevel)
	b.Cleanup(func() {
		log.SetOutput(prevOutput)
		log.SetLevel(prevLevel)
	})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses?foo=bar", nil)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	}
}
