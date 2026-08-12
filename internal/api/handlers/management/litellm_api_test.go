package management

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestRequireLiteLLMRuntimeUnwired verifies the compat-route guard reports
// "unwired" (writes a 503 pg_store_not_configured response and returns false)
// when the runtime PG stores backing the /litellm routes are absent.
func TestRequireLiteLLMRuntimeUnwired(t *testing.T) {
	h := &Handler{} // nil stores
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/litellm/guard", nil)
	if h.requireLiteLLMRuntime(c) {
		t.Fatal("expected requireLiteLLMRuntime to report unwired")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", rec.Code)
	}
}
