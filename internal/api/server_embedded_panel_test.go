package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagementPanelServesEmbeddedConsole(t *testing.T) {
	t.Setenv("MANAGEMENT_STATIC_PATH", "")
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/management.html", nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if !strings.Contains(rr.Body.String(), "<title>Papa Proxy</title>") {
		t.Fatalf("management.html is not the embedded Papa Proxy console")
	}
}
