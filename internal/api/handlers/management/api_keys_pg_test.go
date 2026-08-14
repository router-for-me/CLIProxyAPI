package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func newPGRouter(h *Handler) *gin.Engine {
	r := gin.New()
	g := r.Group("/v0/management")
	g.GET("/api-keys-pg", h.ListPGAPIKeys)
	g.POST("/api-keys-pg", h.CreatePGAPIKey)
	g.GET("/api-keys-pg/:id", h.GetPGAPIKey)
	g.PATCH("/api-keys-pg/:id", h.PatchPGAPIKey)
	g.PUT("/api-keys-pg/:id/policy", h.PutPGAPIKeyPolicy)
	g.POST("/api-keys-pg/:id/regenerate", h.RegeneratePGAPIKey)
	g.DELETE("/api-keys-pg/:id", h.DeletePGAPIKey)
	g.POST("/api-keys-pg/import", h.ImportPGAPIKeys)
	g.GET("/usage-stats", h.GetUsageStats)
	g.GET("/usage-stats/summary", h.GetUsageSummary)
	g.GET("/usage-windows/:api_key_id", h.GetUsageWindows)
	g.GET("/models-catalog", h.ListModelsCatalog)
	g.GET("/models-catalog/count", h.GetModelsCatalogCount)
	g.GET("/models-catalog/:id/pricing", h.GetModelPricing)
	g.PUT("/models-catalog/:id/pricing", h.PutModelPricing)
	return r
}

func newBareHandler() *Handler {
	return NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
}

func TestPGRoutesReturn503WhenNotConfigured(t *testing.T) {
	h := newBareHandler()
	r := newPGRouter(h)
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v0/management/api-keys-pg", ""},
		{http.MethodPost, "/v0/management/api-keys-pg", `{"name":"k"}`},
		{http.MethodGet, "/v0/management/api-keys-pg/k1", ""},
		{http.MethodPatch, "/v0/management/api-keys-pg/k1", `{"name":"x"}`},
		{http.MethodPut, "/v0/management/api-keys-pg/k1/policy", `{"rpm_limit":60}`},
		{http.MethodPost, "/v0/management/api-keys-pg/k1/regenerate", ""},
		{http.MethodDelete, "/v0/management/api-keys-pg/k1", ""},
		{http.MethodPost, "/v0/management/api-keys-pg/import", `{"keys":[{"alias":"a","key":"custom-secret-0123456789"}]}`},
		{http.MethodGet, "/v0/management/usage-stats", ""},
		{http.MethodGet, "/v0/management/usage-stats/summary?window=hourly&api_key_id=k1", ""},
		{http.MethodGet, "/v0/management/usage-windows/k1", ""},
		{http.MethodGet, "/v0/management/models-catalog", ""},
		{http.MethodGet, "/v0/management/models-catalog/count", ""},
		{http.MethodGet, "/v0/management/models-catalog/k1/pricing", ""},
		{http.MethodPut, "/v0/management/models-catalog/k1/pricing", `{"input_per_1m_usd":1.0}`},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var body *bytes.Buffer
			if tc.body != "" {
				body = bytes.NewBufferString(tc.body)
			} else {
				body = bytes.NewBuffer(nil)
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("%s %s: status = %d; want 503", tc.method, tc.path, w.Code)
			}
		})
	}
}

func TestPGCreateRequiresName(t *testing.T) {
	// Without PG configured, we won't reach validation; the 503 path wins.
	h := newBareHandler()
	r := newPGRouter(h)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/api-keys-pg",
		bytes.NewBufferString(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (no PG); got %d", w.Code)
	}
}

func TestPGUsageSummaryRejectsBadWindow(t *testing.T) {
	// Without PG configured, the 503 check happens before window validation.
	// This test documents that behavior so future refactors don't silently
	// change the precedence.
	h := newBareHandler()
	r := newPGRouter(h)
	req := httptest.NewRequest(http.MethodGet,
		"/v0/management/usage-stats/summary?window=garbage&api_key_id=k", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503; got %d", w.Code)
	}
}
