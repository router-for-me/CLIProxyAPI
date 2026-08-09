package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// newLiteLLMRouter registers the /v0/management/litellm/* routes against a
// bare handler so route-wiring tests can exercise the 503-not-configured path
// without a database.
func newLiteLLMRouter(h *Handler) *gin.Engine {
	r := gin.New()
	g := r.Group("/v0/management")
	g.GET("/litellm/users", h.ListLiteLLMUsers)
	g.POST("/litellm/users", h.CreateLiteLLMUser)
	g.GET("/litellm/users/:id", h.GetLiteLLMUser)
	g.PATCH("/litellm/users/:id", h.PatchLiteLLMUser)
	g.DELETE("/litellm/users/:id", h.DeleteLiteLLMUser)
	g.POST("/litellm/users/:id/reset-spend", h.ResetLiteLLMUserSpend)
	g.GET("/litellm/users/:id/keys", h.ListLiteLLMUserKeys)
	g.GET("/litellm/keys", h.ListLiteLLMKeys)
	g.POST("/litellm/keys", h.CreateLiteLLMKey)
	g.GET("/litellm/keys/:id", h.GetLiteLLMKey)
	g.PATCH("/litellm/keys/:id", h.PatchLiteLLMKey)
	g.PUT("/litellm/keys/:id/policy", h.PutLiteLLMKeyPolicy)
	g.POST("/litellm/keys/:id/regenerate", h.RegenerateLiteLLMKey)
	g.DELETE("/litellm/keys/:id", h.DeleteLiteLLMKey)
	g.GET("/litellm/settings", h.GetLiteLLMSyncSettings)
	g.PUT("/litellm/settings", h.PutLiteLLMSyncSettings)
	g.POST("/litellm/sync/run", h.RunLiteLLMSync)
	return r
}

// TestLiteLLMRoutesReturn503WhenNotConfigured verifies every /litellm/* route
// is wired to a handler and returns 503 (pg_store_not_configured) when the
// Manage-LiteLLM stores are absent — mirroring the api-keys-pg route test.
func TestLiteLLMRoutesReturn503WhenNotConfigured(t *testing.T) {
	h := newBareHandler()
	r := newLiteLLMRouter(h)
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v0/management/litellm/users", ""},
		{http.MethodPost, "/v0/management/litellm/users", `{"user_email":"a@b.c"}`},
		{http.MethodGet, "/v0/management/litellm/users/u1", ""},
		{http.MethodPatch, "/v0/management/litellm/users/u1", `{"user_alias":"x"}`},
		{http.MethodDelete, "/v0/management/litellm/users/u1", ""},
		{http.MethodPost, "/v0/management/litellm/users/u1/reset-spend", ""},
		{http.MethodGet, "/v0/management/litellm/users/u1/keys", ""},
		{http.MethodGet, "/v0/management/litellm/keys", ""},
		{http.MethodPost, "/v0/management/litellm/keys", `{"name":"k","user_id":"u1"}`},
		{http.MethodGet, "/v0/management/litellm/keys/k1", ""},
		{http.MethodPatch, "/v0/management/litellm/keys/k1", `{"status":"disabled"}`},
		{http.MethodPut, "/v0/management/litellm/keys/k1/policy", `{"rpm_limit":60}`},
		{http.MethodPost, "/v0/management/litellm/keys/k1/regenerate", ""},
		{http.MethodDelete, "/v0/management/litellm/keys/k1", ""},
		{http.MethodGet, "/v0/management/litellm/settings", ""},
		{http.MethodPut, "/v0/management/litellm/settings", `{"enabled":true}`},
		{http.MethodPost, "/v0/management/litellm/sync/run", ""},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			body := bytes.NewBufferString(tc.body)
			req := httptest.NewRequest(tc.method, tc.path, body)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("%s %s: status = %d; want 503", tc.method, tc.path, w.Code)
			}
		})
	}
}
