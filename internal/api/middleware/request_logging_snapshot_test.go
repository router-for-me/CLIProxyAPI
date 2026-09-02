package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCaptureRequestInfoClonesHeaderValues(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header["X-Audit"] = []string{"before", "second"}
	c.Request = request

	info, errCapture := captureRequestInfo(c, false)
	if errCapture != nil {
		t.Fatalf("captureRequestInfo: %v", errCapture)
	}

	c.Request.Header["X-Audit"][0] = "request-mutated"
	if got := info.Headers["X-Audit"][0]; got != "before" {
		t.Fatalf("captured header changed after request mutation: got %q, want %q", got, "before")
	}

	info.Headers["X-Audit"][1] = "snapshot-mutated"
	if got := c.Request.Header["X-Audit"][1]; got != "second" {
		t.Fatalf("request header changed after snapshot mutation: got %q, want %q", got, "second")
	}
}
