package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func setupRoutingRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewRoutingHandler()
	r.POST("/v1/routing/select", h.POSTRoutingSelect)
	return r
}

func TestPOSTRoutingSelectReturnsOptimalModel(t *testing.T) {
	router := setupRoutingRouter()

	reqBody := RoutingSelectRequest{
		TaskComplexity:  "NORMAL",
		MaxCostPerCall:  0.01,
		MaxLatencyMs:    5000,
		MinQualityScore: 0.75,
	}

	payload, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/v1/routing/select", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp RoutingSelectResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.ModelID == "" {
		t.Error("model_id is empty")
	}
	if resp.Provider == "" {
		t.Error("provider is empty")
	}
	if resp.EstimatedCost == 0 {
		t.Error("estimated_cost is zero")
	}
	if resp.QualityScore == 0 {
		t.Error("quality_score is zero")
	}
}

func TestPOSTRoutingSelectReturns400OnImpossibleConstraints(t *testing.T) {
	router := setupRoutingRouter()

	reqBody := RoutingSelectRequest{
		MaxCostPerCall:  0.0001,
		MaxLatencyMs:    10,
		MinQualityScore: 0.99,
	}

	payload, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/v1/routing/select", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestPOSTRoutingSelectReturns400OnBadJSON(t *testing.T) {
	router := setupRoutingRouter()

	req := httptest.NewRequest("POST", "/v1/routing/select", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestPOSTRoutingSelectConstraintsSatisfied(t *testing.T) {
	router := setupRoutingRouter()

	reqBody := RoutingSelectRequest{
		TaskComplexity:  "FAST",
		MaxCostPerCall:  0.005,
		MaxLatencyMs:    2000,
		MinQualityScore: 0.70,
	}

	payload, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/v1/routing/select", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp RoutingSelectResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.EstimatedCost > reqBody.MaxCostPerCall {
		t.Errorf("cost %.4f exceeds max %.4f", resp.EstimatedCost, reqBody.MaxCostPerCall)
	}
	if resp.EstimatedLatencyMs > reqBody.MaxLatencyMs {
		t.Errorf("latency %d exceeds max %d", resp.EstimatedLatencyMs, reqBody.MaxLatencyMs)
	}
	if resp.QualityScore < reqBody.MinQualityScore {
		t.Errorf("quality %.2f below min %.2f", resp.QualityScore, reqBody.MinQualityScore)
	}
}
