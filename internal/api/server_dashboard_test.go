package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMountDashboardRoutes serves the embedded SPA under /dashboard. The
// Fileserver in mountDashboardRoutes strips the /dashboard prefix so requests
// resolve against the FS root (index.html, assets/...). This guards against a
// regression where bare /dashboard and deep client-side routes 404'd.
func TestMountDashboardRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	s := &Server{engine: engine}
	s.mountDashboardRoutes()

	// /dashboard redirects to /dashboard/ so the directory index is served.
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("/dashboard: got status %d, want %d", w.Code, http.StatusMovedPermanently)
	}

	// /dashboard/ serves index.html (the SPA shell).
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dashboard/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/dashboard/: got status %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("/dashboard/: got Content-Type %q, want text/html", ct)
	}
	if !strings.Contains(w.Body.String(), "<div id=\"root\"></div>") {
		t.Errorf("/dashboard/: index.html did not render the SPA root")
	}

	// A hashed asset (read from index.html) is served with the right type.
	idx := w.Body.String()
	asset := extractAssetPath(t, idx)
	if asset != "" {
		aw := httptest.NewRecorder()
		engine.ServeHTTP(aw, httptest.NewRequest(http.MethodGet, "/dashboard"+asset, nil))
		if aw.Code != http.StatusOK {
			t.Errorf("asset %s: got status %d, want %d", asset, aw.Code, http.StatusOK)
		}
	}

	// A deep client-side route (not a real file) falls back to index.html.
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dashboard/api-keys/some-id", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/dashboard/api-keys/some-id: got status %d, want %d", w.Code, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), "<div id=\"root\"></div>") {
		t.Errorf("SPA fallback did not serve index.html")
	}
}

// extractAssetPath parses the /assets/*.js path out of index.html, or returns
// "" if index.html has no such reference. Keeps the test independent of the
// Vite content-hashed bundle name.
func extractAssetPath(t *testing.T, idx string) string {
	t.Helper()
	srcIdx := strings.Index(idx, `src="/assets/`)
	if srcIdx < 0 {
		return ""
	}
	rest := idx[srcIdx+len(`src="`):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}
