package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPriorityManagementRouteUsesManagementAuthenticationAndRuntimeHeaders(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newTestServer(t)

	unauthorizedReq := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/priority", strings.NewReader(`{}`))
	unauthorizedReq.Header.Set("Content-Type", "application/json")
	unauthorizedRR := httptest.NewRecorder()
	server.engine.ServeHTTP(unauthorizedRR, unauthorizedReq)
	if unauthorizedRR.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d body=%s", unauthorizedRR.Code, http.StatusUnauthorized, unauthorizedRR.Body.String())
	}

	authorizedReq := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/priority", strings.NewReader(`{}`))
	authorizedReq.Header.Set("Content-Type", "application/json")
	authorizedReq.Header.Set("X-Management-Key", "test-management-key")
	authorizedRR := httptest.NewRecorder()
	server.engine.ServeHTTP(authorizedRR, authorizedReq)
	if authorizedRR.Code != http.StatusBadRequest {
		t.Fatalf("authorized status = %d, want %d body=%s", authorizedRR.Code, http.StatusBadRequest, authorizedRR.Body.String())
	}
	for _, header := range []string{"X-CPA-VERSION", "X-CPA-COMMIT", "X-CPA-BUILD-DATE"} {
		if _, ok := authorizedRR.Header()[http.CanonicalHeaderKey(header)]; !ok {
			t.Fatalf("response missing runtime header %s", header)
		}
	}
}
