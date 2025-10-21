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

// Ensure provider=packycode can be filtered in TPS aggregates
func TestGetTPSAggregates_FilterByProviderAndModel_Packycode(t *testing.T) {
    gin.SetMode(gin.TestMode)

    // seed windowed samples
    usage.RecordTPSSampleTagged("packycode", "gpt-5", 7.5, 8.1)
    usage.RecordTPSSampleTagged("zhipu", "glm-4.6", 2.0, 2.2)

    h := &Handler{}
    r := gin.New()
    r.GET("/v0/management/tps", h.GetTPSAggregates)

    req := httptest.NewRequest(http.MethodGet, "/v0/management/tps?window=5m&provider=packycode&model=gpt-5", nil)
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
    if body.TPS.Completion.Count != 1 || body.TPS.Total.Count != 1 {
        t.Fatalf("expected counts=1, got completion=%d total=%d", body.TPS.Completion.Count, body.TPS.Total.Count)
    }
    if body.TPS.Completion.Avg <= 0 || body.TPS.Total.Avg <= 0 {
        t.Fatalf("expected positive averages, got completion=%.2f total=%.2f", body.TPS.Completion.Avg, body.TPS.Total.Avg)
    }

    time.Sleep(10 * time.Millisecond)
}
