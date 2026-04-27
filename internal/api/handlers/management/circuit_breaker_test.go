package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

type circuitBreakerStatusAPIResponse struct {
	Provider            string `json:"provider"`
	State               string `json:"state"`
	FailureCount        int    `json:"failureCount"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	OpenCycles          int    `json:"openCycles"`
	LastFailure         string `json:"lastFailure"`
	RecoveryAt          string `json:"recoveryAt"`
	ErrorInsightFilters struct {
		Provider string `json:"provider"`
		AuthID   string `json:"authId"`
		Model    string `json:"model"`
	} `json:"errorInsightFilters"`
}

func TestGetCircuitBreaker_IncludesErrorInsightFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reg := registry.GetGlobalRegistry()
	clientID := "test-breaker-insight-auth"
	modelID := "test-breaker-insight-model"
	reg.RegisterClient(clientID, "openai", []*registry.ModelInfo{{ID: modelID}})
	reg.RecordFailure(clientID, modelID, 1, 30)
	t.Cleanup(func() {
		reg.ResetCircuitBreaker(clientID, modelID)
		reg.UnregisterClient(clientID)
	})

	h := &Handler{}
	r := gin.New()
	r.GET("/v0/management/circuit-breaker", h.GetCircuitBreaker)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/circuit-breaker", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	var got map[string]map[string]circuitBreakerStatusAPIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	status, ok := got[clientID][modelID]
	if !ok {
		t.Fatalf("missing status for %s/%s in %+v", clientID, modelID, got)
	}
	if status.ErrorInsightFilters.Provider != "openai" {
		t.Fatalf("errorInsightFilters.provider = %q, want %q", status.ErrorInsightFilters.Provider, "openai")
	}
	if status.ErrorInsightFilters.AuthID != clientID {
		t.Fatalf("errorInsightFilters.authId = %q, want %q", status.ErrorInsightFilters.AuthID, clientID)
	}
	if status.ErrorInsightFilters.Model != modelID {
		t.Fatalf("errorInsightFilters.model = %q, want %q", status.ErrorInsightFilters.Model, modelID)
	}
}
