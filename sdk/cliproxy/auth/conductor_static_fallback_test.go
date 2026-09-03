package auth

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestAuthSupportsRouteModelFallback(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	globalReg := registry.GetGlobalRegistry()

	authAntigravity := &Auth{
		ID:       "auth-ag-1",
		Provider: "antigravity",
	}
	authClaude := &Auth{
		ID:       "auth-claude-1",
		Provider: "claude",
	}

	// 1. Static model present in Antigravity catalog (e.g. gemini-3.7-flash-high)
	if !mgr.authSupportsRouteModel(globalReg, authAntigravity, "gemini-3.7-flash-high") {
		t.Fatal("authSupportsRouteModel(authAntigravity, gemini-3.7-flash-high) = false, want true via static catalog fallback")
	}
	if mgr.authSupportsRouteModel(globalReg, authClaude, "gemini-3.7-flash-high") {
		t.Fatal("authSupportsRouteModel(authClaude, gemini-3.7-flash-high) = true, want false")
	}

	// 2. Static model present in both Claude and Antigravity catalogs (e.g. claude-sonnet-4-6)
	if !mgr.authSupportsRouteModel(globalReg, authAntigravity, "claude-sonnet-4-6") {
		t.Fatal("authSupportsRouteModel(authAntigravity, claude-sonnet-4-6) = false, want true")
	}
	if !mgr.authSupportsRouteModel(globalReg, authClaude, "claude-sonnet-4-6") {
		t.Fatal("authSupportsRouteModel(authClaude, claude-sonnet-4-6) = false, want true")
	}

	// 3. Scheduler supportsModel test
	metaAG := &scheduledAuthMeta{
		auth: authAntigravity,
	}
	if !metaAG.supportsModel("gemini-3.7-flash-high") {
		t.Fatal("metaAG.supportsModel(gemini-3.7-flash-high) = false, want true")
	}
	if metaAG.supportsModel("non-existent-model") {
		t.Fatal("metaAG.supportsModel(non-existent-model) = true, want false")
	}
}
