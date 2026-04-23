package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAppendAPIResponseUsesIncrementalBuilder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	appendAPIResponse(c, []byte("first"))
	appendAPIResponse(c, []byte("second"))

	raw, exists := c.Get("API_RESPONSE")
	if !exists {
		t.Fatal("expected API_RESPONSE to be stored")
	}
	builder, ok := raw.(*strings.Builder)
	if !ok {
		t.Fatalf("API_RESPONSE type = %T, want *strings.Builder", raw)
	}
	if got := builder.String(); got != "first\nsecond" {
		t.Fatalf("API_RESPONSE = %q, want %q", got, "first\nsecond")
	}
}

func TestCurrentAPIResponseTextSupportsBuilder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	var builder strings.Builder
	builder.WriteString("streamed")
	c.Set("API_RESPONSE", &builder)

	if got := currentAPIResponseText(c); got != "streamed" {
		t.Fatalf("currentAPIResponseText() = %q, want %q", got, "streamed")
	}
}
