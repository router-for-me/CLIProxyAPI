package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestats"
)

func TestManagementProviderStatisticsRouteRequiresAuthentication(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newTestServer(t)

	missingKeyRequest := httptest.NewRequest(http.MethodGet, "/v0/management/provider-statistics", nil)
	missingKeyResponse := httptest.NewRecorder()
	server.engine.ServeHTTP(missingKeyResponse, missingKeyRequest)
	if missingKeyResponse.Code != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d, want %d body=%s", missingKeyResponse.Code, http.StatusUnauthorized, missingKeyResponse.Body.String())
	}

	authenticatedRequest := httptest.NewRequest(http.MethodGet, "/v0/management/provider-statistics?days=1", nil)
	authenticatedRequest.Header.Set("Authorization", "Bearer test-management-key")
	authenticatedResponse := httptest.NewRecorder()
	server.engine.ServeHTTP(authenticatedResponse, authenticatedRequest)
	if authenticatedResponse.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, want %d body=%s", authenticatedResponse.Code, http.StatusOK, authenticatedResponse.Body.String())
	}

	var report usagestats.Report
	if errUnmarshal := json.Unmarshal(authenticatedResponse.Body.Bytes(), &report); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v body=%s", errUnmarshal, authenticatedResponse.Body.String())
	}
	if report.Range.Days != 1 {
		t.Fatalf("range days = %d, want 1", report.Range.Days)
	}
}
