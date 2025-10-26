package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

// Test that GET /v0/management/tps supports provider+model filtering
func TestGetTPSAggregates_FilterByProviderAndModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Prepare some tagged TPS samples within window
	usage.RecordTPSSampleTagged("zhipu", "glm-4.6", 10.0, 12.0)
	usage.RecordTPSSampleTagged("openai", "gpt-4o", 5.0, 6.0)

	// Build a minimal router mounting only the tested handler
	h := &Handler{}
	r := gin.New()
	r.GET("/v0/management/tps", h.GetTPSAggregates)

	// Request with provider+model filter
	req := httptest.NewRequest(http.MethodGet, "/v0/management/tps?window=5m&provider=zhipu&model=glm-4.6", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	var body struct {
		TPS usage.TPSAggregateSnapshot `json:"tps"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// Expect completion/total counts reflect only the filtered provider/model tuple
	if body.TPS.Completion.Count != 1 {
		t.Fatalf("expected completion.count=1, got %d", body.TPS.Completion.Count)
	}
	if body.TPS.Total.Count != 1 {
		t.Fatalf("expected total.count=1, got %d", body.TPS.Total.Count)
	}

	// Basic sanity on averages (rounded to 2 decimals in impl)
	if body.TPS.Completion.Avg <= 0 || body.TPS.Total.Avg <= 0 {
		t.Fatalf("expected positive averages, got completion.avg=%.2f total.avg=%.2f", body.TPS.Completion.Avg, body.TPS.Total.Avg)
	}

	// Small delay to avoid interfering with other tests re: time windows
	time.Sleep(10 * time.Millisecond)
}
