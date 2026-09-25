package management

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// newInternalCallerKeyRouter wires the internal caller-key route the same way
// production does (management group, no auth middleware in the test harness).
func newInternalCallerKeyRouter(h *Handler) *gin.Engine {
	r := gin.New()
	g := r.Group("/v0/management")
	g.POST("/internal/caller-key", h.EnsureInternalCallerKey)
	return r
}

// TestInternalCallerKeyReturns503WhenNotConfigured documents that the route
// follows the standard PG-backed contract: without PGSTORE_DSN (no api_keys /
// user stores wired) it returns 503 so the dashboard can detect absence.
func TestInternalCallerKeyReturns503WhenNotConfigured(t *testing.T) {
	h := newBareHandler()
	r := newInternalCallerKeyRouter(h)

	req := httptest.NewRequest(http.MethodPost, "/v0/management/internal/caller-key", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503 (no PG store configured)", w.Code)
	}
}
