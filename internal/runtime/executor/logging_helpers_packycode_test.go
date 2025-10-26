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

// Validate per-request-tps log includes provider=packycode when set on context
func TestPerRequestTPSLog_PackycodeProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hook := logtest.NewGlobal()
	defer hook.Reset()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	cfg := &config.Config{}
	cfg.TPSLog = true
	c.Set("config", cfg)

	c.Set("API_PROVIDER", "packycode")
	c.Set("API_MODEL_ID", "gpt-5")

	now := time.Now()
	attempts := []*upstreamAttempt{{
		index:         1,
		requestedAt:   now.Add(-3 * time.Second),
		firstOutputAt: now.Add(-2 * time.Second),
		lastOutputAt:  now.Add(-1 * time.Second),
		inputTokens:   20,
		outputTokens:  10,
		response:      &strings.Builder{},
	}}

	updateAggregatedResponse(c, attempts)

	var found bool
	for i := len(hook.AllEntries()) - 1; i >= 0; i-- {
		e := hook.AllEntries()[i]
		if e.Message == "per-request-tps" {
			if e.Data["provider"] != "packycode" {
				t.Fatalf("expected provider=packycode, got %v", e.Data["provider"])
			}
			if e.Data["provider_model"] != "packycode/gpt-5" {
				t.Fatalf("expected provider_model=packycode/gpt-5, got %v", e.Data["provider_model"])
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("per-request-tps log entry not found")
	}
}
