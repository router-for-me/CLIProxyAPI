package opencode

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

// newTestModule builds a module wired with a stub auth middleware that aborts with
// 200, so route registration can be probed without invoking the real SDK handlers.
func newTestModule() (*OpenCodeModule, *gin.Engine, *bool) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	authCalled := false
	auth := func(c *gin.Context) {
		authCalled = true
		c.AbortWithStatus(http.StatusOK)
	}
	m := New(WithAuthMiddleware(auth))
	m.modelMapper = NewModelMapper(nil)
	m.registerRoutes(r, &handlers.BaseAPIHandler{}, auth)
	return m, r, &authCalled
}

func TestRegisterRoutes_MergedSurface(t *testing.T) {
	_, r, authCalled := newTestModule()

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/opencode/models"},
		{http.MethodGet, "/opencode/v1/models"},
		{http.MethodPost, "/opencode/v1/chat/completions"},
		{http.MethodPost, "/opencode/v1/completions"},
		{http.MethodPost, "/opencode/v1/responses"},
		{http.MethodPost, "/opencode/v1/messages"},
		{http.MethodPost, "/opencode/v1/messages/count_tokens"},
		{http.MethodGet, "/opencode/v1beta/models"},
		{http.MethodPost, "/opencode/v1beta/models/gemini-2.5-pro:generateContent"},
		{http.MethodGet, "/opencode/v1beta/models/gemini-2.5-pro"},
	}

	for _, tc := range routes {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			*authCalled = false
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code == http.StatusNotFound {
				t.Fatalf("route %s %s not registered", tc.method, tc.path)
			}
			if !*authCalled {
				t.Fatalf("auth middleware not executed for %s %s", tc.method, tc.path)
			}
		})
	}
}

func TestRegisterRoutes_ProviderScopedSurface(t *testing.T) {
	_, r, _ := newTestModule()

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/opencode/provider/openai/models"},
		{http.MethodGet, "/opencode/provider/anthropic/v1/models"},
		{http.MethodPost, "/opencode/provider/openai/v1/chat/completions"},
		{http.MethodPost, "/opencode/provider/openai/v1/completions"},
		{http.MethodPost, "/opencode/provider/openai/v1/responses"},
		{http.MethodPost, "/opencode/provider/anthropic/v1/messages"},
		{http.MethodPost, "/opencode/provider/anthropic/v1/messages/count_tokens"},
		{http.MethodGet, "/opencode/provider/google/v1beta/models"},
		{http.MethodPost, "/opencode/provider/google/v1beta/models/gemini-2.5-pro:generateContent"},
	}

	for _, tc := range routes {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code == http.StatusNotFound {
				t.Fatalf("route %s %s not registered", tc.method, tc.path)
			}
		})
	}
}
