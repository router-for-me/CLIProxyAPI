package auth

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestAuthSupportsRouteModelMatchesForcedCompatPrefix(t *testing.T) {
	t.Parallel()

	const authID = "test-compat-prefix-route-model"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai-compatible-opencode", []*registry.ModelInfo{{ID: "opencode/glm-5.3-flash"}})
	defer reg.UnregisterClient(authID)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: authID, Prefix: "opencode"}

	if !manager.authSupportsRouteModel(reg, auth, "glm-5.3-flash") {
		t.Fatal("bare route model should match a forced provider-prefixed registration")
	}
	if manager.authSupportsRouteModel(reg, auth, "opencode/other-model") {
		t.Fatal("unregistered route model should remain unavailable")
	}
}
