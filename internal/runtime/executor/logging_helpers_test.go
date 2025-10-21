package executor

import (
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/gin-gonic/gin"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
    logtest "github.com/sirupsen/logrus/hooks/test"
)

// Test that per-request-tps structured log includes provider/model fields when present
func TestPerRequestTPSLog_IncludesProviderModel(t *testing.T) {
    gin.SetMode(gin.TestMode)
    // capture global logs
    hook := logtest.NewGlobal()
    defer hook.Reset()

    // build gin context
    w := httptest.NewRecorder()
    c, _ := gin.CreateTestContext(w)
    cfg := &config.Config{}
    cfg.TPSLog = true
    c.Set("config", cfg)

    // set provider/model on context
    c.Set("API_PROVIDER", "zhipu")
    c.Set("API_MODEL_ID", "glm-4.6")

    // craft one attempt with windows and tokens
    now := time.Now()
    attempts := []*upstreamAttempt{{
        index:         1,
        requestedAt:   now.Add(-4 * time.Second),
        firstOutputAt: now.Add(-2 * time.Second),
        lastOutputAt:  now.Add(-1 * time.Second),
        inputTokens:   100,
        outputTokens:  50,
        response:      &strings.Builder{},
    }}

    updateAggregatedResponse(c, attempts)

    // find last "per-request-tps" log entry
    var found bool
    for i := len(hook.AllEntries()) - 1; i >= 0; i-- {
        e := hook.AllEntries()[i]
        if e.Message == "per-request-tps" {
            // assert provider/model fields present
            if e.Data["provider"] != "zhipu" {
                t.Fatalf("provider field missing or incorrect: %v", e.Data["provider"])
            }
            if e.Data["model"] != "glm-4.6" {
                t.Fatalf("model field missing or incorrect: %v", e.Data["model"])
            }
            if e.Data["provider_model"] != "zhipu/glm-4.6" {
                t.Fatalf("provider_model field missing or incorrect: %v", e.Data["provider_model"])
            }
            found = true
            break
        }
    }
    if !found {
        t.Fatalf("per-request-tps log entry not found")
    }
}
