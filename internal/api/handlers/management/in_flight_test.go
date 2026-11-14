package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestGetInFlightRequestsReturnsSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	h := &Handler{authManager: manager}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/observability/requests/in-flight", nil)

	h.GetInFlightRequests(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		InFlight        int64            `json:"in_flight"`
		OldestStartedAt string           `json:"oldest_started_at"`
		ByProvider      map[string]int64 `json:"by_provider"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if payload.InFlight != 0 {
		t.Fatalf("in_flight = %d, want 0", payload.InFlight)
	}
}

func TestGetInFlightRequestsNilManager(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/observability/requests/in-flight", nil)

	h.GetInFlightRequests(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		InFlight int64 `json:"in_flight"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if payload.InFlight != 0 {
		t.Fatalf("in_flight = %d, want 0", payload.InFlight)
	}
}
