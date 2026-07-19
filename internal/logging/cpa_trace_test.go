package logging

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestFormatCPATraceID(t *testing.T) {
	selectedAt := time.Date(2026, time.July, 17, 21, 58, 49, 0, time.UTC)
	got := FormatCPATraceID(selectedAt, "auth-index", "request1")
	if want := "20260717215849-auth-index-request1"; got != want {
		t.Fatalf("FormatCPATraceID() = %q, want %q", got, want)
	}

	for _, test := range []struct {
		name       string
		selectedAt time.Time
		authIndex  string
		requestID  string
	}{
		{name: "zero time", authIndex: "auth-index", requestID: "request1"},
		{name: "empty auth index", selectedAt: selectedAt, requestID: "request1"},
		{name: "empty request ID", selectedAt: selectedAt, authIndex: "auth-index"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if gotEmpty := FormatCPATraceID(test.selectedAt, test.authIndex, test.requestID); gotEmpty != "" {
				t.Fatalf("FormatCPATraceID() = %q, want empty", gotEmpty)
			}
		})
	}
}

func TestCPATraceIDMiddlewareRequiresAuthIndexBeforeResponseCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(CPATraceIDMiddleware())
	engine.GET("/selected", func(c *gin.Context) {
		SetGinRequestID(c, "1234abcd")
		SetGinCPATraceID(c, "auth-index")
		c.Status(http.StatusOK)
	})
	engine.GET("/unselected", func(c *gin.Context) {
		SetGinRequestID(c, "1234abcd")
		SetGinCPATraceID(c, "")
		c.Status(http.StatusOK)
	})
	engine.GET("/committed", func(c *gin.Context) {
		SetGinRequestID(c, "1234abcd")
		c.Writer.WriteHeaderNow()
		SetGinCPATraceID(c, "auth-index")
	})

	t.Run("writes selected auth trace", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/selected", nil))

		traceID := recorder.Header().Get(CPATraceIDHeader)
		if len(traceID) != len("20060102150405-auth-index-1234abcd") {
			t.Fatalf("trace ID = %q, unexpected length", traceID)
		}
		if got := traceID[15:]; got != "auth-index-1234abcd" {
			t.Fatalf("trace suffix = %q, want %q", got, "auth-index-1234abcd")
		}
	})

	t.Run("skips empty auth index", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/unselected", nil))

		if got := recorder.Header().Get(CPATraceIDHeader); got != "" {
			t.Fatalf("trace ID = %q, want empty", got)
		}
	})

	t.Run("skips committed response", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/committed", nil))

		if got := recorder.Header().Get(CPATraceIDHeader); got != "" {
			t.Fatalf("trace ID = %q, want empty", got)
		}
	})
}

func TestCPATraceIDConcurrentSelectionAndResponseCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(CPATraceIDMiddleware())
	engine.GET("/race", func(c *gin.Context) {
		SetGinRequestID(c, "1234abcd")
		traceCallback := GinCPATraceIDCallback(c)
		start := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			<-start
			traceCallback("auth-index")
		}()
		close(start)
		_, _ = c.Writer.Write([]byte("\n"))
		<-done
	})

	for range 100 {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/race", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
	}
}
