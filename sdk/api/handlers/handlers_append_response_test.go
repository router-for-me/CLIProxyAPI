package handlers

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAppendAPIResponse_AppendsWithNewline(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Set("API_RESPONSE", []byte("first"))

	appendAPIResponse(ginCtx, []byte("second"))

	value, exists := ginCtx.Get("API_RESPONSE")
	if !exists {
		t.Fatal("expected API_RESPONSE to be set")
	}
	got, ok := value.([]byte)
	if !ok {
		t.Fatalf("expected []byte API_RESPONSE, got %T", value)
	}
	if string(got) != "first\nsecond" {
		t.Fatalf("unexpected API_RESPONSE: %q", string(got))
	}
}
