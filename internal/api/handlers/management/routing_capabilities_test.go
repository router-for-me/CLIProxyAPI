package management

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRoutingCapabilitiesDescribeRuntimeOwner(t *testing.T) {
	for _, local := range []bool{false, true} {
		var manager *coreauth.Manager
		if local {
			manager = coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
		}
		handler := NewHandler(&config.Config{}, "", manager)
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		handler.GetRoutingCapabilities(ctx)
		var result map[string]bool
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !result["provider_affinity_overrides"] || !result["credential_priority_cas"] || result["local_builtin_selector"] != local {
			t.Fatalf("capabilities = %v", result)
		}
	}
}
