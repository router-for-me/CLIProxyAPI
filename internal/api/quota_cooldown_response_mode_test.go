package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestServerAppliesQuotaCooldownResponseModeAtStartupAndReload(t *testing.T) {
	t.Cleanup(func() { auth.SetQuotaCooldownResponseMode("") })
	auth.SetQuotaCooldownResponseMode("")
	cfg := &proxyconfig.Config{SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}}, QuotaCooldownResponseMode: "usage-limit"}
	server := newTestServerWithConfig(t, cfg)
	if !auth.QuotaCooldownUsageLimitModeEnabled() {
		t.Fatal("startup did not apply quota-response-mode usage-limit")
	}
	reloaded := *cfg
	reloaded.QuotaCooldownResponseMode = ""
	server.UpdateClients(&reloaded)
	if auth.QuotaCooldownUsageLimitModeEnabled() {
		t.Fatal("reload to the default kept the usage-limit mode")
	}
}

// Browser clients may read the usage-limit headers only in that mode; the
// default expose list is unchanged.
func TestCorsExposesUsageLimitHeadersOnlyInUsageLimitMode(t *testing.T) {
	t.Cleanup(func() { auth.SetQuotaCooldownResponseMode("") })
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(corsMiddleware())
	engine.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })
	for mode, want := range map[string]bool{"": false, "usage-limit": true} {
		auth.SetQuotaCooldownResponseMode(mode)
		rr := httptest.NewRecorder()
		engine.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/probe", nil))
		exposed := rr.Header().Get("Access-Control-Expose-Headers")
		if !strings.HasPrefix(exposed, corsExposedResponseHeadersJoined) ||
			strings.Contains(exposed, "X-Should-Retry, X-Codex-Primary-Used-Percent") != want || (exposed == corsExposedResponseHeadersJoined) == want {
			t.Fatalf("mode %q: Access-Control-Expose-Headers = %q", mode, exposed)
		}
	}
}
